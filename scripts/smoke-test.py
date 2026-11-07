"""Exercise release binaries with isolated configs and real TCP/UDP traffic."""
import argparse
import contextlib
import http.cookiejar
import json
from pathlib import Path
import socket
import socketserver
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


def eventually(check, timeout=20):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            result = check()
            if result:
                return result
        except (OSError, AssertionError, ValueError) as error:
            last = error
        time.sleep(0.2)
    raise AssertionError(f'Timed out: {last}')


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class API:
    def __init__(self, port):
        self.base = f'http://127.0.0.1:{port}'
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, path, data=None, form=False, expected=200):
        headers = {}
        if data is not None:
            headers['Content-Type'] = 'application/x-www-form-urlencoded' if form else 'application/json'
            data = (urllib.parse.urlencode(data) if form else json.dumps(data)).encode()
        request = urllib.request.Request(self.base + path, data=data, headers=headers)
        try:
            response = self.opener.open(request, timeout=3)
        except urllib.error.HTTPError as error:
            response = error
        body = response.read()
        assert response.code == expected, (path, response.code, body)
        if 'application/json' in response.headers.get('Content-Type', ''):
            return json.loads(body)
        return body


class TCPEcho(socketserver.BaseRequestHandler):
    def handle(self):
        while True:
            try:
                data = self.request.recv(65536)
            except ConnectionResetError:
                return
            if not data:
                return
            self.request.sendall(data)


