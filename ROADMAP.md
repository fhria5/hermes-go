# ROADMAP Hermes-Go — hasil reverse engineering Hermes Agent

Sumber: docs resmi hermes-agent.nousresearch.com (Architecture, Skills, Tools), repo
NousResearch/hermes-agent (MIT, Python ~88%). Dipetakan ke kondisi Hermes-Go saat ini
(v0.2.2 + Tahap A/B/C: 13 tools, /sysinfo, web search/fetch, memory inject, retention).

## Arsitektur Hermes Agent (ringkasan reverse engineering)

### 1. Satu core, banyak pintu masuk
`AIAgent` (agent loop) SATU class dipakai CLI, Gateway (20+ platform), ACP, batch, API
server. Perbedaan platform hanya di entry point, bukan di agent.
**Hermes-Go sekarang:** agent loop memang terpisah dari gateway Telegram (sudah benar
arahnya), tapi entry point-nya satu (Telegram). Belum ada CLI interaktif.

### 2. Prompt bertingkat: stable → context → volatile
System prompt disusun 3 tier agar cache LLM stabil (prefix caching): identitas/skills
(stabil), context files (jarang berubah), memory/timestamp (volatil).
**Hermes-Go:** system prompt disusun sekali + memory diinject (Tahap B2) — belum ada
pemisahan tier dan belum ada prompt caching. DAMPAK BIAYA: vyceai/OpenAI-compatible
kadang mendukung caching; susun prompt agar bagian stabil di depan.

### 3. Context compression lossy
Saat konteks melebihi threshold, giliran-tengah diringkas LLM aux; sesi punya lineage
(parent/child antar kompresi).
**Hermes-Go:** riwayat dipotong mentah (40 pesan terakhir) — belum ada peringkasan.
Ini penyebab "lupa" pada tugas panjang.

### 4. Learning loop (skill otomatis)
Setelah tugas kompleks, agent EKSTRAK pendekatan → simpan sebagai skill
(skill_manage: create/patch/write_file/remove_file). Skill memperbaiki dirinya saat
dipakai. Progressive disclosure 3 level (list ~3k token → view → file referensi).
**Hermes-Go:** skill hanya dibaca pasif dari direktori skills/ — TIDAK ada tool untuk
agent menulis/membuat skill sendiri. Ini jantung "self-improving" yang belum ada.

### 5. Memory terkurasi + FTS5
Memory dikurasi agent sendiri (bukan log mentah), ada "nudge" berkala, recall lintas
sesi via SQLite FTS5 + peringkasan LLM. Plus user modeling terpisah (Honcho).
**Hermes-Go:** ada SaveMemory/SearchMemory + retention worker. Belum ada FTS5
(pencarian LIKE saja), belum ada nudge, belum ada user model.

### 6. Tool system: self-registration + permission
Registry pusat; tiap tool file self-register; toolset preset per platform; large
output 3 lapis (truncation in-tool → persist ke file → budget per giliran).
**Hermes-Go:** registry manual (register di main.go) — sudah cukup untuk skala ini.
Tool output besar sudah dibatasi MaxOutputBytes. Belum ada toolset preset.

### 7. Gateway: sesi per-platform + slash command + hook
Gateway proses terpisah: authorize (allowlist + DM pairing) → session key → buat
agent dengan riwayat → balas. Cron = tugas agent (bukan shell), deliver ke platform
mana pun.
**Hermes-Go:** sudah punya allowlist, session per user, scheduler + delivery ke
Telegram. Cron deliver lintas platform belum relevan (platform-nya satu).

### 8. Multi-agent ringan
Subagent terisolasi untuk paralelisasi + kanban board antar worker. (Rekan: ini
TELAH dicapai Hermes setelah bertahun-tahun.)

## GAP Hermes-Go vs Hermes Agent (prioritas dari dampak/token/biaya)

| # | Gap | Usaha | Nilai |
|---|-----|-------|-------|
| 1 | Self-authored skills (skill_manage tool + skill selection otomatis) | Sedang | Tinggi — inti "agent tumbuh" |
| 2 | Context compression (ringkas riwayat tengah saat >threshold) | Sedang | Tinggi — tugas panjang tak lupa |
| 3 | FTS5 pencarian memory/sesi | Kecil | Sedang |
| 4 | Prompt tiering + prefix caching | Kecil | Sedang — hemat biaya API |
| 5 | CLI interaktif (entry point ke-2 dari core yang sama) | Sedang | Sedang |
| 6 | Streaming jawaban LLM ke Telegram (edit pesan progresif) | Sedang | Tinggi (UX) |
| 7 | Subagent/orkestrasi | Besar | Rendah (saat ini) |
| 8 | 20+ platform gateway | Besar | Rendah (platform satu) |

## RENCANA (D1–D6, tiap batch kecil + test + commit + release)

- **D1 — FTS5 + prompt tiering** (gap 3+4, yang paling murah):
  migration FTS5 virtual table untuk memories+messages; SearchMemory pakai MATCH;
  susun system prompt stable-first (system prompt + tool defs di depan, memory
  volatil di belakang).
- **D2 — Context compression** (gap 2): saat riwayat > N pesan (mis. 40), giliran
  tengah diringkas LLM aux jadi ringkasan berjalan ("rolling summary"); simpan di
  sesi. Test: riwayat 100 pesan → prompt tetap < limit.
- **D3 — Streaming jawaban** (gap 6): kirim pesan awal, edit bertahap saat token
  streaming masuk (editMessageText, throttle ~1x/2 detik). Butuh provider streaming
  (SSE) di internal/llm.
- **D4 — skill_manage tool** (gap 1): tool WriteSkill (create/patch/delete) dengan
  PermApproval + path guard (hanya di skills dir, anti traversal); skill selection:
  daftar nama+deskripsi masuk system prompt (level 0 progressive disclosure).
- **D5 — CLI interaktif** (gap 5): `hermes-go` tanpa env bot = REPL lokal memakai
  Runner yang sama (bukti platform-agnostic core).
- **D6 — (opsional, nanti) subagent**: hanya jika ada kebutuhan nyata.

## Yang sengaja TIDAK ditiru
- 20+ platform, voice, ACP/IDE, kanban multi-agent, 70+ tools, plugin pip —
  biaya jauh melebihi nilai untuk 1 user + VM 4 GB. Hermes-Go kuat justru karena kecil.
