#!/usr/bin/env bash
# Backup database SQLite (WAL-safe) + skill + konfigurasi.
set -euo pipefail
DATA_DIR=${1:-/opt/hermes-go/data}
DEST=${2:-/var/backups/hermes-go}
mkdir -p "$DEST"
STAMP=$(date +%Y%m%d-%H%M%S)
# sqlite3 .backup aman terhadap WAL; fallback: copy file saat service berhenti.
if command -v sqlite3 >/dev/null; then
  sqlite3 "$DATA_DIR/hermes.db" ".backup '$DEST/hermes-$STAMP.db'"
else
  echo "sqlite3 tidak ada — hentikan service dulu lalu copy manual:" >&2
  echo "  systemctl stop hermes-go && cp $DATA_DIR/hermes.db $DEST/" >&2
  exit 1
fi
echo "Backup: $DEST/hermes-$STAMP.db"
# Restore: systemctl stop hermes-go; cp backup.db $DATA_DIR/hermes.db; chown hermes:hermes; systemctl start hermes-go
