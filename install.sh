#!/bin/sh
set -eu

repo="${AETHER_REPO:-Aculnaj/aethercli}"
version="${AETHER_VERSION:-latest}"
install_dir="${AETHER_INSTALL_DIR:-$HOME/.local/bin}"

need() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

need curl
need tar
need uname
need install

case "$(uname -s)" in
  Darwin)
    os="darwin"
    ;;
  Linux)
    os="linux"
    ;;
  *)
    echo "Unsupported operating system: $(uname -s)" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64 | amd64)
    arch="amd64"
    ;;
  arm64 | aarch64)
    arch="arm64"
    ;;
  *)
    echo "Unsupported CPU architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

asset="aether_${os}_${arch}.tar.gz"
if [ "$version" = "latest" ]; then
  url="https://github.com/${repo}/releases/latest/download/${asset}"
else
  url="https://github.com/${repo}/releases/download/${version}/${asset}"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT INT TERM

echo "Downloading ${url}" >&2
curl -fsSL "$url" -o "$tmp_dir/$asset"
tar -xzf "$tmp_dir/$asset" -C "$tmp_dir"

mkdir -p "$install_dir"
install -m 755 "$tmp_dir/aether" "$install_dir/aether"

echo "Installed aether to ${install_dir}/aether" >&2
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    echo "Add ${install_dir} to PATH to run: aether" >&2
    ;;
esac

