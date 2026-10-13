#!/usr/bin/env bash
# Hermes-Go — one-liner installer.
# Pemakaian:  curl -fsSL https://raw.githubusercontent.com/fhria5/hermes-go/main/deploy/scripts/remote-install.sh | bash
# Atau:       bash remote-install.sh [versi]   (default: latest release)
set -euo pipefail

REPO="fhria5/hermes-go"
INSTALL_DIR=/opt/hermes-go
VERSION="${1:-latest}"

# --- butuh root (sudo otomatis kalau perlu) ---
SUDO=""
if [[ $EUID -ne 0 ]]; then
  command -v sudo >/dev/null || { echo "Jalankan sebagai root atau install sudo" >&2; exit 1; }
  SUDO="sudo"
fi

# --- deteksi arsitektur ---
case "$(uname -m)" in
  x86_64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Arsitektur $(uname -m) belum didukung (tersedia: amd64, arm64)" >&2; exit 1 ;;
esac
echo ">> Arsitektur: $ARCH"

# --- pilih versi release ---
AUTH=()
if [[ -n "${GITHUB_TOKEN:-}" ]]; then AUTH=(-H "Authorization: token $GITHUB_TOKEN"); fi
if [[ "$VERSION" == "latest" ]]; then
  VERSION=$(curl -fsSL "${AUTH[@]}" "https://api.github.com/repos/$REPO/releases/latest" | grep -oP '"tag_name":\s*"\K[^"]+') \
    || { echo "Gagal ambil versi terbaru (repo private? set GITHUB_TOKEN=... dulu)" >&2; exit 1; }
fi
echo ">> Versi: $VERSION"

# --- unduh binary + installer sesuai arsitektur ---
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
URL="https://github.com/$REPO/releases/download/$VERSION/hermes-go-linux-$ARCH.tar.gz"
echo ">> Unduh: $URL"
curl -fsSL "${AUTH[@]}" "$URL" | $SUDO tar xz -C "$TMP"

# --- jalankan installer lokal (buat user, /opt, systemd unit, .env jika belum ada) ---
$SUDO bash "$TMP/deploy/scripts/install.sh"

# --- installer remote butuh token untuk repo private ---
: "${GITHUB_TOKEN:=}"
if [[ -n "$GITHUB_TOKEN" ]]; then
  CURL_AUTH=(-H "Authorization: token $GITHUB_TOKEN")
else
  CURL_AUTH=()
fi

echo
echo "✅ Hermes-Go $VERSION terpasang di $INSTALL_DIR"
echo ">> Lanjut: isi kredensial lalu start:"
echo "   $SUDO nano $INSTALL_DIR/.env"
echo "   $SUDO systemctl enable --now hermes-go"
echo "   $SUDO journalctl -u hermes-go -f"
