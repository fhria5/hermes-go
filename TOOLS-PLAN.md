# RENCANA TOOLS Hermes-Go — setelah reverse engineering tool system Hermes Agent

Sumber: docs "Tools & Toolsets" + "Architecture" Hermes Agent (NousResearch, MIT).
Dipetakan ke 13 tools Hermes-Go saat ini (read_file, write_file, list_directory,
search_files, shell, http_request, search_memory, save_memory, task_status,
task_cancel, web_search, web_fetch, system_info).

## Kategori Hermes Agent → status Hermes-Go

| Kategori Hermes | Tools Hermes-Go | Status |
|---|---|---|
| Terminal & Files | shell, read_file, write_file, list_directory, search_files | ✅ ADA (shell pakai allowlist + timeout, tanpa TTY — sama prinsipnya) |
| Web | web_search, web_fetch | ✅ ADA (C1) |
| Memory & recall | search_memory, save_memory | ✅ ADA (FTS5 menyusul di D1) |
| Automation | task_status, task_cancel + scheduler | ✅ ADA (scheduler bawaan + CAS + skip-backlog) |
| System | system_info | ✅ ADA (bukan di Hermes, tambahan sendiri) |
| Skill management | skill_manage | ❌ BELUM — D4 |
| Agent orchestration | todo, clarify, execute_code, delegate_task | ❌ BELUM — D6/opsional |
| Browser | browser_* | ❌ TIDAK direncanakan (Playwright ~622MB — tak layak untuk VM 4GB) |
| Media | vision_analyze, image_generate, text_to_speech | ❌ TIDAK direncanakan (butuh API tambahan; nilai rendah via Telegram teks) |
| Integrations | ha_*, MCP | ❌ TIDAK direncanakan (belum ada kebutuhan nyata) |

## Toolset preset (pola Hermes yang MURAH dan BERGUNA ditiru)

Hermes: grup tool (toolset) bisa on/off per platform, ada preset `safe`.
Hermes-Go: registry sudah ada; tinggal tambah konsep **profile toolset** via env:

    HERMES_TOOLSET=minimal   → file tools + memory + system_info (tanpa shell & web)
    HERMES_TOOLSET=default   → semua kecuali shell
    HERMES_TOOLSET=full      → semua (shell tetap lewat approval + allowlist)

Implementasi: filter di `Registry.AllowedTools()` berdasarkan nama tool — ±40 baris
+ test. Manfaat: bot di chat bisa dibatasi tanpa rebuild.

## Perbaikan perilaku tool dari Hermes yang layak ditiru (murah, dampak nyata)

1. **Penjelasan death-signal**: exit code -9/137 → teks "dibunuh signal 9 (SIGKILL) —
   sering karena OOM killer". Shell tool sekarang cuma bilang "exit status -1".
   ±20 baris di shell.go.
2. **Large result 3 lapis** (Hermes): truncation in-tool → simpan sisa ke file →
   budget per giliran. Hermes-Go: sudah truncation + MaxOutputBytes; tahap 2
   (simpan sisa ke file + kasih path ke LLM) bisa menyusul — LLM bisa baca lanjutannya
   via read_file. ±50 baris.
3. **UTF-16 transcode** di read_file: deteksi BOM UTF-16 → konversi UTF-8, bukan
   menolak sebagai binary. ±25 baris.
4. **Timeout per tool** sudah ada (ToolTimeout). ✅

## Urutan eksekusi (digabung dengan ROADMAP D1–D6)

- **D1** FTS5 + prompt tiering (+ toolset preset di atas — satu tema: efisiensi konteks)
- **D2** Context compression (rolling summary)
- **D2.5** Perbaikan tool murah: death-signal text, UTF-16 transcode, large-result file
- **D3** Streaming jawaban LLM
- **D4** skill_manage tool (WriteSkill/patch/delete, PermApproval, path guard)
- **D5** CLI interaktif
- **D6** (opsional) execute_code sederhana (Python via subprocess + timeout) — versi
  mini dari execute_code Hermes; delegate_task DITUNDA.

## Yang sengaja TIDAK dibangun (keputusan tegas)

- Browser automation (ukuran + kompleksitas tak layak untuk 4GB/1 user)
- Media/vision/TTS (butuh API berbayar tambahan)
- MCP client (protokol besar; kalau nanti butuh, baru dipertimbangkan)
- Multi-backend terminal (Docker/SSH/Modal...) — local saja cukup
