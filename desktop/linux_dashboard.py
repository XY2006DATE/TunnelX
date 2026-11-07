import argparse
from urllib.parse import urlsplit, urlunsplit

import gi

gi.require_version("Gtk", "3.0")
gi.require_version("WebKit2", "4.1")
from gi.repository import Gio, GLib, Gtk, WebKit2


def dashboard_url(value):
    parts = urlsplit(value.strip())
    if (
        parts.scheme not in ("http", "https")
        or parts.hostname not in ("127.0.0.1", "localhost", "::1")
        or parts.username is not None
        or parts.path not in ("", "/")
        or parts.query
        or parts.fragment
        or parts.port == 0
    ):
        raise ValueError("请输入本机管理地址，例如 http://127.0.0.1:7100/")
    return urlunsplit((parts.scheme, parts.netloc, "/", "", ""))


def origin(uri):
    parts = urlsplit(uri)
    return parts.scheme, parts.hostname, parts.port or (443 if parts.scheme == "https" else 80)


class DashboardWindow(Gtk.Window):
    def __init__(self, component, uri):
        super().__init__(title=f"TunnelX {component.title()}")
        self.component = component
        self.uri = dashboard_url(uri)
        self.set_default_size(1280, 820)
        self.set_icon_name(f"tunnelx-{component}")

        layout = Gtk.Box(orientation=Gtk.Orientation.VERTICAL)
        toolbar = Gtk.Box(spacing=8, margin=8)
        self.address = Gtk.Entry(text=self.uri)
        self.address.set_tooltip_text("本机 Dashboard 地址")
        self.address.connect("activate", self.load_address)
        toolbar.pack_start(self.address, True, True, 0)
        for label, action in (("打开 / 刷新", self.load_address), ("在浏览器打开", self.open_browser)):
            button = Gtk.Button(label=label)
            button.connect("clicked", action)
            toolbar.pack_start(button, False, False, 0)
        layout.pack_start(toolbar, False, False, 0)

        self.status = Gtk.Label(xalign=0, margin_start=8, margin_end=8, wrap=True)
        layout.pack_start(self.status, False, False, 0)
        context = WebKit2.WebContext.new_ephemeral()
        self.view = WebKit2.WebView.new_with_context(context)
        self.view.connect("decide-policy", self.check_navigation)
        self.view.connect("load-failed", self.load_failed)
        layout.pack_start(self.view, True, True, 0)
        self.add(layout)
        self.load_address()

    def load_address(self, *_):
        try:
            self.uri = dashboard_url(self.address.get_text())
        except ValueError as error:
            self.status.set_text(str(error))
            return
        self.status.set_text("")
        self.view.load_uri(self.uri)

    def open_browser(self, *_):
        try:
            Gio.AppInfo.launch_default_for_uri(dashboard_url(self.address.get_text()), None)
        except (ValueError, GLib.Error) as error:
            self.status.set_text(str(error))

    def check_navigation(self, _view, decision, kind):
        if kind == WebKit2.PolicyDecisionType.NAVIGATION_ACTION:
            uri = decision.get_navigation_action().get_request().get_uri()
            try:
                if origin(uri) == origin(self.uri):
                    return False
            except ValueError:
                pass
        elif kind != WebKit2.PolicyDecisionType.NEW_WINDOW_ACTION:
            return False
        decision.ignore()
        return True

    def load_failed(self, _view, _event, _uri, error):
        if not error.matches(WebKit2.network_error_quark(), WebKit2.NetworkError.CANCELLED):
            self.status.set_text(
                f"无法打开管理页。请检查地址和服务状态（systemctl status tunnelx-{self.component}）。{error.message}"
            )
        return False


def main():
    parser = argparse.ArgumentParser(description="Open the local TunnelX service Dashboard")
    parser.add_argument("component", choices=("client", "server"))
    parser.add_argument("--url", type=dashboard_url)
    args = parser.parse_args()
    port = 7101 if args.component == "client" else 7100
    window = DashboardWindow(args.component, args.url or f"http://127.0.0.1:{port}/")
    window.connect("destroy", Gtk.main_quit)
    window.show_all()
    Gtk.main()


if __name__ == "__main__":
    main()
