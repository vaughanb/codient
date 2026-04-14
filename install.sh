#!/bin/sh
set -e

REPO="vaughanb/codient"
INSTALL_DIR="${CODIENT_INSTALL_DIR:-$HOME/.local/bin}"

fail() { printf "\033[31merror:\033[0m %s\n" "$1" >&2; exit 1; }
info() { printf "\033[36m%s\033[0m\n" "$1"; }

detect_os() {
    case "$(uname -s)" in
        Linux*)  echo "linux" ;;
        Darwin*) echo "darwin" ;;
        *)       fail "unsupported OS: $(uname -s)" ;;
    esac
}

detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)  echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *)             fail "unsupported architecture: $(uname -m)" ;;
    esac
}

OS="$(detect_os)"
ARCH="$(detect_arch)"

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO- "$1"; }
else
    fail "curl or wget is required"
fi

info "Detecting latest release..."
TAG=$(fetch "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"//;s/".*//')
[ -z "$TAG" ] && fail "could not determine latest release"
VERSION="${TAG#v}"
info "Latest version: ${VERSION}"

ARCHIVE="codient_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ARCHIVE}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

info "Downloading ${ARCHIVE}..."
fetch "$URL" > "${TMPDIR}/${ARCHIVE}"

info "Extracting..."
tar -xzf "${TMPDIR}/${ARCHIVE}" -C "$TMPDIR"

mkdir -p "$INSTALL_DIR"
mv "${TMPDIR}/codient" "${INSTALL_DIR}/codient"
chmod +x "${INSTALL_DIR}/codient"

info "Installed codient ${VERSION} to ${INSTALL_DIR}/codient"

case ":$PATH:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
        printf "\n\033[33mNote:\033[0m %s is not in your PATH.\n" "$INSTALL_DIR"
        echo "Add it by appending this to your shell profile (~/.bashrc, ~/.zshrc, etc.):"
        echo ""
        echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
        echo ""
        ;;
esac
