# PANDUAN DEPLOY VM42 — Hermes-Go

Paket ini untuk ASUS VivoPC VM42 (Ubuntu/Debian, RAM 4 GB, x86-64).

## Isi paket

```
hermes-go-vm42/
├── hermes-go              # binary statis-ish (x86-64, 11 MB)
├── deploy/
│   ├── systemd/hermes-go.service
│   └── scripts/{install.sh, backup.sh, benchmark.sh}
├── skills/
│   ├── system-admin/SKILL.md
│   └── developer/SKILL.md
├── .env.example
└── PANDUAN-VM42.md        # file ini
```

## Langkah 1 — Copy paket ke VM42

Dari komputer mana pun yang punya akses SSH ke VM42:

```bash
scp hermes-go-vm42.tar.gz user@VM42:/tmp/
ssh user@VM42 "tar xzf /tmp/hermes-go-vm42.tar.gz -C /tmp/"
```

## Langkah 2 — Instal (sekali per update)

```bash
ssh user@VM42
cd /tmp/hermes-go-vm42
sudo bash deploy/scripts/install.sh
```

Installer: membuat user `hermes` non-root, copy binary ke `/opt/hermes-go/`,
copy skills (`cp -rn` — tidak menimpa skill yang sudah ada), membuat
`/opt/hermes-go/.env` **hanya jika belum ada** (tidak pernah menimpa),
mendaftarkan systemd unit, enable service.

## Langkah 3 — Isi kredensial

```bash
sudo nano /opt/hermes-go/.env
```

Wajib diisi:
```
LLM_API_KEY=isi-key-kamu
LLM_BASE_URL=https://api.openai.com/v1     # atau proxy OpenAI-compatible
LLM_MODEL=gpt-4o-mini
TELEGRAM_BOT_TOKEN=token-dari-BotFather
TELEGRAM_ALLOWED_USERS=7804936884          # ID Telegram kamu (format polos atau koma)
```

Opsional: `MEMORY_RETENTION_DAYS`, `HTTP_ALLOWED_HOSTS`, `HEALTH_ADDR`
(default `127.0.0.1:8089` — jangan diubah ke 0.0.0.0).

```bash
sudo chmod 600 /opt/hermes-go/.env        # izin ketat
```

## Langkah 4 — Jalankan & cek

```bash
sudo systemctl start hermes-go
systemctl status hermes-go --no-pager      # harus: active (running)
curl 127.0.0.1:8089/health                 # harus: ok
sudo journalctl -u hermes-go -f            # log live
```

Lalu kirim pesan dari Telegram ke bot kamu. Contoh: "Sebutkan 3 warna dasar".
Balasan LLM = E2E dasar lulus.

## Langkah 5 — Benchmark (wajib untuk laporan resource)

```bash
cd /tmp/hermes-go-vm42
sudo OUT=./benchmark-results.txt bash deploy/scripts/benchmark.sh
```

Skrip mengukur: RSS proses, total/available RAM sistem, CPU ticks —
idle (60 dtk), setelah 1 tugas LLM, setelah 5 tugas berurutan, dan setelah
restart (plus `PRAGMA integrity_check` DB). Skrip akan meminta kamu menekan
Enter setelah mengirim tugas via Telegram. Hasil di `benchmark-results.txt`.

## Backup & restore

```bash
sudo bash /opt/hermes-go/deploy/scripts/backup.sh /opt/hermes-go /var/backups/hermes-go
# Restore: systemctl stop hermes-go; cp backup.db /opt/hermes-go/data/hermes.db;
#          chown hermes:hermes /opt/hermes-go/data/hermes.db; systemctl start hermes-go
```

## Troubleshooting

| Gejala | Periksa |
|---|---|
| Service gagal start | `journalctl -u hermes-go -n 50` |
| "user tidak diizinkan" | `TELEGRAM_ALLOWED_USERS` di .env — format `7804936884` atau `111,222` |
| Conflict 409 di log | Bot token dipakai proses lain — matikan instance lain |
| Health tidak merespons | `curl 127.0.0.1:8089/health`; cek `HEALTH_ADDR` |
| LLM error | `LLM_BASE_URL`/`LLM_API_KEY` benar? `journalctl` menunjukkan status code |

## Upgrade versi berikutnya

Copy paket baru, jalankan ulang `install.sh` — .env dan data TIDAK disentuh,
skill existing tidak ditimpa.
