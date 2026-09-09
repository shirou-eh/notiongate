# Plugins — полная кастомизация без правок ядра

Каждый плагин живёт в **своей папке** со своим конфигом, как нож по маслу вставляется в ядро. Хочешь авторегер — пишешь плагин, кладёшь рядом, не трогая `internal/`.

## Структура

```
plugins/
  README.md                ← ты тут
  example/                 ← Go-шаблон (копируй)
    plugin.go
    config.example.yaml
    README.md
  autoreg-example/         ← реалистичный шаблон авторега
    plugin.go
    config.example.json
    README.md
  my-super-autoreg/        ← твой плагин (любой язык)
    plugin.json            ← манифест для exec-плагина
    config.json            ← изолированные настройки
    run.py                 ← твой код (python / bash / go / что угодно)
```

## Два пути — выбирай один

### Путь A: Go-плагин (глубокая интеграция, максимальная скорость)

1. Скопируй шаблон:
   ```bash
   cp -r plugins/example plugins/myplugin
   # или
   cp -r plugins/autoreg-example plugins/my-autoreg
   ```
2. Открой `plugins/myplugin/plugin.go`, поменяй `Name()` на `"myplugin"` и реализуй логику.
   Уже доступны интерфейсы:
   - `plugin.AutoregProvider` — `CreateAccounts(ctx, cfg, opts) ([]CreatedAccount, error)`
   - `plugin.HTTPProvider` — `RegisterRoutes` + `Middleware` (любые ручки)
   - `plugin.HookProvider` — `OnAccountAdded` / `OnAccountRemoved`
3. Зарегистрируй одной строкой — добавь в `cmd/notiongate/main.go`:
   ```go
   import _ "github.com/shirou-eh/notiongate/plugins/myplugin"
   ```
   (рядом с уже существующими `_ "github.com/shirou-eh/notiongate/plugins/example"`). Пересобери — плагин появится.

4. Изолированный конфиг: положи `plugins/myplugin/config.json` (или `.yaml` — конвертируется). Плагин читает его в `Init` через `plugin.ProviderConfig(dir, name)`.

```bash
go build -o notiongate ./cmd/notiongate
./notiongate plugins list
./notiongate autoreg --provider myplugin --count 5
```

### Путь B: Exec-плагин (любой язык, ноль правок Go)

Подходит если хочешь писать на Python/Node/Bash или держать плагин в отдельном репо.

1. Создай папку:
   ```bash
   mkdir -p plugins/my-exec-autoreg
   ```
2. Создай `plugin.json` — манифест:
   ```json
   {
     "name": "my-exec-autoreg",
     "description": "Мой авторегер на Python",
     "version": "0.1.0",
     "command": "./run.py",
     "args": [],
     "timeout": "180s"
   }
   ```
3. Положи исполняемый файл `run.py` (или `run.sh`, бинарник):
   ```python
   #!/usr/bin/env python3
   import json, sys
   req = json.load(sys.stdin)  # {"config": {...}, "options": {"count":2}}
   cfg = req["config"]
   opts = req["options"]
   # TODO: твоя логика → получи token_v2
   out = [{"token_v2": "v02:...","label":"my-1"}, {"token_v2":"v02:...","label":"my-2"}]
   json.dump(out, sys.stdout)
   ```
   `chmod +x plugins/my-exec-autoreg/run.py`

4. Конфиг плагина изолирован: `plugins/my-exec-autoreg/config.json`
   ```json
   {"mail_domain":"example.com","captcha_key":"xxx"}
   ```

5. Запуск — ядро само найдёт exec-плагин при старте:
   ```bash
   PLUGINS_DIR=./plugins ./notiongate serve
   ./notiongate plugins list   # увидишь my-exec-autoreg
   curl -X POST http://127.0.0.1:8787/admin/autoreg -H "Authorization: Bearer $ADMIN_KEY" \
     -d '{"provider":"my-exec-autoreg","count":3}'
   ```

Протокол exec-плагина:
- stdin → `{"config": <config.json>, "options": {"count":2,"proxy":"...","label_prefix":"my"}}`
- stdout ← `[{"token_v2":"...","label":"..."}]` **или** `{"accounts":[...]}`
- stderr → логи (уходят в slog)
- exit 0 = успех, !=0 = ошибка

## Изолированные настройки

* Каждый плагин читает **только** свою папку: `plugins/<name>/config.json` (или `.yaml`).
* Глобальные env: `PLUGINS_DIR` (по умолчанию `./plugins`), `AUTOREG_*` не нужны — всё в папке плагина.
* Секреты не попадают в основной `.env` — держи `config.json` вне git (`echo "config.json" >> plugins/<name>/.gitignore`).

## Куда расширять

* Авторег — лишь пример. Через те же интерфейсы можно:
  * `HTTPProvider` — добавить `/plugins/<name>/...` ручки, админку, вебхуки.
  * `Middleware` — логирование, метрики, рейт-лимиты.
  * `HookProvider` — реагировать на добавление/удаление аккаунтов.

Смотри `internal/plugin/plugin.go` — там 90 строк, вся система.

## FAQ

**Нужно ли трогать core?** Нет, кроме одной blank-import строки для Go-плагинов. Exec-плагины — вообще ноль правок.

**Можно ли держать плагин в отдельном репо?** Да. Скопируй папку или сделай git submodule: `git submodule add https://github.com/you/my-plugin plugins/my-plugin`.

**Конфликт имён?** `Name()` должен быть уникальным — ядро паникует при дубликате на старте.

**Пример за 30 секунд?** `cp -r plugins/example plugins/demo && ./notiongate autoreg --provider demo --count 2 --label-prefix demo` — создаст 2 mock-аккаунта.
