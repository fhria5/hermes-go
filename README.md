# Hermes-Go

AI agent modular berbahasa Go, terinspirasi pola umum Hermes Agent (agent loop + tool calling + skill SKILL.md + Telegram). **Bukan** kompatibilitas penuh dengan Hermes Agent — format skill hanya mengikuti subset yang didokumentasikan di bawah.

## Arsitektur

- `cmd/agent` — entrypoint, wiring, command Telegram.
- `internal/llm` — provider OpenAI-compatible Chat Completions + tool calling, retry backoff/jitter, hormati Retry-After, mock untuk test.
- `internal/agent` — agent loop: max iterasi/tool call/token, deteksi pengulangan, cancellation via context, status tugas (planned/running/waiting_approval/succeeded/failed/cancelled).
- `internal/tools` — registry (schema, validasi, izin, timeout, batas output): read_file, write_file, list_directory, search_files, shell, http_request, search_memory, save_memory, task_status, task_cancel.
- `internal/skills` — pemuat SKILL.md (front matter YAML), pemilihan berbasis kata kunci, lazy load, toleransi skill rusak.
- `internal/storage` — SQLite (modernc, tanpa CGO): pesan, tugas, memory, log tool call; migrasi terkontrol.
- `internal/telegram` — polling, allowlist user ID, dedup update, antrean 1 worker, rate limit.
- `internal/config`, `internal/observability` — env config dengan validasi, logging JSON, sanitasi secret.

## Keamanan (aktif secara default)

- Filesystem dikunci ke `WORKSPACE_ROOT`; traversal `..` dan symlink keluar ditolak; tulis atomik.
- `shell` butuh allowlist (`SHELL_ALLOWLIST`); **kosong = dinonaktifkan**. Dieksekusi tanpa shell (tanpa shell injection), dengan timeout.
- `http_request` menolak IP privat/loopback/link-local (dicek per-dial, tahan DNS rebinding) dan redirect ke host di luar allowlist.
- Tool `PermApproval` (write_file, shell, http_request) **ditolak** hingga mekanisme persetujuan diaktifkan (`Approve` masih nil — lihat Keterbatasan).
- Konten tool/file/skill/web dianggap data tidak tepercaya; system prompt melarang instruksi dari konten mengubah policy.
- Token bot & API key hanya via `.env` (0600), tidak pernah masuk Git.

## Format skill yang didukung

```markdown
---
name: nama-skill          # wajib, [a-z0-9-], maks 64
description: deskripsi    # wajib, maks 500 char
version: 1.0.0            # opsional
tags: satu,dua            # opsional
---
Instruksi markdown (maks 20.000 char, dimuat hanya saat dipilih).
```

Pemilihan: skor kata kunci (name 3, tags 2, deskripsi 1), maks 3 skill per tugas. Skill rusak dilaporkan, aplikasi tetap jalan. Skrip dalam skill **tidak pernah** dijalankan otomatis.

## Instalasi (Ubuntu/Debian)

```bash
git clone <repo> && cd hermes-go
go build -o hermes-go ./cmd/agent
sudo bash deploy/scripts/install.sh
sudo nano /opt/hermes-go/.env   # isi LLM_API_KEY, TELEGRAM_BOT_TOKEN, TELEGRAM_ALLOWED_USERS
sudo systemctl enable --now hermes-go
```

Konfigurasi lengkap: lihat `.env.example` (semua variabel dengan default aman).

## Operasi

```bash
systemctl status hermes-go
journalctl -u hermes-go -f          # log structured JSON
sudo bash deploy/scripts/backup.sh  # backup SQLite WAL-safe
# restore: systemctl stop hermes-go; cp backup.db /opt/hermes-go/data/hermes.db;
#          chown hermes:hermes ...; systemctl start hermes-go
curl 127.0.0.1:8089/health          # health check lokal
```

Perintah Telegram: `/start /help /status /tasks /cancel <id> /memory <query>`; pesan biasa memulai tugas.

## Recovery

Tugas berstatus `running`/`waiting_approval` saat restart **tidak diulang otomatis** — ditandai `failed` dengan peringatan log, karena efek sampingnya tidak diketahui. Riwayat percakapan dan memory bertahan (SQLite WAL).

## Keterbatasan (jujur, belum diimplementasi)

1. **Approval interaktif belum aktif** — tool berisiko selalu ditolak di mode default; alur persetujuan via chat (/approve) belum dibangun.
2. **Scheduler/background tasks belum ada** (tahap 8 master plan).
3. Memory search = LIKE SQL, **bukan semantic search**.
4. `/clear` placeholder; retensi memory (`MEMORY_RETENTION_DAYS`) belum dieksekusi otomatis.
5. Orkestrasi multi-langkah masih lewat agent loop tunggal, tanpa planner eksplisit.
6. Pengukuran RAM/CPU pada target 4 GB **belum dilakukan** (dibangun & diuji di VM x86_64 2 vCPU; binary statis CGO-free, diharapkan < 50 MB RSS saat idle — angka aktual harus diukur di VM42).
7. Tes Telegram end-to-end butuh token bot asli — belum dijalankan.

## Verifikasi

```bash
gofmt -l . && go vet ./... && go test ./... && go test -race ./... && go build ./...
```

Status saat commit ini: semua lulus di lingkungan pengembangan (Go 1.23, linux/amd64).
