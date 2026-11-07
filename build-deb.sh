#!/usr/bin/env bash
set -Eeuo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARCH=native
SKIP_WEB=false
while (($#)); do
  case "$1" in
    --arch) ARCH="${2:?--arch requires a value}"; shift 2 ;;
    --skip-web) SKIP_WEB=true; shift ;;
    -h|--help)
      echo 'Usage: ./build-deb.sh [--arch native|amd64|arm64] [--skip-web]'
      echo 'Builds Client and Server packages with desktop and Web Dashboards.'
      exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done
[[ "$ARCH" != native ]] || ARCH="$(uname -m)"
case "$ARCH" in
  amd64|x86_64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
for command in go node dpkg-deb sha256sum; do command -v "$command" >/dev/null; done
cd "$ROOT_DIR"
OUTPUT_DIR="$ROOT_DIR/dist/deb-$ARCH"
mkdir -p "$OUTPUT_DIR"
STAGING="$(mktemp -d)"
trap 'rm -rf "$STAGING"' EXIT
ARTIFACTS=()
for component in client server; do
  dashboard="$ROOT_DIR/$component/${component}dashboard"
  version="$(node -p "require('$dashboard/src-tauri/tauri.conf.json').version")"
  if [[ "$SKIP_WEB" != true ]]; then
    (cd "$dashboard" && npm ci --no-audit --no-fund && npm run build)
  fi
  display="${component^}"
  package="tunnelx-$component"
  launcher="$package-gui"
  state="/var/lib/$package"
  pkg="$STAGING/$package"
  mkdir -p "$pkg/DEBIAN" "$pkg/usr/lib/$package" "$pkg/usr/lib/systemd/system" \
    "$pkg/usr/bin" "$pkg/usr/share/applications" "$pkg/usr/share/icons/hicolor/scalable/apps"
  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags='-s -w' \
    -o "$pkg/usr/lib/$package/$package" "./$component"
  cp "$component/$component.yaml" "$pkg/usr/lib/$package/$component.yaml.default"
  chmod 755 "$pkg/usr/lib/$package/$package"
  cat > "$pkg/DEBIAN/control" <<EOF
Package: $package
Version: $version
Section: net
Priority: optional
Architecture: $ARCH
Maintainer: TunnelX Team <support@tunnelx.io>
Depends: systemd, passwd, python3, python3-gi, gir1.2-gtk-3.0, gir1.2-webkit2-4.1
Conflicts: $launcher
Replaces: $launcher
Description: TunnelX $display with desktop and Web Dashboards
 Secure tunneling with a desktop application and browser-based management.
EOF
  cat > "$pkg/DEBIAN/postinst" <<EOF
#!/bin/sh
set -e
if [ "\$1" = configure ]; then
  if ! getent passwd $package >/dev/null; then
    useradd --system --user-group --home-dir $state --no-create-home --shell /usr/sbin/nologin $package
  fi
  install -d -m 0750 -o $package -g $package $state
  if [ ! -f $state/$component.yaml ]; then
    install -m 0640 -o $package -g $package /usr/lib/$package/$component.yaml.default $state/$component.yaml
  fi
  if [ -d /run/systemd/system ]; then
    systemctl daemon-reload
    if systemctl is-active --quiet $package.service; then systemctl try-restart $package.service; fi
  fi
  echo 'Start with: systemctl enable --now $package'
  echo 'Set the Dashboard administrator password from the desktop app or browser on first use.'
fi
EOF
  cat > "$pkg/DEBIAN/prerm" <<EOF
#!/bin/sh
set -e
if [ "\$1" = remove ] && [ -d /run/systemd/system ]; then
  systemctl stop $package.service || true
  systemctl disable $package.service || true
fi
EOF
  cat > "$pkg/DEBIAN/postrm" <<EOF
#!/bin/sh
set -e
if [ "\$1" = purge ]; then
  rm -rf $state
  if getent passwd $package >/dev/null; then userdel $package; fi
fi
if [ -d /run/systemd/system ]; then systemctl daemon-reload; fi
EOF
  chmod 755 "$pkg/DEBIAN/"{postinst,prerm,postrm}
  cat > "$pkg/usr/lib/systemd/system/$package.service" <<EOF
[Unit]
Description=TunnelX $display Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$package
Group=$package
StateDirectory=$package
StateDirectoryMode=0750
WorkingDirectory=$state
ExecStart=/usr/lib/$package/$package $state/$component.yaml
Restart=on-failure
RestartSec=5s
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$state
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF
  install -m 644 desktop/linux_dashboard.py "$pkg/usr/lib/$package/dashboard.py"
  install -m 644 "$dashboard/public/tunnelx.svg" "$pkg/usr/share/icons/hicolor/scalable/apps/$package.svg"
  cat > "$pkg/usr/bin/$launcher" <<EOF
#!/bin/sh
exec /usr/bin/python3 /usr/lib/$package/dashboard.py $component "\$@"
EOF
  chmod 755 "$pkg/usr/bin/$launcher"
  cat > "$pkg/usr/share/applications/$launcher.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=TunnelX $display
Comment=Open the local TunnelX Dashboard
Exec=$launcher
Icon=$package
Terminal=false
Categories=Network;
EOF
  artifact="TunnelX-$display-$version-linux-$ARCH.deb"
  dpkg-deb --root-owner-group --build "$pkg" "$OUTPUT_DIR/$artifact"
  ARTIFACTS+=("$artifact")
done
(cd "$OUTPUT_DIR" && sha256sum "${ARTIFACTS[@]}" > SHA256SUMS)
printf '\nDebian packages: %s\n' "$OUTPUT_DIR"
