# exec-example — плагин на любом языке

Не нужно трогать Go. Папка изолирована.

## Как работает

* `plugin.json` — манифест (имя, команда, таймаут)
* `config.json` — изолированные настройки этого плагина
* `run.py` — твой код (python / bash / node / бинарник)

Ядро при старте находит `plugins/exec-example/plugin.json`, регистрирует провайдера.

## Протокол

stdin: `{"config": {...}, "options": {"count":2,"proxy":"...","label_prefix":"my"}}`
stdout: `[{"token_v2":"v02:...","label":"..."}]`
stderr — логи.

## Тест

```bash
echo '{"config":{"prefix":"test"},"options":{"count":2}}' | ./plugins/exec-example/run.py
./notiongate autoreg --provider exec-example --count 2
curl -X POST http://127.0.0.1:8787/admin/autoreg -H "Authorization: Bearer $ADMIN_KEY" -d '{"provider":"exec-example","count":2}'
```
