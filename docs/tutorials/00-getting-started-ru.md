# Быстрый старт Levara

Сначала запустите долговечную память с SQLite, затем добавьте модель для
семантического поиска. Полный справочник запуска: [getting started](../getting-started.md).
Эти команды описывают исходный код, а не состояние чужого сервера.

## 1. Собрать и запустить

Нужна версия Go из [go.mod](../../go.mod).

```bash
git clone https://github.com/Stek0v/Levara.git
cd Levara
make build
DB_PROVIDER=sqlite DB_PATH="$PWD/data/levara.db" \
./levara-server -profile=standalone -host=127.0.0.1 -port=8080 \
  -grpc-port=0 -dim=768 -data-dir="$PWD/data"
```

Это локальный сервер без аутентификации; он слушает loopback. SQL задан явно:
один `-profile=standalone` не создаёт SQL-базу автоматически. Для команды и
удалённых клиентов используйте [team tutorial](04-team-deploy.md).

Во втором терминале из корня репозитория:

```bash
export LEVARA_URL=http://127.0.0.1:8080/api/v1
./levara health --details
```

## 2. Сохранить и вспомнить

```bash
curl -fsS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"save_memory","arguments":{"collection":"tutorial","room":"onboarding","hall":"fact","key":"first-memory","value":"Учебная память хранится в SQLite."}}}'

curl -fsS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall_memory","arguments":{"collection":"tutorial","query":"SQLite","limit":3}}}'
```

Проверьте ключ `first-memory` и отсутствие `error` / `result.isError`.
HTTP 200 не исключает ошибку MCP-инструмента. Это лексический пример без моделей;
семантическое совпадение другими словами требует embeddings и готового индекса.
Повторный запуск с тем же `DB_PATH` сохраняет запись.

В агентской сессии последовательность — `set_context`, `wake_up`, затем recall
нужной темы. Независимые sessionless HTTP-вызовы, как выше, должны каждый явно
задавать collection. `room` означает тему, `hall` — вид знания; не сохраняйте
код, пароли и временный TODO как долговечную память.

## 3. Подключить агента и документы

[Agent integration](02-agent-integration.md) показывает, как аккуратно добавить
MCP-конфигурацию в существующий файл. Endpoint MCP — `/mcp`, REST/CLI base —
`/api/v1`. WebUI запускается отдельно на порту 3000, см. [WebUI](../../webui/README.md).

Для документов выполните [semantic setup](../getting-started.md#add-semantic-document-search)
с рабочим embedding endpoint и совпадающим `-dim`, затем:

```bash
./levara add 'Alice maintains the Acme payment service.' --dataset=demo
./levara cognify --dataset=demo --collection=demo --wait
./levara search 'Acme' --collection=demo --type=CHUNKS_LEXICAL --top-k=5
```

`add` сохраняет материал, `cognify` обрабатывает, поиск проверяет результат.
Для файла используйте `add --file=./report.pdf --dataset=reports`.
Флаги CLI со значением пишутся `--key=value`, глобальные `--url` и `--token`
ставятся перед командой. Справка — `./levara help`.

## Дальше

- [Первая память](01-first-memory.md) — scope и восстановление.
- [База документов](03-knowledge-base.md) — ingestion → processing → search.
- [Работа с документами](../document-management.md) — оригиналы, ошибки и индивидуальный доступ.
- [Профили](../profile-presets.md) — personal, solo_pro, team, enterprise.
- [LDAP/AD и SSO](../enterprise-identity.md) — поддерживаемые интеграции и пробелы.
- [Проверки](../testing.md) — тесты без live-сервисов и отдельные интеграционные сценарии.
