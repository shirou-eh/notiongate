# notiongate

<p align="center"><img src="assets/mascot.svg" width="96" alt="Гейтик"></p>

Go-прокси для Notion AI: пул аккаунтов, **авторотация при достижении 80% лимитов**,
OpenAI- и Anthropic-совместимый API. Один бинарник, SQLite, без CGO. Гейтик просто существует.

> [!WARNING]
> **Неофициальный инструмент.** Работает через приватный API Notion с cookie
> `token_v2`. Такой доступ с высокой вероятностью нарушает условия использования
> Notion — аккаунты могут быть ограничены или **заблокированы**.
> Используете на свой страх и риск.
>
> Используя этот проект, вы принимаете все условия и риски, описанные в
> **[DISCLAIMER.md](DISCLAIMER.md)** (риски бана, безопасность `token_v2`,
> отказ от ответственности). Если вы не согласны — не используйте проект.

## Возможности

- **OpenAI-совместимый API** — `POST /v1/chat/completions` (stream/non-stream), `GET /v1/models`
- **Anthropic-совместимый API** — `POST /v1/messages` (stream/non-stream) для Claude Code / anthropic SDK
- **Пул аккаунтов** — подбор по остатку квоты, sticky-сессии (переиспользование треда Notion на пользователя)
- **Авторотация на 80%** — аккаунт с usage ≥ `ROTATE_AT` уходит в `reserve` и исключается из ротации; при 100% — `exhausted`
- **Failover** — 401/403 → аккаунт помечается `invalid`, 429 → `cooldown` (по `Retry-After`), 5xx/таймаут → повтор на другом аккаунте; `ai_not_enabled` перебирает всех (аккаунт-специфично), а одинаковые пустые стримы подряд останавливаются на 3 — это уровень IP/модели, перебор дальше только кладёт пул- **Фоновый рефрешер** — периодическая проверка валидности токенов
- **Egress-прокси на аккаунт** — http/https/socks5 (снижает риск бана)
- **Лёгкая авторизация** — один статический `API_KEY` (Bearer или `x-api-key`); на localhost можно без ключа
- **Admin REST + CLI** — без веб-UI
- Учёт лимитов локальный (счётчики запросов на окно day/month), т.к. у Notion нет публичного API квот

## Установка (curl / irm — одним движением)

```bash
# Linux / macOS — curl
curl -fsSL https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.sh | bash
# Windows PowerShell — irm
irm https://raw.githubusercontent.com/shirou-eh/notiongate/main/scripts/install.ps1 | iex
```

Или из исходников:

```bash
make build
./notiongate login --serve
```

Что произойдёт:

1. Откроется окно Chrome (временный профиль, ничего не трогает основной браузер).
2. **Один раз войдите в Notion** в этом окне (код из почты / Google — полностью
   автоматизировать вход нельзя, так устроена Notion).
3. Как только вход завершён, `notiongate` сам заберёт cookie `token_v2` из браузера,
   сам определит `user_id`, `space_id`, `email` и список моделей, сам добавит аккаунт
   в пул (сам выберет рабочий домен notion.so/notion.com) — и сразу стартует API на
   `http://127.0.0.1:8787`. Дальше пользуетесь клиентом (пример ниже).

Браузер закроется сам. Баннер «Chrome for Testing is only for automated testing» —
безобидный, это служебная сборка для автоматизации.

> На VPS без GUI команда `login` подскажет варианты: добавить cookie вручную
> (`accounts add`), выполнить login на машине с GUI (БД переносится) или
> использовать `--profile-dir` с заранее подготовленным профилем Chrome.

### Ручной способ (без браузера)

