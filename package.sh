#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERSION="0.1.0"
OUTPUT_DIR="${ROOT_DIR}/dist"
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
BUILD_WEB=false
BUILD_DESKTOP=false

usage() {
  cat <<'EOF'
Build TunnelX release archives.

Usage:
  ./package.sh [options]

Options:
  --version VERSION     Release version used in artifact names (default: 0.1.0)
  --targets TARGETS     Space-separated GOOS/GOARCH targets
  --output DIR          Output directory (default: ./dist)
  --build-web           Rebuild embedded React dashboards before compiling
  --desktop             Also build native Tauri bundles for the current OS
  -h, --help            Show this help

Examples:
  ./package.sh --version 1.0.0
  ./package.sh --targets "linux/amd64 windows/amd64"
  ./package.sh --build-web --desktop
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
    --version)
      (($# >= 2)) || fail "--version requires a value"
      VERSION="$2"
      shift 2
      ;;
    --targets)
      (($# >= 2)) || fail "--targets requires a value"
      TARGETS="$2"
      shift 2
      ;;
    --output)
      (($# >= 2)) || fail "--output requires a value"
      OUTPUT_DIR="$2"
      shift 2
      ;;
    --build-web)
      BUILD_WEB=true
      shift
      ;;
    --desktop)
      BUILD_DESKTOP=true
      shift
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

[[ "$VERSION" =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || fail "version contains unsupported characters: $VERSION"
[[ -n "${TARGETS// }" ]] || fail "target list cannot be empty"

if [[ "$OUTPUT_DIR" != /* ]]; then
  OUTPUT_DIR="$ROOT_DIR/${OUTPUT_DIR#./}"
fi
case "$OUTPUT_DIR" in
  ""|"/"|"$ROOT_DIR"|"$ROOT_DIR/"|*"/../"*|*"/.."|*"/./"*|*"/.")
    fail "refusing unsafe output directory: $OUTPUT_DIR"
    ;;
esac

need_command go
need_command tar
need_command zip

cd "$ROOT_DIR"

if $BUILD_WEB || $BUILD_DESKTOP; then
  need_command npm
fi

if $BUILD_WEB; then
  printf 'Rebuilding embedded dashboards...\n'
  for dashboard in server/serverdashboard client/clientdashboard; do
    (
      cd "$ROOT_DIR/$dashboard"
      npm ci
      npm run build
    )
  done
fi

rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR/.staging"

package_component() {
  local component="$1"
  local goos="$2"
  local goarch="$3"
  local extension=""
  local archive_extension="tar.gz"
  local release_name="tunnelx-${component}-${VERSION}-${goos}-${goarch}"
  local stage_dir="$OUTPUT_DIR/.staging/$release_name"

  if [[ "$goos" == "windows" ]]; then
    extension=".exe"
    archive_extension="zip"
  fi

  mkdir -p "$stage_dir"
  printf 'Building %-6s for %s/%s...\n' "$component" "$goos" "$goarch"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w" \
    -o "$stage_dir/tunnelx-${component}${extension}" "./${component}"

  cp "$ROOT_DIR/${component}/${component}.yaml" "$stage_dir/${component}.yaml"
  cp "$ROOT_DIR/${component}/README.md" "$stage_dir/README.md"
  cp "$ROOT_DIR/LICENSE" "$stage_dir/LICENSE"

  if [[ "$archive_extension" == "zip" ]]; then
    (
      cd "$OUTPUT_DIR/.staging"
      zip -qr "$OUTPUT_DIR/${release_name}.zip" "$release_name"
    )
  else
    tar -C "$OUTPUT_DIR/.staging" -czf "$OUTPUT_DIR/${release_name}.tar.gz" "$release_name"
  fi
}

for target in $TARGETS; do
  [[ "$target" =~ ^[a-z0-9]+/[a-z0-9]+$ ]] || fail "invalid target '$target'; expected GOOS/GOARCH"
  goos="${target%/*}"
  goarch="${target#*/}"
  package_component server "$goos" "$goarch"
  package_component client "$goos" "$goarch"
done

build_desktop_bundle() {
  local component="$1"
  local rust_target="$2"
  local host_os="$3"
  local host_arch="$4"
  local dashboard_dir="$ROOT_DIR/${component}/${component}dashboard"
  local sidecar="$dashboard_dir/src-tauri/binaries/tunnelx-${component}-${rust_target}"
  local bundle_dir="$dashboard_dir/src-tauri/target/${rust_target}/release/bundle"
  local fallback_bundle_dir="$dashboard_dir/src-tauri/target/release/bundle"
  local destination="$OUTPUT_DIR/desktop/${component}-${host_os}-${host_arch}"

  if [[ "$host_os" == "windows" ]]; then
    sidecar="${sidecar}.exe"
  fi

  mkdir -p "$(dirname "$sidecar")" "$destination"
  printf 'Building native %s desktop bundle for %s/%s...\n' "$component" "$host_os" "$host_arch"
  CGO_ENABLED=0 GOOS="$host_os" GOARCH="$host_arch" \
    go build -trimpath -ldflags="-s -w" -o "$sidecar" "./${component}"

  (
    cd "$dashboard_dir"
    npm ci
    npm run tauri build -- --target "$rust_target"
  )

  if [[ ! -d "$bundle_dir" && -d "$fallback_bundle_dir" ]]; then
    bundle_dir="$fallback_bundle_dir"
  fi
  [[ -d "$bundle_dir" ]] || fail "Tauri bundle directory not found for $component"
  cp -R "$bundle_dir"/. "$destination/"
}

if $BUILD_DESKTOP; then
  need_command rustc
  host_os="$(go env GOHOSTOS)"
  host_arch="$(go env GOHOSTARCH)"
  case "${host_os}/${host_arch}" in
    darwin/amd64) rust_target="x86_64-apple-darwin" ;;
    darwin/arm64) rust_target="aarch64-apple-darwin" ;;
    linux/amd64) rust_target="x86_64-unknown-linux-gnu" ;;
    linux/arm64) rust_target="aarch64-unknown-linux-gnu" ;;
    windows/amd64) rust_target="x86_64-pc-windows-msvc" ;;
    windows/arm64) rust_target="aarch64-pc-windows-msvc" ;;
    *) fail "unsupported native desktop host: ${host_os}/${host_arch}" ;;
  esac
  build_desktop_bundle server "$rust_target" "$host_os" "$host_arch"
  build_desktop_bundle client "$rust_target" "$host_os" "$host_arch"
fi

rm -rf "$OUTPUT_DIR/.staging"

(
  cd "$OUTPUT_DIR"
  archives=()
  while IFS= read -r file; do
    archives+=("${file#./}")
  done < <(find . -maxdepth 1 -type f \( -name '*.tar.gz' -o -name '*.zip' \) | sort)

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${archives[@]}" > SHA256SUMS
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "${archives[@]}" > SHA256SUMS
  elif command -v openssl >/dev/null 2>&1; then
    : > SHA256SUMS
    for archive in "${archives[@]}"; do
      printf '%s  %s\n' "$(openssl dgst -sha256 -r "$archive" | awk '{print $1}')" "$archive" >> SHA256SUMS
    done
  else
    fail "sha256sum, shasum, or openssl is required to generate checksums"
  fi
)

printf '\nRelease artifacts written to %s\n' "$OUTPUT_DIR"
find "$OUTPUT_DIR" -maxdepth 1 -type f -print | sort
