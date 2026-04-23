#!/usr/bin/env bash
set -euo pipefail

# DevLog installer — builds the binary, puts it on PATH, and configures
# Claude Code hooks.
#
# Usage:
#   ./install.sh                    # install to ~/.local/bin
#   ./install.sh --prefix=/usr/local # install to /usr/local/bin
#   ./install.sh --uninstall        # remove binary and hooks

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PREFIX="${HOME}/.local"
UNINSTALL=false

for arg in "$@"; do
  case "$arg" in
    --prefix=*) PREFIX="${arg#--prefix=}" ;;
    --uninstall) UNINSTALL=true ;;
    --help|-h)
      cat <<'USAGE'
DevLog Installer

Usage:
    ./install.sh [OPTIONS]

Options:
    --prefix=DIR   Install binary to DIR/bin (default: ~/.local)
    --uninstall    Remove devlog binary and Claude Code hooks
    -h, --help     Show this help

Steps performed:
    1. Build the devlog Go binary
    2. Copy it to PREFIX/bin/devlog
    3. Verify it's on PATH
    4. Run `devlog install` to write hooks into Claude Code settings.json

Prerequisites:
    - Go 1.21+ (https://go.dev/dl/)
    - Claude Code CLI installed
USAGE
      exit 0
      ;;
    *)
      echo "devlog-install: unknown option: $arg" >&2
      echo "Run './install.sh --help' for usage." >&2
      exit 2
      ;;
  esac
done

BIN_DIR="${PREFIX}/bin"

# --- Uninstall path ---

if $UNINSTALL; then
  echo "==> Removing Claude Code hooks..."
  if command -v devlog &>/dev/null; then
    devlog uninstall || true
  elif [ -f "${BIN_DIR}/devlog" ]; then
    "${BIN_DIR}/devlog" uninstall || true
  else
    echo "    (devlog binary not found, skipping hook removal)"
  fi

  if [ -f "${BIN_DIR}/devlog" ]; then
    rm -f "${BIN_DIR}/devlog"
    echo "==> Removed ${BIN_DIR}/devlog"
  else
    echo "    (binary not found at ${BIN_DIR}/devlog)"
  fi

  echo "Done. Run 'rm -rf .devlog' in any project directory to remove local state."
  exit 0
fi

# --- Install path ---

# 1. Check prerequisites
if ! command -v go &>/dev/null; then
  echo "error: Go is not installed or not on PATH." >&2
  echo "Install Go 1.21+ from https://go.dev/dl/" >&2
  exit 1
fi

GO_VERSION=$(go version | grep -oE 'go[0-9]+\.[0-9]+' | head -1)
echo "==> Found ${GO_VERSION}"

# 2. Build
echo "==> Building devlog..."
cd "$SCRIPT_DIR"
go build -o devlog .
echo "    Built successfully"

# 3. Install binary
mkdir -p "$BIN_DIR"
mv devlog "${BIN_DIR}/devlog"
chmod +x "${BIN_DIR}/devlog"
echo "==> Installed to ${BIN_DIR}/devlog"

# 4. Verify PATH
if ! command -v devlog &>/dev/null; then
  echo ""
  echo "WARNING: ${BIN_DIR} is not on your PATH."
  echo "Add it to your shell profile:"
  echo ""
  echo "    echo 'export PATH=\"${BIN_DIR}:\$PATH\"' >> ~/.zshrc"
  echo "    source ~/.zshrc"
  echo ""
  echo "Then re-run: devlog install"
  exit 0
fi

# 5. Install hooks
echo "==> Installing Claude Code hooks..."
devlog install
echo ""
echo "Done. To start using DevLog in a project:"
echo ""
echo "    cd your-project"
echo "    devlog init"
echo ""
