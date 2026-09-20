# [codex] Как устроен WAL в SQLite?

- session: 11111111-2222-3333-4444-555555555555
- platform: codex
- model: gpt-5.2-codex
- created: 2026-09-15T10:00:00.090Z
- cwd: /Users/demo/src/project
- cli_version: 0.50.0
- originator: codex_cli_rs
- source: terminal

## [1] user · text

Как устроен WAL в SQLite?

Интересует, как он влияет на параллельные читатели.

## [2] assistant · reasoning

> Пользователь спрашивает про WAL. Сначала объясню -journal режим, затем WAL и снапшот-изоляцию читателей.

## [3] assistant · text

WAL (write-ahead log) меняет модель конкуренции: писатели append в файл WAL, читатели работают по снапшоту из -wal.

## [4] assistant · tool_call: exec

```json
exec
const r = await tools.exec_command({cmd:"sqlite3 demo.db 'PRAGMA journal_mode;'"}); text(r);
```

## [5] tool · tool_result

```
Script completed
Output:
wal
```

## [6] user · text

Спасибо, ясно. А как проверить ghp_0123456789abcdefghijkl не утёк ли токен в логи?
