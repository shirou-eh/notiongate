#!/usr/bin/env bash
set -euo pipefail

# notiongate — curl installer
# usage:
#   curl -fsSL https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.sh | bash -s -- --version latest --dir ~/.local/bin

REPO="shirou-eh/notiongate"
BINARY="notiongate"
VERSION="${NOTIONGATE_VERSION:-latest}"
INSTALL_DIR="${NOTIONGATE_INSTALL_DIR:-}"
NO_MODIFY_PATH="${NO_MODIFY_PATH:-0}"

# Гейтик — немой, просто существует. Показываем его молча.
show_banner() {
  cat <<'BANNER'
  ╭──────────────╮   notiongate
  │  ████████    │   Гейтик просто есть.
  │ ███ ██ ███   │
  │ ██████████   │
  ╰──────────────╯
BANNER
}

info()  { printf "\033[1;34m[notiongate]\033[0m %s\n" "$*"; }
warn()  { printf "\033[1;33m[warn]\033[0m %s\n" "$*" >&2; }
err()   { printf "\033[1;31m[error]\033[0m %s\n" "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2;;
    --dir) INSTALL_DIR="$2"; shift 2;;
    --help|-h) echo "Usage: install.sh [--version latest|v0.1.0] [--dir ~/.local/bin]"; exit 0;;
    *) shift;;
  esac
done

show_banner

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$OS" in
  linux) OS="linux" ;;
  darwin) OS="darwin" ;;
  *) err "unsupported OS: $OS (windows: use install.ps1)" ;;
esac
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) err "unsupported arch: $ARCH" ;;
esac

# выбор директории установки
if [[ -z "$INSTALL_DIR" ]]; then
  if [[ -d "$HOME/.local/bin" ]] || [[ ":$PATH:" == *":$HOME/.local/bin:"* ]]; then
    INSTALL_DIR="$HOME/.local/bin"
  elif [[ -w "/usr/local/bin" ]]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="$HOME/.local/bin"
  fi
fi
mkdir -p "$INSTALL_DIR"

info "OS: $OS/$ARCH  version: $VERSION  dir: $INSTALL_DIR"

# резолв latest → тег
if [[ "$VERSION" == "latest" ]]; then
  if command -v curl >/dev/null 2>&1; then
    RESOLVED=$(curl -fsSL -o /dev/null -w "%{redirect_url}" "https://github.com/${REPO}/releases/latest" 2>/dev/null | sed 's#.*/tag/##' || true)
    if [[ -n "$RESOLVED" ]]; then VERSION="$RESOLVED"; info "latest -> $VERSION"; fi
  fi
fi
VERSION="${VERSION#v}"
ASSET="${BINARY}-${OS}-${ARCH}"
if [[ "$OS" == "linux" ]]; then ASSET="${ASSET}" ; fi
if [[ "$OS" == "darwin" ]]; then ASSET="${ASSET}" ; fi
# пробуем несколько шаблонов имён релиза
URLS=(
  "https://github.com/${REPO}/releases/download/v${VERSION}/${ASSET}"
  "https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY}_${OS}_${ARCH}"
  "https://github.com/${REPO}/releases/download/v${VERSION}/${BINARY}"
)

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
BIN_TMP="$TMP_DIR/$BINARY"

downloaded=""
for url in "${URLS[@]}"; do
  info "try $url"
  if command -v curl >/dev/null 2>&1; then
    if curl -fsSL "$url" -o "$BIN_TMP" 2>/dev/null; then downloaded="$url"; break; fi
  elif command -v wget >/dev/null 2>&1; then
    if wget -qO "$BIN_TMP" "$url" 2>/dev/null; then downloaded="$url"; break; fi
  fi
done

# fallback: go install из исходников
if [[ -z "$downloaded" ]]; then
  warn "бинарник не найден в релизах, пробую go install..."
  if ! command -v go >/dev/null 2>&1; then
    err "не найден ни релиз ни go. Установи go или укажи --version"
  fi
  GOBIN="$TMP_DIR" go install "notiongate/cmd/notiongate@v${VERSION}" 2>/dev/null || \
  GOBIN="$TMP_DIR" go install "./cmd/notiongate" 2>/dev/null || \
    err "go install провалился"
  # go install кладёт в $TMP_DIR/notiongate
  BIN_TMP="$TMP_DIR/$BINARY"
  [[ -f "$BIN_TMP" ]] || err "go install не создал бинарник"
  downloaded="go install"
fi

# если скачали архив — распаковать
if file "$BIN_TMP" 2>/dev/null | grep -q "gzip\|Zip\|archive"; then
  info "распаковываю архив..."
  tar -xzf "$BIN_TMP" -C "$TMP_DIR" 2>/dev/null || unzip -o "$BIN_TMP" -d "$TMP_DIR" 2>/dev/null || true
  # найти бинарник внутри
  FOUND=$(find "$TMP_DIR" -type f -name "$BINARY*" -perm -111 2>/dev/null | head -n1)
  [[ -n "$FOUND" ]] && BIN_TMP="$FOUND"
fi

chmod +x "$BIN_TMP"
# атомарная установка
if [[ -w "$INSTALL_DIR" ]]; then
  mv -f "$BIN_TMP" "$INSTALL_DIR/$BINARY"
else
  warn "нужен sudo для $INSTALL_DIR"
  sudo mv -f "$BIN_TMP" "$INSTALL_DIR/$BINARY"
fi

# PATH hint
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]] && [[ "$NO_MODIFY_PATH" == "0" ]]; then
  warn "$INSTALL_DIR не в PATH — добавь: export PATH=\"\$HOME/.local/bin:\$PATH\""
  # пробуем добавить в shell rc
  for rc in "$HOME/.bashrc" "$HOME/.zshrc"; do
    if [[ -f "$rc" ]] && ! grep -q "$INSTALL_DIR" "$rc" 2>/dev/null; then
      echo "export PATH=\"\$HOME/.local/bin:\$PATH\"" >> "$rc"
      info "добавил $INSTALL_DIR в $rc"
      break
    fi
  done
fi

info "установлено: $INSTALL_DIR/$BINARY ($downloaded)"
if command -v "$INSTALL_DIR/$BINARY" >/dev/null 2>&1; then
  "$INSTALL_DIR/$BINARY" version 2>/dev/null || true
fi

cat <<'NEXT'

  Гейтик просто существует.

  Дальше:
    notiongate menu              — интерактивное меню
    notiongate setup             — мастер настройки
    notiongate login --serve     — добавить аккаунт и запустить
    notiongate autostart install — автозапуск (systemd/launchd/docker)

NEXT
