#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REQUESTED_ARCH="native"

usage() {
  cat <<'EOF'
Build the TunnelX Client and Server macOS application bundles.

Usage:
  ./build-macos.sh [--arch native|arm64|x86_64]

Outputs (four artifacts):
  TunnelX-Client-<version>-macOS-<arch>.app
  TunnelX-Client-<version>-macOS-<arch>.dmg
  TunnelX-Server-<version>-macOS-<arch>.app
  TunnelX-Server-<version>-macOS-<arch>.dmg

Examples:
  ./build-macos.sh
  ./build-macos.sh --arch arm64
  ./build-macos.sh --arch x86_64
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

[[ "$(uname -s)" == "Darwin" ]] || fail "this script must run on macOS"

case "$REQUESTED_ARCH" in
  native)
    case "$(uname -m)" in
      arm64)
        GO_ARCH="arm64"
        RUST_TARGET="aarch64-apple-darwin"
        ARTIFACT_ARCH="arm64"
        ;;
      x86_64)
        GO_ARCH="amd64"
        RUST_TARGET="x86_64-apple-darwin"
        ARTIFACT_ARCH="x86_64"
        ;;
      *) fail "unsupported native architecture: $(uname -m)" ;;
    esac
    ;;
  arm64|aarch64)
    GO_ARCH="arm64"
    RUST_TARGET="aarch64-apple-darwin"
    ARTIFACT_ARCH="arm64"
    ;;
  x86_64|amd64)
    GO_ARCH="amd64"
    RUST_TARGET="x86_64-apple-darwin"
    ARTIFACT_ARCH="x86_64"
    ;;
  *) fail "unsupported architecture: $REQUESTED_ARCH" ;;
esac

need_command go
need_command node
need_command npm
need_command cargo
need_command rustc
need_command rustup
need_command xcrun
need_command hdiutil
need_command ditto
need_command shasum

xcrun --find clang >/dev/null 2>&1 || fail "Xcode Command Line Tools are not installed"

if ! rustup target list --installed | grep -Fxq "$RUST_TARGET"; then
  printf 'Installing Rust target %s...\n' "$RUST_TARGET"
  rustup target add "$RUST_TARGET"
fi

cd "$ROOT_DIR"

CLIENT_VERSION="$(node -p "require('./client/clientdashboard/src-tauri/tauri.conf.json').version")"
SERVER_VERSION="$(node -p "require('./server/serverdashboard/src-tauri/tauri.conf.json').version")"
[[ -n "$CLIENT_VERSION" ]] || fail "client version is empty"
[[ "$CLIENT_VERSION" == "$SERVER_VERSION" ]] || fail "client and server versions differ"
VERSION="$CLIENT_VERSION"

OUTPUT_DIR="$ROOT_DIR/dist/macos-$ARTIFACT_ARCH"
case "$OUTPUT_DIR" in
  "$ROOT_DIR"/dist/macos-arm64|"$ROOT_DIR"/dist/macos-x86_64) ;;
  *) fail "refusing unsafe output directory: $OUTPUT_DIR" ;;
esac

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

build_component() {
  local component="$1"
  local display_name="$2"
  local product_name="$3"
  local dashboard_dir="$ROOT_DIR/$component/${component}dashboard"
  local tauri_dir="$dashboard_dir/src-tauri"
  local sidecar="$tauri_dir/binaries/tunnelx-$component-$RUST_TARGET"
  local bundle_dir="$tauri_dir/target/$RUST_TARGET/release/bundle"
  local app_source="$bundle_dir/macos/$product_name.app"
  local app_destination="$OUTPUT_DIR/TunnelX-$display_name-$VERSION-macOS-$ARTIFACT_ARCH.app"
  local dmg_destination="$OUTPUT_DIR/TunnelX-$display_name-$VERSION-macOS-$ARTIFACT_ARCH.dmg"
  local dmg_source=""

  printf '\n[%s] Installing frontend dependencies...\n' "$display_name"
  (
    cd "$dashboard_dir"
    npm ci
  )

  printf '[%s] Building Go sidecar for darwin/%s...\n' "$display_name" "$GO_ARCH"
  mkdir -p "$(dirname "$sidecar")"
  CGO_ENABLED=0 GOOS=darwin GOARCH="$GO_ARCH" \
    go build -trimpath -ldflags="-s -w" -o "$sidecar" "./$component"

  # Remove only this target's old Tauri bundle outputs so a stale DMG cannot
  # be mistaken for the artifact produced by the current build.
  rm -rf "$bundle_dir/macos" "$bundle_dir/dmg"

  printf '[%s] Building macOS .app and .dmg...\n' "$display_name"
  (
    cd "$dashboard_dir"
    npm run tauri build -- --target "$RUST_TARGET" --bundles app,dmg
  )

  [[ -d "$app_source" ]] || fail "$display_name app bundle not found: $app_source"
  for candidate in "$bundle_dir"/dmg/*.dmg; do
    if [[ -f "$candidate" ]]; then
      [[ -z "$dmg_source" ]] || fail "multiple DMG files found for $display_name"
      dmg_source="$candidate"
    fi
  done
  [[ -n "$dmg_source" ]] || fail "$display_name DMG was not generated"

  ditto "$app_source" "$app_destination"
  cp "$dmg_source" "$dmg_destination"
}

printf 'Building TunnelX %s for macOS %s...\n' "$VERSION" "$ARTIFACT_ARCH"
build_component client Client TunnelX
build_component server Server "TunnelX Server"

APP_COUNT="$(find "$OUTPUT_DIR" -maxdepth 1 -type d -name '*.app' | wc -l | tr -d '[:space:]')"
DMG_COUNT="$(find "$OUTPUT_DIR" -maxdepth 1 -type f -name '*.dmg' | wc -l | tr -d '[:space:]')"
[[ "$APP_COUNT" == "2" && "$DMG_COUNT" == "2" ]] || \
  fail "expected two apps and two DMGs, found $APP_COUNT apps and $DMG_COUNT DMGs"

(
  cd "$OUTPUT_DIR"
  shasum -a 256 ./*.dmg > SHA256SUMS
)

printf '\nBuild complete. Four macOS artifacts:\n'
find "$OUTPUT_DIR" -maxdepth 1 \( -type f -name '*.dmg' -o -type d -name '*.app' \) -print | sort
printf 'Checksums: %s/SHA256SUMS\n' "$OUTPUT_DIR"