1. Откройте [notion.so](https://www.notion.so), войдите.
2. `F12` → **Application** → **Cookies** → `https://www.notion.so` → скопируйте `token_v2`.
3. Или вставьте `scripts/extract_notion_info.js` в **Console** — получите готовый JSON.
4. Добавьте аккаунт (прокси сам дотянется остальное):

```bash
./notiongate accounts add --cookie "token_v2=v02%3A..."
```

### Запуск сервера

```bash
./notiongate serve
# 2025.. INFO notiongate listening addr=127.0.0.1:8787 accounts=1 rotate_at=0.8
```

### Использовать из ADE / клиента

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8787/v1", api_key="any")  # без API_KEY на localhost
r = client.chat.completions.create(
    model="sonnet-5",          # точное имя из GET /v1/models (алиасы вида claude-sonnet-5 тоже резолвятся)
    messages=[{"role": "user", "content": "Привет!"}],
    stream=True,
)
for chunk in r:
    print(chunk.choices[0].delta.content or "", end="")
```

Anthropic-совместимо:

```bash
curl http://127.0.0.1:8787/v1/messages \
  -H "content-type: application/json" \
  -d '{"model":"sonnet-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Hi"}]}'
```

### Модели

Актуальный список всегда отдаёт `GET /v1/models` (живой каталог из Notion, обновляется сам).
Коротко (проверено живым опросом Notion):

- **Claude** — `sonnet-5`, `opus-5`, `sonnet-4.6`, `opus-4.6`, `opus-4.7`, `opus-4.8`, `haiku-4.5`
- **GPT** — `gpt-5.6-luna`, `gpt-5.6-terra`, `gpt-5.6-sol`, `gpt-5.5`, `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.4-nano`, `gpt-5.2`
- **Gemini** — `gemini-3.7-flash`, `gemini-3.6-flash`, `gemini-3.5-flash`, `gemini-3-flash`, `gemini-3.1-pro`
- **Grok** — `grok-4.6`, `grok-4.5`, `grok-4.3`, `grok-build-0.1`
- **Open models** — `kimi-k3`, `kimi-k2.6`, `kimi-k2.7-code`, `deepseek-v4-pro`, `deepseek-v4-flash`, `glm-5.2`

Алиасы вида `claude-opus-5`, `claude-sonnet-5` тоже резолвятся. Неизвестное имя — честный
`400 model_not_found`, а не молчаливый дефолт. Дефолт при пустом `model` — `sonnet-5`.

### Агенты и правка файлов (tool calling)

 notiongate — только транспорт: модель возвращает `tool_calls`, а исполняет их **агент
 на твоём компе** (OpenCode / Claude Code / ADE правит файлы, запускает команды).
 Сервер сам ничего не запускает — поэтому для правки файлов направь своего агента
 на `http://127.0.0.1:8787/v1` как на OpenAI-совместимый бэкенд и работай как обычно:
 `tool_calls` туда-обратно возятся честно, включая stream-режим.

 ⚠️ Совместимость — свойство модели, проверено живьём:
 - ✅ вызывают тулзы сами: `gpt-5.4`, `gpt-5.4-nano` (файлы реально создаются, проверено 2026-09-12)
 - ❌ отказываются (чужеродные тулзы противоречат их системному промпту):
   `sonnet-5`, `sonnet-4.6`, `haiku-4.5`, `gemini-3.5-flash`, `gemini-3.7-flash`
   (перепробованы system/config-инжекты, few-shot демо, prefill — детектят и стоят)
 - 🚫 пустой стрим на триалах (в каталоге есть, инференс молчит): `opus-5`, `gpt-5.6-sol`
 - Остальные не проверялись — если твоя модель отказывается, ставь агенту `gpt-5.4`.

 Практика: рассуждать — `sonnet-5`, дёргать тулзы — `gpt-5.4`.

### tools_model: сильные думают, послушные вызывают (2026-09-12, проверено живьём)

Отказ Sonnet/Gemini пробивается двухмодельной цепочкой: запрошенная модель
рассуждает, а compliant-исполнитель переводит план в `tool_calls`:

```python
r = client.chat.completions.create(
    model="sonnet-5",          # думает
    messages=[{"role": "user", "content": "Создай файл /tmp/a.txt с текстом 'hi'."}],
    tools=[...],               # твои Edit/Bash/...
    tool_choice="auto",
    extra_body={"tools_model": "gpt-5.4-nano"},  # вызывает
)
# -> finish_reason=tool_calls, исполняешь локально, шлёшь tool result,
#    файл реально создан (проверено: /tmp/ng_chain2.txt на диске).
```

Правила честные и явные: цепочка только opt-in; срабатывает только если первый
проход не дал вызовов; mid-chain (история уже с результатами) hop не стреляет —
дублирующих записей нет; неизвестный `tools_model` — обычный текстовый ответ.
Стоит один лишний апстрим-вызов за ход. На шаге 2 ворчливая модель может
поворчать вместо «готово» — файл к тому моменту уже создан, агенту это не мешает.

### Effort и подача тулзов (расширения API)

```python
r = client.chat.completions.create(
    model="gpt-5.4",
    messages=[...],
    tools=[...],
    tool_choice="auto",
    extra_body={
        "reasoning_effort": "high",   # low | medium | high (алиас: effort)
        "tools_placement": "system",  # system (default) | config
    },
)
```

- `reasoning_effort` — явная строка-хинт в system («думай кратко/тщательно»).
  У Notion нет нативной крутилки effort — это честная инструкция, а не скрытый рероут.
- `tools_placement: "config"` — спеки тулзов едут в config-блоке транскрипта
  (харнесс-нативный вид), в тексте только протокол вызова. По умолчанию `system`.
  На отказывающихся моделях не помогает (проверено на `sonnet-5`) — им нужен
  compliant исполнитель вроде `gpt-5.4`.

## Меню и маскот

Гейтик — немой пиксельный привратник notiongate. Просто существует, ничего не пишет. Везде как логотип: в баннере CLI, в меню, в доках. SVG: `assets/mascot.svg`.

```bash
./notiongate              # баннер + помощь
./notiongate menu         # интерактивное меню с Гейтиком (статус, добавить акк, плагины, автозапуск)
./notiongate setup        # мастер настройки
```

Автозапуск — протоколы:
```bash
./notiongate autostart status              # детект systemd/launchd/docker
./notiongate autostart install             # systemd --user или launchd (healthcheck + Restart=always)
./notiongate autostart generate | less     # посмотреть unit/plist без установки
# Docker — уже готов: docker compose up -d
```

## Управление

```bash
./notiongate accounts list            # статус пула с % лимитов
./notiongate accounts test <id>       # проверить сессию
./notiongate accounts remove <id>
./notiongate status                   # сводка по статусам и трафику за сегодня
```

Admin API (ключ `ADMIN_KEY` или `API_KEY`):

| Метод  | Путь                        | Назначение                                  |
|--------|-----------------------------|---------------------------------------------|
| GET    | `/admin/status`             | пул + эффективные статусы + счётчики         |
| GET    | `/admin/accounts`           | список аккаунтов (токен маскирован)          |
| POST   | `/admin/accounts`           | добавить: `{"token_v2":"...","label":"","proxy":"","limit_req":0,"window_type":"month","rotate_at":0.8,"force":false}` |
| PATCH  | `/admin/accounts/{id}`      | изменить `label`/`proxy`/`limit_req`/`window_type`/`rotate_at`/`status` (`enable`/`disable`) |
| DELETE | `/admin/accounts/{id}`      | удалить                                      |
| POST   | `/admin/accounts/{id}/test` | проверить сессию                             |
| GET    | `/admin/stats`              | трафик: сегодня / 24ч / 30 дней              |

## Конфигурация (ENV)

| Переменная          | По умолчанию | Описание                                              |
|---------------------|--------------|-------------------------------------------------------|
| `API_KEY`           | *(пусто)*    | ключ клиентов; на localhost можно пусто, на `0.0.0.0` запросы без ключа отклоняются |
| `ADMIN_KEY`         | —            | ключ admin API (иначе используется `API_KEY`)          |
| `NOTIONGATE_HOST`   | `127.0.0.1`  | интерфейс                                              |
| `NOTIONGATE_PORT`   | `8787`       | порт                                                   |
| `DB_PATH`           | `./data/notiongate.db` | SQLite                                       |
| `ROTATE_AT`         | `0.8`        | порог авторотации (80%)                                |
| `DEFAULT_WINDOW`    | `month`      | окно счётчиков: `day` / `month`                        |
| `DEFAULT_LIMIT`     | `0`          | лимит запросов на окно по умолчанию (`0` = не ограничивать) |
| `STICKY_SESSIONS`   | `true`       | закрепление аккаунта за пользователем                  |
| `MAX_ATTEMPTS`      | `3`          | попыток фейловера на запрос                            |
| `UPSTREAM_TIMEOUT`  | `5m`         | таймаут запроса к Notion                               |
| `REFRESH_INTERVAL`  | `15m`        | период фоновой проверки токенов                        |
| `NOTION_BASE_URL`   | `https://www.notion.so` | upstream (для тестов)                       |
| `LOG_LEVEL`         | `info`       | `debug`/`info`/`warn`/`error`                          |

Лимиты на аккаунт задаются при добавлении (`limit_req`, `window_type`, `rotate_at`) или через
`PATCH /admin/accounts/{id}`.

## Установка с одного компа на сервер (перенос всего нужного)

Собирает БД, `.env`, `plugins/*/config.json` в один архив и разворачивает на сервере (бинарь ставится под архитектуру сервера).

```bash
# Вариант A — Go-команда (кроссплатформенно)
./notiongate bundle                           # → notiongate-bundle-*.tgz
./notiongate deploy user@host:/opt/notiongate
# Вариант B — bash-скрипт (то же, плюс автозапуск)
./scripts/transfer.sh user@host:/opt/notiongate
# Ручной перенос
scp notiongate-bundle-*.tgz user@host:/tmp/ && ssh user@host 'tar -xzf /tmp/notiongate-bundle.tgz -C /opt/notiongate && cd /opt/notiongate && ./notiongate autostart install'
```

Что внутри бандла: `data/notiongate.db`, `.env`, `docker-compose.yml`, `plugins/`, `extensions/`. Бинарь ставится отдельно через `scripts/install.sh` (curl/irm) — корректно для разных OS/ARCH.

## Docker

```bash
echo "API_KEY=your-secret" > .env
docker compose up -d --build
# порт проброшен на 127.0.0.1:8787; для внешнего доступа меняйте ports и ставьте API_KEY
```

## Как работает ротация

```
usage = req_count / limit_req   (счётчики в SQLite за окно day/month)

usage < ROTATE_AT(0.8)   → active   : участвует в ротации
usage >= ROTATE_AT       → reserve  : исключён из ротации, используется только если active нет
usage >= 1.0             → exhausted: исключён полностью (оживает в новом окне)
429                      → cooldown : пауза по Retry-After (по умолчанию 10m)
401/403                  → invalid  : токен умер, нужен re-login ( accounts add заново )
```

Подбор: sticky-аккаунт пользователя → `active` с максимальным остатком квоты (tie → LRU) →
`reserve` как последний резерв. Неудачные аккаунты внутри одного запроса не выбираются повторно.

## Расширения — плагины (полная кастомизация без правок ядра)

Каждый плагин — отдельная папка со своим `config.json` — вставляется как нож по маслу.

```
plugins/
  example/              ← Go шаблон (копируй)
  autoreg-example/      ← шаблон авторега
  exec-example/         ← шаблон exec-плагина (любой язык)
  myplugin/             ← твой плагин
    plugin.go / run.py
    plugin.json         ← для exec
    config.json         ← изолированные настройки (не коммитится)
```

* **Go-плагин:** `cp -r plugins/example plugins/myplugin`, поменяй `Name()` и логику, добавь одну строку в `cmd/notiongate/main.go`:
  ```go
  import _ "notiongate/plugins/myplugin"
  ```
* **Exec-плагин (любой язык):** создай `plugins/myplugin/plugin.json` + `run.py` (`chmod +x`). Протокол: stdin `{"config":{},"options":{"count":2}}` → stdout `[{"token_v2":"..."}]`. Ноль правок Go.

Изолированные настройки: каждый плагин читает только `plugins/<name>/config.json` (или `.yaml`). Секреты не попадают в общий `.env`. См. `PLUGINS_DIR` (default `./plugins`, также сканируется `./extensions`).

Подробно: `plugins/README.md`. Команды:

```bash
./notiongate plugins list
./notiongate autoreg --provider example --count 3
curl -X POST http://127.0.0.1:8787/admin/autoreg -H "Authorization: Bearer $ADMIN_KEY" \
  -d '{"provider":"exec-example","count":2}'
```

## Структура

```
cmd/notiongate/        CLI (serve, accounts, status, autoreg, plugins)
internal/api/          HTTP: OpenAI, Anthropic, admin, фейловер-исполнитель
internal/translate/    протоколы ↔ transcript Notion
internal/notion/       клиент приватного API Notion (адаптер — меняется только здесь)
internal/pool/         пул, состояния, счётчики, рефрешер
internal/store/        SQLite (аккаунты, счётчики, лог запросов)
internal/config/       ENV-конфиг
internal/plugin/       система расширений (реестр, loader, exec)
plugins/               примеры и твои плагины (каждый в своей папке)
extensions/            алиас для plugins (тоже сканируется)
scripts/               extract_notion_info.js для DevTools Console
```

## Ограничения

- Вложения (картинки/PDF по URL и data:) поддерживаются; usage-токены Notion отдаёт не всегда —
  тогда оценка эвристическая (~4 символа/токен)
- Tool calling — passthrough через `tool_calls` (см. раздел про агентов выше)
- Приватный API Notion может измениться: вся интеграция изолирована в `internal/notion`
- `token_v2` равен полному доступу к аккаунту — храните `data/` и `.env` в секрете

## Лицензия и отказ от ответственности

Использование регулируется [DISCLAIMER.md](DISCLAIMER.md): неофициальная интеграция,
риск блокировки аккаунтов, software предоставляется «как есть», авторы не несут
ответственности за последствия.
