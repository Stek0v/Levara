# Настройка Levara на новом компьютере

Полная инструкция для подключения Levara MCP к Claude Code с автозагрузкой контекста и синхронизацией с Pi.

## 1. Установить зависимости

```bash
# Ollama (embedding + LLM)
curl -fsSL https://ollama.com/install.sh | sh

# Модели
ollama pull nomic-embed-text-v2-moe   # embedding, 957MB, dim=768
ollama pull qwen3.5:2b                 # LLM для RAG (опционально)
```

## 2. Собрать Levara

```bash
git clone <repo-url> && cd Levara

# Для текущей ОС
go build -o levara ./cmd/server/

# Или для Pi (ARM64)
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o levara-arm64 ./cmd/server/
```

## 3. Настроить автозапуск

### macOS (launchd)

```bash
mkdir -p ~/levara-local/data

# Скопировать бинарник
cp levara ~/levara-local/levara

# Создать plist
cat > ~/Library/LaunchAgents/com.stek0v.levara.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.stek0v.levara</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/YOUR_USER/levara-local/levara</string>
        <string>-standalone=true</string>
        <string>-dim=768</string>
        <string>-shards=1</string>
        <string>-port=8081</string>
        <string>-grpc-port=0</string>
        <string>-data-dir=/Users/YOUR_USER/levara-local/data</string>
        <string>-node-id=mac1</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>DB_PROVIDER</key>
        <string>sqlite</string>
        <key>EMBEDDING_ENDPOINT</key>
        <string>http://localhost:11434/v1/embeddings</string>
        <key>EMBEDDING_MODEL</key>
        <string>nomic-embed-text-v2-moe</string>
        <key>LLM_ENDPOINT</key>
        <string>http://localhost:11434/v1</string>
        <key>LLM_MODEL</key>
        <string>qwen3.5:2b</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/Users/YOUR_USER/levara-local/levara.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/YOUR_USER/levara-local/levara.log</string>
    <key>WorkingDirectory</key>
    <string>/Users/YOUR_USER/levara-local</string>
</dict>
</plist>
EOF

# Заменить YOUR_USER
sed -i '' "s/YOUR_USER/$(whoami)/g" ~/Library/LaunchAgents/com.stek0v.levara.plist

# Запустить
launchctl load ~/Library/LaunchAgents/com.stek0v.levara.plist

# Проверить
curl -s http://localhost:8081/health
```

### Linux (systemd)

```bash
sudo mkdir -p /var/lib/levara/data
sudo cp levara /usr/local/bin/levara

sudo cat > /etc/systemd/system/levara.service << 'EOF'
[Unit]
Description=Levara Knowledge Engine
After=network.target ollama.service

[Service]
Type=simple
User=YOUR_USER
ExecStart=/usr/local/bin/levara -standalone=true -dim=768 -shards=1 -port=8081 -grpc-port=0 -data-dir=/var/lib/levara/data -node-id=linux1
Environment=DB_PROVIDER=sqlite
Environment=EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
Environment=EMBEDDING_MODEL=nomic-embed-text-v2-moe
Environment=LLM_ENDPOINT=http://localhost:11434/v1
Environment=LLM_MODEL=qwen3.5:2b
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo sed -i "s/YOUR_USER/$(whoami)/g" /etc/systemd/system/levara.service
sudo systemctl daemon-reload
sudo systemctl enable --now levara
curl -s http://localhost:8081/health
```

## 4. Подключить к Claude Code

```bash
# Добавить MCP сервер
claude mcp add --transport http levara http://localhost:8081/mcp
```

## 5. Настроить автозагрузку контекста

### Hook для SessionStart

Создать в корне проекта:

```bash
mkdir -p .claude/hooks

cat > .claude/hooks/load-levara-context.sh << 'HOOK'
#!/bin/bash
LEVARA_URL="${LEVARA_URL:-http://localhost:8081}"
if ! curl -s --max-time 2 "$LEVARA_URL/health" >/dev/null 2>&1; then
    echo "[levara] Local instance not reachable at $LEVARA_URL"
    exit 0
fi
MANIFEST=$(curl -s --max-time 3 "$LEVARA_URL/api/v1/sync/manifest" 2>/dev/null)
MEM_COUNT=$(echo "$MANIFEST" | python3 -c "import sys,json; print(json.load(sys.stdin)['memories']['count'])" 2>/dev/null)
MEMORIES=$(curl -s --max-time 3 "$LEVARA_URL/api/v1/memories" 2>/dev/null | python3 -c "
import sys,json
try:
    mems = json.load(sys.stdin)
    for m in mems[:15]:
        print(f'- [{m[\"type\"]}] {m[\"key\"]}: {m[\"value\"][:120]}')
except: pass
" 2>/dev/null)
cat <<EOF
[Levara MCP Context — auto-loaded]
Instance: $LEVARA_URL | Memories: $MEM_COUNT
Use recall_memory/save_memory/search to interact with project knowledge.

Project memories:
$MEMORIES
EOF
HOOK

chmod +x .claude/hooks/load-levara-context.sh
```

### Добавить hook в settings

