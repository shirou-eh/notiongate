# notiongate — шпаргалка

**Установка**
```bash
curl -fsSL https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.sh | bash
# Windows: irm https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.ps1 | iex
# или: make build && ./notiongate --help
```

**Старт**
```bash
./notiongate menu                    # меню
./notiongate serve                   # http://127.0.0.1:8787
curl http://127.0.0.1:8787/healthz
```

**Клиент (ADE)**
```python
from openai import OpenAI
client = OpenAI(base_url="http://127.0.0.1:8787/v1", api_key="any")
client.chat.completions.create(model="sonnet-5", messages=[{"role":"user","content":"Привет!"}])
```
`Ctrl+C` весь текст (до 100k + картинки/PDF) → `Ctrl+V` в чат → `Enter` — один вызов, 2–5 мин reasoning.

**Команды**
```bash
./notiongate accounts list; ./notiongate status
./notiongate plugins list; ./notiongate autoreg --provider example --count 3
./notiongate autostart install # systemd/launchd
./notiongate bundle; ./notiongate deploy user@host:/opt/notiongate
notiongate update --check; notiongate update
```

**API**
```
GET /admin/status, /admin/accounts, POST /admin/accounts, POST /admin/autoreg
POST /v1/chat/completions, POST /v1/messages, GET /v1/models
```

Плагины: `plugins/<name>/` + `config.json` → `plugins/README.md`
