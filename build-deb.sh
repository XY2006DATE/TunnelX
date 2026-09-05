#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REQUESTED_ARCH="native"

usage() {
  cat <<'EOF'
Build the TunnelX Client and Server Debian packages.

Usage:
  ./build-deb.sh [--arch native|amd64|arm64]

Outputs (two artifacts):
  TunnelX-Client-<version>-linux-<arch>.deb
  TunnelX-Server-<version>-linux-<arch>.deb

Examples:
  ./build-deb.sh
  ./build-deb.sh --arch amd64
  ./build-deb.sh --arch arm64
EOF
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

need_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

while (($# > 0)); do
  case "$1" in
    --arch)
      (($# >= 2)) || fail "--arch requires a value"
      REQUESTED_ARCH="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

case "$REQUESTED_ARCH" in
  native)
    case "$(uname -m)" in
      x86_64)
        GO_ARCH="amd64"
        DEB_ARCH="amd64"
        ARTIFACT_ARCH="amd64"
        ;;
      aarch64|arm64)
        GO_ARCH="arm64"
        DEB_ARCH="arm64"
        ARTIFACT_ARCH="arm64"
        ;;
      *) fail "unsupported native architecture: $(uname -m)" ;;
    esac
    ;;
  amd64|x86_64)
    GO_ARCH="amd64"
    DEB_ARCH="amd64"
    ARTIFACT_ARCH="amd64"
    ;;
  arm64|aarch64)
    GO_ARCH="arm64"
    DEB_ARCH="arm64"
    ARTIFACT_ARCH="arm64"
    ;;
  *) fail "unsupported architecture: $REQUESTED_ARCH" ;;
esac

need_command go
need_command node
need_command npm
need_command dpkg-deb
need_command shasum

cd "$ROOT_DIR"

# Extract version from package.json or tauri config
if [[ -f "./client/clientdashboard/src-tauri/tauri.conf.json" ]]; then
  CLIENT_VERSION="$(node -p "require('./client/clientdashboard/src-tauri/tauri.conf.json').version")"
  SERVER_VERSION="$(node -p "require('./server/serverdashboard/src-tauri/tauri.conf.json').version")"
elif [[ -f "./client/package.json" ]]; then
  CLIENT_VERSION="$(node -p "require('./client/package.json').version || '1.0.0'")"
  SERVER_VERSION="$(node -p "require('./server/package.json').version || '1.0.0'")"
else
  CLIENT_VERSION="1.0.0"
  SERVER_VERSION="1.0.0"
fi

[[ -n "$CLIENT_VERSION" ]] || fail "client version is empty"
[[ "$CLIENT_VERSION" == "$SERVER_VERSION" ]] || fail "client and server versions differ"
VERSION="$CLIENT_VERSION"

OUTPUT_DIR="$ROOT_DIR/dist/deb-$ARTIFACT_ARCH"
case "$OUTPUT_DIR" in
  "$ROOT_DIR"/dist/deb-amd64|"$ROOT_DIR"/dist/deb-arm64) ;;
  *) fail "refusing unsafe output directory: $OUTPUT_DIR" ;;
esac

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

