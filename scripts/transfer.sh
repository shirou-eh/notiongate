#!/usr/bin/env bash
set -euo pipefail
# notiongate — перенос с одного компа на сервер одним движением
# usage:
#   ./scripts/transfer.sh user@host:/opt/notiongate
#   ./scripts/transfer.sh user@host            # -> ~/notiongate
#
# Что делает:
#   1) собирает бандл (БД, .env, plugins/*/config.json) через notiongate bundle
#   2) scp на сервер
#   3) ssh: ставит бинарь (install.sh), распаковывает бандл, autostart install, healthz

REMOTE="${1:-}"
if [[ -z "$REMOTE" ]]; then
  echo "Usage: $0 user@host[:/path]"
  echo "  example: $0 root@1.2.3.4:/opt/notiongate"
  exit 2
fi

# Гейтик — молчаливый баннер
cat <<'BANNER'
  ╭──────────────╮   notiongate
  │  ████████    │   Гейтик просто есть.
  │ ███ ██ ███   │
  │ ██████████   │
  ╰──────────────╯
BANNER

if [[ "$REMOTE" != *:* ]]; then
  REMOTE="$REMOTE:~/notiongate"
fi
HOST="${REMOTE%%:*}"
RPATH="${REMOTE#*:}"

echo "[notiongate] bundle..."
go build -o /tmp/notiongate ./cmd/notiongate 2>/dev/null || make build 2>/dev/null || true
if [[ ! -f ./notiongate ]]; then go build -o notiongate ./cmd/notiongate; fi
./notiongate bundle 2>&1 | tail -n 5
BUNDLE=$(ls -t notiongate-bundle-*.tgz | head -n1)
echo "[notiongate] bundle: $BUNDLE -> $REMOTE"

# проверяем ssh
ssh -o BatchMode=yes -o ConnectTimeout=5 "$HOST" "echo ok" 2>&1 | grep -q ok || {
  echo "[error] ssh $HOST недоступен (проверь ключи)"
  exit 1
}

echo "[notiongate] scp bundle..."
scp "$BUNDLE" "$REMOTE/notiongate-bundle.tgz"

echo "[notiongate] remote install..."
ssh "$HOST" bash -s -- "$RPATH" <<'EOSSH'
set -e
RPATH="$1"
mkdir -p "$RPATH"
cd "$RPATH"
echo "[remote] dir: $(pwd)"
if [[ -f notiongate-bundle.tgz ]]; then
  echo "[remote] extract bundle..."
  tar -xzf notiongate-bundle.tgz
  rm notiongate-bundle.tgz
fi
# ставим бинарь если его нет
if [[ ! -f ./notiongate ]]; then
  echo "[remote] install binary..."
  curl -fsSL https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.sh | bash
  # если install положил в ~/.local/bin — скопируем
  if [[ -f ~/.local/bin/notiongate ]]; then cp ~/.local/bin/notiongate ./notiongate; fi
fi
chmod +x ./notiongate 2>/dev/null || true
echo "[remote] autostart..."
./notiongate autostart install 2>&1 | tail -n 5 || true
# health
sleep 2
curl -fs http://127.0.0.1:8787/healthz 2>&1 | head -c 200 || echo "[remote] healthz not yet (maybe need API_KEY)"
echo ""
echo "[remote] done: $RPATH"
EOSSH

echo "[notiongate] done — проверь: ssh $HOST 'curl -s http://127.0.0.1:8787/healthz'"
echo "  Гейтик просто стоит на сервере."
