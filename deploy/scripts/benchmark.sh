#!/usr/bin/env bash
# benchmark.sh — pengukuran resource Hermes-Go di mesin target (VM42).
# Jalankan DI MESIN TARGET. Tidak mengekspos pprof; pengukuran via ps & /proc.
#
# Pemakaian:
#   sudo bash deploy/scripts/benchmark.sh [PATH_BINARY] [DATA_DIR]
# Default: /opt/hermes-go/hermes-go, /opt/hermes-go/data
#
# Yang diukur (dari /proc/<pid>, bukan pprof):
#   - RSS proses agent (VmRSS)
#   - Total & available RAM sistem (MemAvailable — BUKAN disamakan dengan RSS)
#   - CPU% proses (dari /proc/<pid>/stat, dua sampel)
#   - Jumlah goroutine TIDAK diukur di sini (butuh pprof/debug endpoint);
#     dokumentasikan cara opsional di README bila diperlukan.
# Skenario: idle → 1 tugas → beberapa tugas berurutan → restart → cek DB.
set -euo pipefail

BIN=${1:-/opt/hermes-go/hermes-go}
DATA=${2:-/opt/hermes-go/data}
ENVF=${ENVF:-/opt/hermes-go/.env}
OUT=${OUT:-./benchmark-results.txt}
PID=""

log() { echo "[$(date +%H:%M:%S)] $*" | tee -a "$OUT"; }

rss_kb() { awk '/VmRSS/{print $2}' "/proc/$PID/status" 2>/dev/null || echo 0; }
cpu_ticks() { awk '{print $14+$15}' "/proc/$PID/stat" 2>/dev/null || echo 0; }
mem_system() {
  awk '/MemTotal/{t=$2} /MemAvailable/{a=$2} END{printf "total=%dMB available=%dMB", t/1024, a/1024}' /proc/meminfo
}
sample() {
  local label="$1"
  local rss=$(rss_kb)
  log "$label: RSS=$(awk -v k=$rss 'BEGIN{printf "%.1f", k/1024}')MB | sistem $(mem_system)"
}

start_service() {
  systemctl start hermes-go
  for i in $(seq 1 30); do
    PID=$(systemctl show -p MainPID --value hermes-go)
    [[ "$PID" != "0" ]] && curl -fsS 127.0.0.1:8089/health >/dev/null 2>&1 && return 0
    sleep 1
  done
  echo "service gagal start" >&2; exit 1
}

: > "$OUT"
log "=== Hermes-Go benchmark — $(uname -m) — $(lsb_release -ds 2>/dev/null || cat /etc/debian_version) ==="

log "--- Skenario 1: idle (60 detik) ---"
start_service
sleep 60; sample "idle"

log "--- Skenario 2: satu tugas LLM normal ---"
log "Kirim satu tugas via Telegram ke bot, tunggu selesai, lalu tekan Enter."
read -r
sample "setelah-1-tugas"

log "--- Skenario 3: beberapa tugas berurutan ---"
log "Kirim 5 tugas berurutan via Telegram, tunggu semua selesai, lalu tekan Enter."
read -r
sample "setelah-5-tugas"

log "--- Skenario 4: restart + integritas DB ---"
CPU1=$(cpu_ticks)
systemctl restart hermes-go
start_service
sleep 10; sample "setelah-restart"
CPU2=$(cpu_ticks)
log "CPU ticks selama restart-window: $((CPU2 - CPU1))"
if command -v sqlite3 >/dev/null; then
  log "DB: $(sqlite3 "$DATA/hermes.db" 'SELECT COUNT(*) FROM tasks') tugas, $(sqlite3 "$DATA/hermes.db" 'PRAGMA integrity_check')"
fi

log "=== Selesai. Hasil di $OUT ==="
