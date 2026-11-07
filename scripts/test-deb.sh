#!/usr/bin/env bash
# Run only in a disposable Debian/Ubuntu test environment, as root.
set -Eeuo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
[[ "$(id -u)" == 0 ]] || { echo 'Run as root in a test environment'; exit 1; }
for component in client server; do
  package="tunnelx-$component"
  if dpkg-query -W "$package" >/dev/null 2>&1 || [[ -e "/var/lib/$package" ]] || getent passwd "$package" >/dev/null; then
    echo "Refusing to overwrite existing $package installation/state" >&2
    exit 1
  fi
done
cleanup() { dpkg --purge tunnelx-client tunnelx-server >/dev/null 2>&1 || true; }
trap cleanup EXIT
cd dist/deb-amd64
sha256sum -c SHA256SUMS
for component in client server; do
  version="$(node -p "require('$ROOT/$component/${component}dashboard/src-tauri/tauri.conf.json').version")"
  artifact="TunnelX-${component^}-$version-linux-amd64.deb"
  [[ "$(dpkg-deb -f "$artifact" Package)" == "tunnelx-$component" ]]
  [[ "$(dpkg-deb -f "$artifact" Version)" == "$version" ]]
  [[ "$(dpkg-deb -f "$artifact" Architecture)" == amd64 ]]
  dpkg-deb --info "$artifact"
  echo "PASS $component Debian package version: $version"
done
dpkg -i ./*.deb
systemd-analyze verify /usr/lib/systemd/system/tunnelx-{client,server}.service
for component in client server; do
  [[ "$(stat -c %U /usr/lib/tunnelx-$component/tunnelx-$component)" == root ]]
done
cd "$ROOT"
python3 scripts/smoke-test.py --client /usr/lib/tunnelx-client/tunnelx-client --server /usr/lib/tunnelx-server/tunnelx-server
systemctl start tunnelx-server tunnelx-client
python3 - <<'PY'
import runpy
smoke = runpy.run_path('scripts/smoke-test.py')
API, eventually = smoke['API'], smoke['eventually']
for name, port in [('client', 7101), ('server', 7100)]:
    api = API(port)
    eventually(lambda: api.request('/api/setup-status') == {'configured': False})
    assert api.request('/api/setup', {'password': 'Deb-package-test-123'})['success']
    api.request('/login', {'password': 'Deb-package-test-123'}, form=name == 'server')
    api.request('/api/stats')
    print(f'PASS {name} systemd service: startup, setup, login')
PY
before="$(sha256sum /var/lib/tunnelx-{client,server}/{dashboard.password,*.yaml})"
systemctl restart tunnelx-client tunnelx-server
dpkg -i dist/deb-amd64/*.deb
after="$(sha256sum /var/lib/tunnelx-{client,server}/{dashboard.password,*.yaml})"
[[ "$before" == "$after" ]]
systemctl is-active --quiet tunnelx-client tunnelx-server
echo 'PASS reinstall/restart: configurations and passwords preserved'
dpkg --purge tunnelx-client
systemctl is-active --quiet tunnelx-server
[[ -f /var/lib/tunnelx-server/dashboard.password ]]
[[ ! -e /var/lib/tunnelx-client ]]
echo 'PASS client purge: server service and state preserved'
dpkg --purge tunnelx-server
[[ ! -e /var/lib/tunnelx-server ]]
echo 'PASS server purge: test installation removed'
trap - EXIT
echo 'ALL DEB INSTALLATION TESTS PASSED'
