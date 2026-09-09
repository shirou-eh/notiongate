# autoreg-example — шаблон авторега Notion

Реалистичный скелет. Замени `TODO` на свой флоу: почта → капча → регистрация Notion → достать `token_v2`.

## Структура

```
plugins/autoreg-example/
  plugin.go              ← логика (Go)
  config.example.json    ← скопируй в config.json, заполни ключи
  README.md
```

## Конфиг (изолирован)

`config.json` — только для этого плагина, не в корневой `.env`:

```json
{
  "mail_domain": "example.com",
  "mail_api_key": "...",
  "captcha_key": "...",
  "proxy_url": "socks5://user:pass@host:1080"
}
```

В коде: `plugin.ProviderConfig(deps.Config.PluginsDir, p.Name())` — читает `plugins/autoreg-example/config.json`.

## Использование

```bash
go build -o notiongate ./cmd/notiongate
./notiongate autoreg --provider autoreg-example --count 2 --label-prefix my
./notiongate accounts list
# или через API:
curl -X POST http://127.0.0.1:8787/admin/autoreg \
  -H "Authorization: Bearer $ADMIN_KEY" \
  -d '{"provider":"autoreg-example","count":2}'
```

Каждый созданный аккаунт автоматически проходит `AddWithBootstrap` (space discovery) и попадает в пул с ротацией 80%.

## Куда вписывать свой код

В `plugin.go` метод `CreateAccounts` — секция `// TODO: your real autoreg flow here`.
Вернуть надо `[]plugin.CreatedAccount{{TokenV2: "v02:...", Label: "...", Proxy: "..."}}`.

Ошибки пробрасывай как `fmt.Errorf` — ядро отметит аккаунты и залогирует.