build_component() {
  local component="$1"
  local display_name="$2"
  local package_name="tunnelx-$component"
  local deb_name="TunnelX-$display_name-${VERSION}-linux-${ARTIFACT_ARCH}.deb"

  printf '\n[%s] Building Go binary for linux/%s...\n' "$display_name" "$GO_ARCH"

  local binary_name="tunnelx-$component"
  CGO_ENABLED=0 GOOS=linux GOARCH="$GO_ARCH" \
    go build -trimpath -ldflags="-s -w" -o "$OUTPUT_DIR/$binary_name" "./$component"

  # Create deb package structure
  local pkg_dir="$OUTPUT_DIR/${package_name}_${VERSION}_${DEB_ARCH}"
  rm -rf "$pkg_dir"
  mkdir -p "$pkg_dir/DEBIAN"
  mkdir -p "$pkg_dir/opt/tunnelx/bin"
  mkdir -p "$pkg_dir/opt/tunnelx/config"
  mkdir -p "$pkg_dir/opt/tunnelx/logs"
  mkdir -p "$pkg_dir/etc/systemd/system"

  # Copy binary
  cp "$OUTPUT_DIR/$binary_name" "$pkg_dir/opt/tunnelx/bin/"
  chmod 755 "$pkg_dir/opt/tunnelx/bin/$binary_name"

  # Copy default config file
  if [[ -f "$ROOT_DIR/$component/${component}.yaml" ]]; then
    cp "$ROOT_DIR/$component/${component}.yaml" "$pkg_dir/opt/tunnelx/config/${component}.yaml.default"
  fi

  # Create control file
  cat > "$pkg_dir/DEBIAN/control" <<CTRL
Package: $package_name
Version: $VERSION
Section: net
Priority: optional
Architecture: $DEB_ARCH
Maintainer: TunnelX Team <support@tunnelx.io>
Description: TunnelX $display_name
 TunnelX $display_name component for secure tunneling.
Depends: systemd
CTRL

  # Create postinst script
  cat > "$pkg_dir/DEBIAN/postinst" <<'POSTINST'
#!/bin/bash
set -e

INSTALL_DIR="/opt/tunnelx"
CONFIG_DIR="$INSTALL_DIR/config"
LOGS_DIR="$INSTALL_DIR/logs"
PASSWORD_FILE="$CONFIG_DIR/.password"
COMPONENT="COMPONENT"
CONFIG_FILE="$CONFIG_DIR/${COMPONENT}.yaml"
DEFAULT_CONFIG="$CONFIG_DIR/${COMPONENT}.yaml.default"

# Create tunnelx user if not exists
if ! id -u tunnelx >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /bin/false tunnelx || true
fi

# Set directory ownership and permissions
chown -R tunnelx:tunnelx "$INSTALL_DIR"
chmod 755 "$INSTALL_DIR"
chmod 755 "$INSTALL_DIR/bin"
chmod 755 "$INSTALL_DIR/bin"/*
chmod 750 "$CONFIG_DIR"
chmod 770 "$LOGS_DIR"

# Create config file from default if not exists
if [[ ! -f "$CONFIG_FILE" ]] && [[ -f "$DEFAULT_CONFIG" ]]; then
    cp "$DEFAULT_CONFIG" "$CONFIG_FILE"
    chown tunnelx:tunnelx "$CONFIG_FILE"
    chmod 640 "$CONFIG_FILE"
    echo "Created default config: $CONFIG_FILE"
fi

# Prompt for password on first installation
if [[ "$1" == "configure" ]] && [[ ! -f "$PASSWORD_FILE" ]]; then
    echo "==============================================="
    echo " TunnelX First-Time Setup"
    echo "==============================================="
    echo ""

    # Try to set password interactively
    if [[ -t 0 ]]; then
        while true; do
            read -s -p "Set admin password: " password1
            echo ""
            read -s -p "Confirm password: " password2
            echo ""

            if [[ -z "$password1" ]]; then
                echo "Password cannot be empty. Please try again."
                continue
            fi

            if [[ "$password1" == "$password2" ]]; then
                echo -n "$password1" | sha256sum | awk '{print $1}' > "$PASSWORD_FILE"
                chmod 600 "$PASSWORD_FILE"
                chown tunnelx:tunnelx "$PASSWORD_FILE"
                echo "Password set successfully!"
                break
            else
                echo "Passwords do not match. Please try again."
            fi
        done
    else
        # Non-interactive installation: generate random password
        RANDOM_PASSWORD="$(openssl rand -base64 32 | tr -d '/+=' | head -c 24)"
        echo -n "$RANDOM_PASSWORD" | sha256sum | awk '{print $1}' > "$PASSWORD_FILE"
        chmod 600 "$PASSWORD_FILE"
        chown tunnelx:tunnelx "$PASSWORD_FILE"
        echo "Generated random admin password: $RANDOM_PASSWORD"
        echo "Please save this password securely!"
        echo "Password hash stored in: $PASSWORD_FILE"
    fi

    echo ""
    echo "Setup complete!"
    echo "==============================================="
fi

# Reload systemd if service file exists
if [[ -f "/etc/systemd/system/tunnelx-COMPONENT.service" ]]; then
    systemctl daemon-reload || true
    echo "To enable and start the service, run:"
    echo "  sudo systemctl enable tunnelx-COMPONENT"
    echo "  sudo systemctl start tunnelx-COMPONENT"
fi

exit 0
POSTINST

  # Replace COMPONENT placeholder
  sed -i "s/COMPONENT/$component/g" "$pkg_dir/DEBIAN/postinst"
  chmod 755 "$pkg_dir/DEBIAN/postinst"

  # Create prerm script
  cat > "$pkg_dir/DEBIAN/prerm" <<'PRERM'
#!/bin/bash
set -e

if [[ "$1" == "remove" ]]; then
    if systemctl is-active --quiet tunnelx-COMPONENT 2>/dev/null; then
        systemctl stop tunnelx-COMPONENT || true
    fi
    if systemctl is-enabled --quiet tunnelx-COMPONENT 2>/dev/null; then
        systemctl disable tunnelx-COMPONENT || true
    fi
fi

exit 0
PRERM

  sed -i "s/COMPONENT/$component/g" "$pkg_dir/DEBIAN/prerm"
  chmod 755 "$pkg_dir/DEBIAN/prerm"

  # Create postrm script
  cat > "$pkg_dir/DEBIAN/postrm" <<'POSTRM'
#!/bin/bash
set -e

if [[ "$1" == "purge" ]]; then
    rm -rf /opt/tunnelx
    if id -u tunnelx >/dev/null 2>&1; then
        userdel tunnelx || true
    fi
    systemctl daemon-reload || true
fi

exit 0
POSTRM

  chmod 755 "$pkg_dir/DEBIAN/postrm"

  # Create systemd service file
  cat > "$pkg_dir/etc/systemd/system/tunnelx-$component.service" <<SERVICE
[Unit]
Description=TunnelX $display_name Service
After=network.target

[Service]
Type=simple
User=tunnelx
Group=tunnelx
WorkingDirectory=/opt/tunnelx/config
ExecStart=/opt/tunnelx/bin/$binary_name
Restart=on-failure
RestartSec=5s
StandardOutput=append:/opt/tunnelx/logs/$component.log
StandardError=append:/opt/tunnelx/logs/$component.err.log

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/tunnelx/config /opt/tunnelx/logs

[Install]
WantedBy=multi-user.target
SERVICE

  # Build deb package
  printf '[%s] Building .deb package...\n' "$display_name"
  dpkg-deb --build "$pkg_dir" "$OUTPUT_DIR/$deb_name"

  # Cleanup temp directory
  rm -rf "$pkg_dir"
  rm -f "$OUTPUT_DIR/$binary_name"

  printf '[%s] Package created: %s\n' "$display_name" "$deb_name"
}

printf 'Building TunnelX %s for Linux %s...\n' "$VERSION" "$ARTIFACT_ARCH"
build_component client Client
build_component server Server

DEB_COUNT="$(find "$OUTPUT_DIR" -maxdepth 1 -type f -name '*.deb' | wc -l | tr -d '[:space:]')"
[[ "$DEB_COUNT" == "2" ]] || fail "expected two deb packages, found $DEB_COUNT"

(
  cd "$OUTPUT_DIR"
  shasum -a 256 ./*.deb > SHA256SUMS
)

printf '\nBuild complete. Two Debian packages:\n'
find "$OUTPUT_DIR" -maxdepth 1 -type f -name '*.deb' -print | sort
printf 'Checksums: %s/SHA256SUMS\n' "$OUTPUT_DIR"
printf '\nInstallation:\n'
printf '  sudo dpkg -i TunnelX-Client-%s-linux-%s.deb\n' "$VERSION" "$ARTIFACT_ARCH"
printf '  sudo dpkg -i TunnelX-Server-%s-linux-%s.deb\n' "$VERSION" "$ARTIFACT_ARCH"
