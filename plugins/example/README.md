# example — минимальный Go-плагин

Шаблон для копирования. Живёт в своей папке, конфиг изолирован.

## Что делает

* Регистрируется как `example`
* Реализует все опциональные способности (Autoreg + Hook) — удали ненужные
* `CreateAccounts` возвращает mock-токены (для теста интеграции без внешних сервисов)

## Как скопировать

```bash
cp -r plugins/example plugins/myplugin
# поменяй в plugins/myplugin/plugin.go:
#   func (p *Plugin) Name() string { return "myplugin" }
```

## Регистрация

Добавь в `plugins/all.go`:

```go
package plugins
import _ "github.com/shirou-eh/notiongate/plugins/myplugin"
```

Или в `internal/plugin/all.go` — раскомментируй пример.

## Конфиг

`config.example.yaml` → скопируй в `config.yaml` рядом с `plugin.go` если нужен.