Добавить в `.claude/settings.local.json` (или создать):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup",
        "hooks": [
          {
            "type": "command",
            "command": ".claude/hooks/load-levara-context.sh",
            "timeout": 10
          }
        ]
      },
      {
        "matcher": "compact",
        "hooks": [
          {
            "type": "command",
            "command": "echo '[levara] Context compacted. Use recall_memory to reload.'"
          }
        ]
      }
    ]
  }
}
```

### Добавить в CLAUDE.md

Добавить в конец CLAUDE.md проекта:

```markdown
## Levara MCP Memory

Levara MCP подключена. Используй проактивно:

- При старте: контекст загружается автоматически
- Нужна информация: `recall_memory(query="тема")`
- После задачи: `save_memory(key="...", value="...", type="project")`
- Кросс-проект: `cross_search(collections=[...])`
- Синхронизация: `sync(remote_url="http://PI_IP:8080/api/v1", direction="pull")`
```

## 6. Синхронизация с Pi / другим инстансом

### Первичная синхронизация

```bash
# Проверить оба инстанса
curl -s http://localhost:8081/api/v1/sync/manifest | python3 -m json.tool
curl -s http://PI_IP:8080/api/v1/sync/manifest | python3 -m json.tool

# Pull всё с Pi
curl -s http://PI_IP:8080/api/v1/sync/export/memories | \
  curl -s -X POST http://localhost:8081/api/v1/sync/import/memories \
  -H 'Content-Type: application/json' -d @-

curl -s http://PI_IP:8080/api/v1/sync/export/interactions | \
  curl -s -X POST http://localhost:8081/api/v1/sync/import/interactions \
  -H 'Content-Type: application/json' -d @-

curl -s http://PI_IP:8080/api/v1/sync/export/graph | \
  curl -s -X POST http://localhost:8081/api/v1/sync/import/graph \
  -H 'Content-Type: application/json' -d @-
```

### CLI aliases (опционально)

```bash
mkdir -p ~/bin

# sync_levara [pull|push|both]
cat > ~/bin/sync_levara << 'EOF'
#!/bin/bash
DIR="${1:-both}"
LOCAL="http://localhost:8081/api/v1"
REMOTE="http://PI_IP:8080/api/v1"

do_sync() {
    local FROM=$1 TO=$2 LABEL=$3
    echo "$LABEL"
    echo -n "  memories:     "
    curl -s "$FROM/sync/export/memories" | curl -s -X POST "$TO/sync/import/memories" -H 'Content-Type: application/json' -d @- | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['imported'],'imported,',d['skipped'],'skipped')"
    echo -n "  interactions: "
    curl -s "$FROM/sync/export/interactions" | curl -s -X POST "$TO/sync/import/interactions" -H 'Content-Type: application/json' -d @- | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['imported'],'imported,',d['skipped'],'skipped')"
    echo -n "  graph:        "
    curl -s "$FROM/sync/export/graph" | curl -s -X POST "$TO/sync/import/graph" -H 'Content-Type: application/json' -d @- | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['nodes_imported'],'nodes,',d['edges_imported'],'edges')"
}

case "$DIR" in
    pull) do_sync "$REMOTE" "$LOCAL" "Pull Remote -> Local" ;;
    push) do_sync "$LOCAL" "$REMOTE" "Push Local -> Remote" ;;
    *) do_sync "$REMOTE" "$LOCAL" "Pull Remote -> Local"; echo ""; do_sync "$LOCAL" "$REMOTE" "Push Local -> Remote" ;;
esac
echo ""; echo "Done."
EOF

# man_levara
cat > ~/bin/man_levara << 'EOF'
#!/bin/bash
for LABEL_URL in "Local|http://localhost:8081" "Remote|http://PI_IP:8080"; do
    LABEL="${LABEL_URL%%|*}"
    URL="${LABEL_URL##*|}"
    echo "=== $LABEL ($URL) ==="
    curl -s "$URL/api/v1/sync/manifest" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print('  Model:', d['embed_model'], 'Dim:', d['embed_dim'])
print('  Memories:', d['memories']['count'], 'Interactions:', d['interactions']['count'])
print('  Graph:', d['graph_nodes']['count'], 'nodes,', d['graph_edges']['count'], 'edges')
cols = d.get('collections') or []
print('  Collections:', len(cols))
" 2>/dev/null || echo "  (unreachable)"
    echo ""
done
EOF

chmod +x ~/bin/sync_levara ~/bin/man_levara

# Заменить PI_IP
sed -i '' "s/PI_IP/10.23.0.53/g" ~/bin/sync_levara ~/bin/man_levara  # macOS
# sed -i "s/PI_IP/10.23.0.53/g" ~/bin/sync_levara ~/bin/man_levara  # Linux
```

## 7. Обновление бинарника

```bash
cd Levara
go build -o ~/levara-local/levara ./cmd/server/

# macOS
launchctl kickstart -k gui/$(id -u)/com.stek0v.levara

# Linux
sudo systemctl restart levara
```

## 8. Проверка

```bash
# Здоровье
curl -s http://localhost:8081/health/details | python3 -m json.tool

# Manifest
man_levara

# Sync
sync_levara

# MCP tools видны в Claude Code
claude mcp list
```
