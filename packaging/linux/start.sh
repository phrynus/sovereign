#!/usr/bin/env bash
# One-click start for the Linux/macOS Sovereign bundle.
#
# The web console is embedded in the binary: no Node.js, no Go toolchain and no
# external database are required. Keep this file next to the sovereign-server
# binary, config.example.yaml and QUICKSTART.txt.
set -euo pipefail
cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")"

if [ ! -f ./sovereign-server ]; then
  echo "[x] sovereign-server is missing next to this script." >&2
  echo "    Unpack the whole archive and run ./start.sh from inside it." >&2
  exit 1
fi
[ -x ./sovereign-server ] || chmod +x ./sovereign-server 2>/dev/null || true

if [ ! -f config.yaml ]; then
  if [ ! -f config.example.yaml ]; then
    echo "[x] config.yaml is missing and config.example.yaml is not available." >&2
    echo "    Re-download the archive; it must contain config.example.yaml." >&2
    exit 1
  fi
  cp config.example.yaml config.yaml
  chmod 600 config.yaml 2>/dev/null || true
  echo "[*] First run: created config.yaml from config.example.yaml"
fi

cat <<'BANNER'

  Sovereign is starting.
  Console : https://127.0.0.1:8000/   (self-signed certificate: accept once)
  Login   : the first start prints the generated "admin" password below.
  Stop    : press Ctrl+C in this terminal.

BANNER

exec ./sovereign-server -config config.yaml
