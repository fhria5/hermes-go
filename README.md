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

Perintah Telegram: `/start /help /status /tasks /cancel <id> /memory <query> /clear /sched ...`; pesan biasa memulai tugas.

## Approval interaktif

Tool berisiko (`write_file`, `shell`, `http_request`) memicu pesan Telegram dengan tombol **✅ Setujui / ❌ Tolak**:

- Permintaan persisten di tabel `approvals` (ID unik, sesi pemohon, hash argumen, kedaluwarsa 5 menit).
- Callback divalidasi: hanya user pada sesi pemohon, status `pending`, belum kedaluwarsa.
- Keputusan atomik (`UPDATE ... WHERE status='pending'`) — double-click / race: hanya satu yang menang.
- Fail-closed: kedaluwarsa, restart (pending → `expired` saat startup), error → **tolak**; tidak ada eksekusi ulang otomatis.
- Argumen ditampilkan setelah sanitasi secret (`tools.PreviewArgs`).

## Scheduler

- `/sched add <nama> interval <detik> <prompt>` (60 s..30 hari) atau `once <RFC3339> <prompt>`.
- Persisten di SQLite (`schedules`, `schedule_runs`); anti-eksekusi-ganda via CAS `WHERE next_run_at=old`.
- Kebijakan missed-run eksplisit: interval → 1 eksekusi + `next = now + interval` (tidak mengejar backlog); `once` terlewat > 24 jam → dinonaktifkan, bukan dijalankan.
- Job berjalan dengan security policy yang sama; job yang butuh approval berhenti menunggu keputusan.

## /clear dan memory

- `/clear` menghapus riwayat pesan **hanya sesi aktif** (transaksional, dengan konfirmasi tombol); memory jangka panjang tidak terhapus.
- Retensi: `MEMORY_RETENTION_DAYS` dikonfigurasi di `.env` (pembersihan otomatis berkala menyusul; backup terlebih dulu via `deploy/scripts/backup.sh`).

## Recovery

Tugas berstatus `running`/`waiting_approval` saat restart **tidak diulang otomatis** — ditandai `failed` dengan peringatan log, karena efek sampingnya tidak diketahui. Riwayat percakapan dan memory bertahan (SQLite WAL).

## Keterbatasan (jujur, belum diimplementasi)

1. **E2E Telegram dengan bot sungguhan belum dijalankan** — butuh token asli; semua logika lokal teruji via unit/integration test (mock provider + SQLite).
2. Memory search = LIKE SQL, **bukan semantic search**.
3. Retensi memory (`MEMORY_RETENTION_DAYS`) berupa config, belum dieksekusi otomatis oleh pekerja berkala.
4. Zona waktu jadwal disimpan per-jadwal (default `Asia/Jakarta`) tapi perhitungan interval memakai UTC; cron ekspresi (mis. "tiap Senin 09:00") belum didukung — hanya `once` RFC3339 dan `interval`.
5. Pengukuran RAM/CPU pada target 4 GB **belum dilakukan** (dibangun & diuji di VM x86_64 2 vCPU; RSS idle terukur ~18 MB di VM pengembangan — bukan angka VM42).
6. Approval binding: `args_hash` disimpan tapi belum diverifikasi ulang saat eksekusi (eksekusi tetap memakai argumen asli dari loop, sehingga risiko low; verifikasi eksplisit menyusul).

## Verifikasi

```bash
gofmt -l . && go vet ./... && go test ./... && go test -race ./... && go build ./...
```

Status saat commit ini: semua lulus di lingkungan pengembangan (Go 1.23, linux/amd64).
