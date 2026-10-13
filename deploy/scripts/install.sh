#!/usr/bin/env bash
# Instalasi Hermes-Go di Ubuntu/Debian. Jalankan sebagai root.
set -euo pipefail

INSTALL_DIR=/opt/hermes-go
BIN_SRC="$(cd "$(dirname "$0")/.." && pwd)/hermes-go"  # hasil go build

if [[ $EUID -ne 0 ]]; then echo "Jalankan sebagai root" >&2; exit 1; fi
if [[ ! -x "$BIN_SRC" ]]; then echo "Binary $BIN_SRC tidak ditemukan. Build dulu: cd repo && go build -o hermes-go ./cmd/agent" >&2; exit 1; fi

# User service non-root
id -u hermes &>/dev/null || useradd --system --home "$INSTALL_DIR" --shell /usr/sbin/nologin hermes

mkdir -p "$INSTALL_DIR"/{data,workspace,skills}
install -m 0755 "$BIN_SRC" "$INSTALL_DIR/hermes-go"
cp -r "$(dirname "$0")/.."/skills/* "$INSTALL_DIR/skills/" 2>/dev/null || true

# .env: buat jika belum ada, jangan pernah ditimpa
if [[ ! -f "$INSTALL_DIR/.env" ]]; then
  install -m 0600 "$(dirname "$0")/.."/.env.example "$INSTALL_DIR/.env"
  echo ">> Isi LLM_API_KEY, TELEGRAM_BOT_TOKEN, dan TELEGRAM_ALLOWED_USERS di $INSTALL_DIR/.env"
fi

chown -R hermes:hermes "$INSTALL_DIR"
chmod 700 "$INSTALL_DIR/data" "$INSTALL_DIR/workspace"

install -m 0644 "$(dirname "$0")/systemd/hermes-go.service" /etc/systemd/system/hermes-go.service
systemctl daemon-reload
echo ">> Selesai. Isi .env lalu: systemctl enable --now hermes-go"
echo ">> Status: systemctl status hermes-go | Log: journalctl -u hermes-go -f"
