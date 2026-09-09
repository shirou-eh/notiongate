# extensions — алиас для plugins

Эта папка — алиас. Ядро сканирует и `./plugins`, и `./extensions` (см. `PLUGINS_DIR`).

Можешь класть свои плагины куда удобнее:

* `plugins/myplugin/` — каноничный путь
* `extensions/myplugin/` — то же самое, для тех кто привык к слову extensions

Оба работают: `plugin.LoadExecPlugins` проверяет обе директории.

Смотри `plugins/README.md` — там полная дока.