class UDPEcho(socketserver.BaseRequestHandler):
    def handle(self):
        data, sock = self.request
        sock.sendto(data, self.client_address)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--client', required=True, type=Path)
    parser.add_argument('--server', required=True, type=Path)
    parser.add_argument('--websocket', action='store_true', help='Use the server /proxy WebSocket endpoint for all tunnel channels')
    parser.add_argument('--no-server-dashboard', action='store_true', help='Use preconfigured routes and verify WebSocket works with the server Dashboard disabled')
    args = parser.parse_args()
    binaries = {name: getattr(args, name).resolve() for name in ('client', 'server')}
    ports = {name: free_port() for name in ('client', 'server', 'tcp', 'udp')}
    processes, logs = {}, {}
    with tempfile.TemporaryDirectory(prefix='tunnelx-smoke-') as temporary, contextlib.ExitStack() as stack:
        root = Path(temporary)
        for name in ('client', 'server'):
            directory = root / name
            directory.mkdir()
            config = {
                'log': {'level': 'info', 'file': f'{name}.log'},
                'dashboard': {'enable': True, 'port': ports[name], 'password_file': 'dashboard.password'},
                'tls': {'enable': False}, 'auth': {'token': 'smoke-test-token-123'},
                'heartbeat': {'interval': 2, 'timeout': 15}, 'heartbeat_timeout': 15,
            }
            if name == 'server':
                config.update(server={'bind_addr': '127.0.0.1', 'bind_port': ports[name]},
                              port_pool={'start': 1024, 'end': 65535})
            else:
                config.update(server_addr='', server_port=ports['server'], proxies=[])
            (directory / f'{name}.yaml').write_text(json.dumps(config), encoding='utf-8')

        def stop(name):
            process = processes.get(name)
            if process and process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=6)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()

        def start(name):
            logs[name] = stack.enter_context((root / f'{name}-console.log').open('ab'))
            processes[name] = subprocess.Popen(
                [str(binaries[name]), f'{name}.yaml'], cwd=root / name,
                stdout=logs[name], stderr=subprocess.STDOUT)

        tcp = stack.enter_context(socketserver.ThreadingTCPServer(('127.0.0.1', 0), TCPEcho))
        udp = stack.enter_context(socketserver.ThreadingUDPServer(('127.0.0.1', 0), UDPEcho))
        for service in (tcp, udp):
            service.daemon_threads = True
            threading.Thread(target=service.serve_forever, daemon=True).start()
            stack.callback(service.shutdown)
        stack.callback(lambda: [stop(name) for name in ('client', 'server')])
        if args.no_server_dashboard:
            server_config = root / 'server/server.yaml'
            config = json.loads(server_config.read_text(encoding='utf-8'))
            config['dashboard']['enable'] = False
            server_config.write_text(json.dumps(config), encoding='utf-8')
            client_config = root / 'client/client.yaml'
            config = json.loads(client_config.read_text(encoding='utf-8'))
            config['proxies'] = [
                {'name': f'smoke-{kind}', 'type': kind, 'local_ip': '127.0.0.1',
                 'local_port': service.server_address[1], 'remote_port': ports[kind],
                 'server_addr': f"ws://127.0.0.1:{ports['server']}/proxy" if args.websocket else '127.0.0.1',
                 'server_port': ports['server'], 'server_token': 'smoke-test-token-123', 'server_tls': False}
                for kind, service in [('tcp', tcp), ('udp', udp)]
            ]
            client_config.write_text(json.dumps(config), encoding='utf-8')
        apis = {name: API(ports[name]) for name in ('client', 'server')}
        try:
            for name, api in apis.items():
                start(name)
                if name == 'server' and args.no_server_dashboard:
                    eventually(lambda: api.request('/api/setup-status', expected=404))
                    assert api.request('/proxy', expected=426)
                    print('PASS server: Dashboard disabled, WebSocket endpoint remains available', flush=True)
                    continue
                eventually(lambda: api.request('/api/setup-status') == {'configured': False})
                assert b'<html' in api.request('/')
                api.request('/api/stats', expected=428)
                api.request('/api/setup', {'password': 'short'}, expected=400)
                assert api.request('/api/setup', {'password': 'Smoke-password-123'})['success']
                api.request('/api/setup', {'password': 'Smoke-password-123'}, expected=409)
                api.request('/login', {'password': 'wrong-password'}, form=name == 'server', expected=401)
                api.request('/login', {'password': 'Smoke-password-123'}, form=name == 'server')
                api.request('/api/stats')
                print(f'PASS {name}: startup, embedded Dashboard, setup, authentication', flush=True)

            client, server = apis['client'], apis['server']
            for kind, local in (('tcp', tcp), ('udp', udp)):
                if args.no_server_dashboard:
                    continue
                assert client.request('/api/request-proxy', {
                    'server_addr': f"http://127.0.0.1:{ports['server']}/proxy" if args.websocket else '127.0.0.1', 'server_port': ports['server'],
                    'server_tls': False, 'token': 'smoke-test-token-123',
                    'local_ip': '127.0.0.1', 'local_port': local.server_address[1], 'proxy_type': kind,
                })['success']
                pending = eventually(lambda: [r for r in (server.request('/api/pending-requests') or []) if r['proxy_type'] == kind])
                assert server.request('/api/approve-request', {
                    'request_id': pending[0]['id'], 'remote_port': ports[kind], 'proxy_name': f'smoke-{kind}',
                })['success']

            def tcp_roundtrip():
                payload = b'TunnelX TCP release smoke test\x00' * 4096
                with socket.create_connection(('127.0.0.1', ports['tcp']), timeout=3) as sock:
                    sock.sendall(payload)
                    received = bytearray()
                    while len(received) < len(payload):
                        chunk = sock.recv(65536)
                        if not chunk:
                            break
                        received.extend(chunk)
                assert received == payload
                return True

            eventually(tcp_roundtrip)
            print('PASS TCP: request, approval, 120 KiB binary roundtrip', flush=True)

            def udp_roundtrip():
                payload = b'TunnelX UDP release test\x00' * 20
                with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
                    sock.settimeout(2)
                    sock.sendto(payload, ('127.0.0.1', ports['udp']))
                    received, _ = sock.recvfrom(4096)
                assert received == payload
                return True

            eventually(udp_roundtrip)
            print('PASS UDP: request, approval, binary datagram roundtrip', flush=True)
            if args.websocket:
                saved = (root / 'client/client.yaml').read_text(encoding='utf-8')
                assert f"ws://127.0.0.1:{ports['server']}/proxy" in saved, 'WebSocket URL was not persisted'
                print('PASS WebSocket URL and path saved in approved proxy configuration', flush=True)
            stop('client')
            start('client')
            eventually(lambda: client.request('/api/setup-status')['configured'])
            eventually(tcp_roundtrip)
            eventually(udp_roundtrip)
            print('PASS client restart: password and approved TCP/UDP proxies restored', flush=True)
        except Exception:
            for name in processes:
                print(f'--- {name} process log ---\n{(root / (name + "-console.log")).read_text(errors="replace")}')
            raise
    print('ALL RELEASE SMOKE TESTS PASSED', flush=True)


if __name__ == '__main__':
    main()
