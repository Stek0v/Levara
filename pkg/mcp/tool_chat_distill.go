package mcp

// chat_distill: LLM distillation of an imported chat session into durable
// memories (E5b). Raw transcripts are not memory — this tool turns a
// conversation into a handful of hall-tagged records with provenance.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/chatimport"
	"github.com/stek0v/levara/pkg/llm"
)

const (
	distillDefaultMax      = 5
	distillTranscriptLimit = 24000
	distillToolPrefixLimit = 240
)

// DistillCandidate is one LLM-proposed memory before it is saved.
type DistillCandidate struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ToolChatDistill loads an imported conversation, asks the configured LLM to
// extract a few durable memories of the requested hall, and upserts them
// with provenance. dry_run returns candidates without writing.
func ToolChatDistill(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	platform, _ := args["platform"].(string)
	sessionID, _ := args["session_id"].(string)
	chatID, _ := args["chat_id"].(string)
	switch chatimport.Platform(platform) {
	case chatimport.PlatformCodex, chatimport.PlatformClaudeCode, chatimport.PlatformCursor:
	default:
		return errorResult(fmt.Sprintf("invalid platform %q (codex|claude-code|cursor)", platform))
	}
	if chatID == "" && sessionID == "" {
		return errorResult("'session_id' required")
	}
	hall, _ := args["hall"].(string)
	if hall == "" {
		hall = "decision"
	}
	if !IsValidHall(hall) {
		return errorResult(fmt.Sprintf("invalid hall '%s'. Valid values: %s", hall, strings.Join(ValidHalls(), ", ")))
	}
	maxMemories := distillDefaultMax
	if m, ok := args["max_memories"].(float64); ok && int(m) > 0 && int(m) <= 20 {
		maxMemories = int(m)
	}
	dryRun, _ := args["dry_run"].(bool)

	db := deps.DB()
	if db == nil {
		return errorResult("database not configured")
	}
	prov := deps.LLMProvider()
	if prov == nil {
		return errorResult("llm provider not configured")
	}

	// A synchronous call retains the caller's earlier deadline and cancellation.
	opCtx, cancelOp := context.WithTimeout(ctx, 4*time.Minute)
	defer cancelOp()
	actor := deps.MetadataActor(opCtx)
	local := actor.TrustedLocal && actor.UserID == "" && actor.TenantID == ""
	if !local {
		actor.TrustedLocal = false
	}
	chat, tx, release, err := distillSourceFence(opCtx, deps, actor, chatID, platform, sessionID, false)
	if err != nil {
		return errorResult(err.Error())
	}
	transcript, title, err := distillLoadTranscriptQuery(opCtx, tx.QueryContext, deps.Q, string(chat.Platform), chat.ID)
	release()
	platform, sessionID, chatID = string(chat.Platform), chat.SourceSessionID, chat.ID
	if err != nil {
		return errorResult(err.Error())
	}
	if transcript == "" {
		return errorResult("imported session not found or has no distillable messages")
	}

	recheck := func() error {
		_, _, release, err := distillSourceFence(opCtx, deps, actor, chat.ID, string(chat.Platform), "", false)
		if err == nil {
			release()
		}
		return err
	}
	candidates, err := distillViaLLM(opCtx, deps, prov, transcript, hall, maxMemories, recheck)
	if err != nil {
		return errorResult("llm distillation failed: " + err.Error())
	}
	if err := opCtx.Err(); err != nil {
		return errorResult(err.Error())
	}
	if err := recheck(); err != nil {
		return errorResult(err.Error())
	}
	if len(candidates) == 0 {
		return jsonResult(map[string]any{
			"chat_id": chatID, "session_id": sessionID, "platform": platform, "saved": 0,
			"candidates": []any{}, "message": "model returned no memorable items",
		})
	}

	provenance := fmt.Sprintf("[источник: %s session %s, %s]", platform, shortSessionID(sessionID), title)
	if !local {
		provenance += fmt.Sprintf(" [chat_id: %s]", chat.ID)
	}
	for i := range candidates {
		candidates[i].Value = strings.TrimSpace(candidates[i].Value) + " " + provenance
	}
	if dryRun {
		if err := opCtx.Err(); err != nil {
			return errorResult(err.Error())
		}
		return jsonResult(map[string]any{"session_id": sessionID, "platform": platform, "hall": hall, "dry_run": true, "candidates": candidates})
	}

	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	if room == "" {
		room = "chat-import"
	}
	ownerID := actor.UserID
	now := time.Now().UTC().Format(time.RFC3339)
	saved := 0
	for _, c := range candidates {
		if err := opCtx.Err(); err != nil {
			return errorResult(err.Error())
		}
		if c.Key == "" || c.Value == "" {
			continue
		}
		_, saveTx, releaseSave, err := distillSourceFence(opCtx, deps, actor, chat.ID, platform, "", true)
		if err != nil {
			return errorResult(err.Error())
		}
		var canonicalID, canonicalKey, canonicalType string
		if err := saveTx.QueryRowContext(opCtx, deps.Q(`
			INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, verification_status, source_task_id, source_receipt_ids, created_at, updated_at)
			VALUES ($1, $2, $3, 'project', $4, $5, $6, $7, false, 0, 'unverified', '', '[]', $8, $9)
			ON CONFLICT(key, owner_id, collection_name) DO UPDATE SET value = $10, room = $11, hall = $12, updated_at = $13,
				verification_status = 'unverified', source_task_id = '', source_receipt_ids = '[]'
			RETURNING id, key, type
		`),
			uuid.NewString(), c.Key, c.Value, ownerID, collectionName, room, hall, now, now,
			c.Value, room, hall, now).Scan(&canonicalID, &canonicalKey, &canonicalType); err != nil {
			releaseSave()
			return errorResult("save candidate " + c.Key + ": " + err.Error())
		}
		if err := chatimport.RecheckChatActor(opCtx, access.SQLPolicy{DB: db, Q: deps.Q}.WithReadTransaction(saveTx), actor, access.ActionWrite); err != nil {
			releaseSave()
			return errorResult(err.Error())
		}
		if err := saveTx.Commit(); err != nil {
			releaseSave()
			return errorResult(err.Error())
		}
		releaseSave()
		// Same vector-sidecar contract as save_memory so semantic recall
		// sees distilled records, not just SQL listing.
		if deps.EmbedAvailable() {
			if err := recheck(); err != nil {
				return errorResult(err.Error())
			}
			if err := indexMemoryContextFenced(opCtx, deps, collectionName, canonicalID, canonicalKey, c.Value, canonicalType, ownerID, func() (func(), error) {
				_, indexTx, releaseIndex, err := distillSourceFence(opCtx, deps, actor, chat.ID, platform, "", false)
				if err != nil {
					return nil, err
				}
				var currentKey, currentValue, currentType string
				query := "SELECT key,value,type FROM memories WHERE id=$1 AND owner_id=$2 AND collection_name=$3 AND superseded_by=''"
				if !memoryCommitSQLite(deps) {
					query += " FOR SHARE"
				}
				err = indexTx.QueryRowContext(opCtx, deps.Q(query), canonicalID, ownerID, collectionName).Scan(&currentKey, &currentValue, &currentType)
				if err == nil && (currentKey != canonicalKey || currentValue != c.Value || currentType != canonicalType) {
					err = fmt.Errorf("memory changed before source publication")
				}
				if err != nil {
					releaseIndex()
					return nil, err
				}
				if err := chatimport.RecheckChatActor(opCtx, access.SQLPolicy{DB: db, Q: deps.Q}.WithReadTransaction(indexTx), actor, access.ActionRead); err != nil {
					releaseIndex()
					return nil, err
				}
				return releaseIndex, nil
			}); err != nil {
				return errorResult("index candidate " + c.Key + ": " + err.Error())
			}
		}
		saved++
	}
	if err := opCtx.Err(); err != nil {
		return errorResult(err.Error())
	}
	return jsonResult(map[string]any{
		"session_id": sessionID, "platform": platform, "hall": hall,
		"saved": saved, "candidates": candidates,
	})
}

func distillSourceFence(ctx context.Context, deps Deps, actor access.MetadataActor, chatID, platform, session string, write bool) (chatimport.ChatIdentity, *sql.Tx, func(), error) {
	policy := access.SQLPolicy{DB: deps.DB(), Q: deps.Q}
	var tx *sql.Tx
	var release func()
	var err error
	var stopLocal func() bool
	if actor.TrustedLocal && actor.UserID == "" && actor.TenantID == "" {
		if write {
			tx, err = deps.DB().BeginTx(ctx, nil)
			if err == nil {
				release = func() { _ = tx.Rollback() }
			}
		} else {
			var conn *sql.Conn
			conn, err = deps.DB().Conn(ctx)
			if err == nil {
				txCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
				stopLocal = context.AfterFunc(ctx, cancel)
				tx, err = conn.BeginTx(txCtx, nil)
				if err != nil {
					stopLocal()
					cancel()
					_ = conn.Close()
				} else {
					release = func() { stopLocal(); _ = tx.Rollback(); cancel(); _ = conn.Close() }
				}
			}
		}
		if err == nil {
			policy = policy.WithReadTransaction(tx)
			if memoryCommitSQLite(deps) {
				_, err = tx.ExecContext(ctx, "UPDATE chat_import_sessions SET id=id WHERE 1=0")
				if err != nil {
					release()
				}
			}
		}
	} else if write {
		tx, policy, err = policy.BeginMetadataWrite(ctx, actor, memoryCommitSQLite(deps))
		if err == nil {
			release = func() { _ = tx.Rollback() }
		}
	} else {
		tx, policy, release, err = policy.BeginTransferFenceTx(ctx, memoryCommitSQLite(deps))
	}
	if err != nil {
		return chatimport.ChatIdentity{}, nil, nil, err
	}
	if err := chatimport.LockChatRegistry(ctx, tx, memoryCommitSQLite(deps), false); err != nil {
		release()
		return chatimport.ChatIdentity{}, nil, nil, err
	}
	chat, err := chatimport.ResolveChat(ctx, tx, deps.Q, policy, actor, chatID, platform, session)
	if err == nil && write {
		err = chatimport.RecheckChatActor(ctx, policy, actor, access.ActionWrite)
	}
	if err != nil {
		release()
		return chatimport.ChatIdentity{}, nil, nil, err
	}
	if stopLocal != nil && (!stopLocal() || ctx.Err() != nil) {
		release()
		return chatimport.ChatIdentity{}, nil, nil, ctx.Err()
	}
	return chat, tx, release, nil
}

// distillLoadTranscriptQuery renders the conversation into a compact
// user/assistant/reasoning transcript, skipping system boilerplate and
// truncating tool chatter — the model sees the dialogue, not the noise.
func distillLoadTranscriptQuery(ctx context.Context, query func(context.Context, string, ...any) (*sql.Rows, error), q func(string) string, platform, sessionID string) (string, string, error) {
	rows, err := query(ctx, q(`
		SELECT role, kind, content, session_title FROM chat_import_messages
		WHERE platform = $1 AND session_id = $2
		ORDER BY ordinal, source_created_at, external_id LIMIT 1500
	`), platform, sessionID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()

	var b strings.Builder
	title := ""
	total := 0
	for rows.Next() {
		var role, kind, content string
		if err := rows.Scan(&role, &kind, &content, &title); err != nil {
			return "", "", err
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		if role == "developer" {
			// Harness instructions (permissions, app-context, skills,
			// multi-agent roles): verified noise for distillation — with
			// them the model returns [], without them it extracts
			// decisions (live-tested against gemma4:e2b).
			continue
		}
		var line string
		switch kind {
		case "system", "tool_call", "tool_result":
			if len(content) > distillToolPrefixLimit {
				content = content[:distillToolPrefixLimit]
			}
			if strings.TrimSpace(content) == "" {
				continue
			}
			line = "[" + kind + "] " + content
		case "reasoning":
			line = "(рассуждение) " + truncateLine(content)
		default:
			switch role {
			case "user":
				line = "Пользователь: " + truncateLine(content)
			case "assistant":
				line = "Ассистент: " + truncateLine(content)
			case "developer":
				// non-boilerplate harness instructions — keep a hint
				line = "[инструкции] " + truncateLine(content)
			default:
				line = role + ": " + truncateLine(content)
			}
		}
		remaining := distillTranscriptLimit - total
		if remaining <= 400 {
			b.WriteString("\n…[транскрипт усечён]")
			break
		}
		// A single codex message can exceed the whole budget; truncate the
		// line instead of discarding the rest of the transcript.
		if len(line) > remaining {
			line = line[:remaining] + " …[сообщение усечено]"
		}
		b.WriteString(line)
		b.WriteString("\n\n")
		total += len(line)
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if err := rows.Close(); err != nil {
		return "", "", err
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return b.String(), title, nil
}

// distillViaLLM prompts for a strict JSON array and parses defensively:
// small local models wrap JSON in prose or code fences.
func distillViaLLM(ctx context.Context, deps Deps, prov llm.Provider, transcript, hall string, maxMemories int, recheck func() error) ([]DistillCandidate, error) {
	hallGuide := map[string]string{
		"decision":  "архитектурные и проектные решения с причинами (почему выбрали X, а не Y)",
		"discovery": "найденные проблемы, корневые причины, неочевидные закономерности",
		"advice":    "переиспользуемые практические правила и рекомендации",
		"fact":      "стабильные объективные факты (версии, адреса, параметры)",
		"event":     "значимые события с датами",
	}[hall]

	prompt := fmt.Sprintf(`Ты дистиллируешь переписку с AI-ассистентом в долговременную память проекта.
Извлеки не более %d записей категории "%s" — %s. Только существенное: то, что стоит вспомнить через полгода.
Формат ответа — строго JSON-массив объектов с полями key и value, без пояснений.
Пример:
[{"key": "storage-choice-pi", "value": "Выбран SQLite, а не Postgres: лимит RAM на Pi; WAL покрывает нагрузку."}]
Если извлекать нечего — верни [].

Переписка:
%s`, maxMemories, hall, hallGuide, transcript)

	if err := recheck(); err != nil {
		return nil, err
	}
	candidates, err := distillOnce(ctx, deps, prov, prompt, 0, maxMemories)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		// Small local models are brittle: near-identical prompts flip
		// between four items and []. One retry with mild sampling usually
		// recovers the extraction.
		if err := recheck(); err != nil {
			return nil, err
		}
		return distillOnce(ctx, deps, prov, prompt, 0.3, maxMemories)
	}
	return candidates, nil
}

func distillOnce(ctx context.Context, deps Deps, prov llm.Provider, prompt string, temperature float64, maxMemories int) ([]DistillCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := prov.ChatCompletion(ctx, llm.CompletionRequest{
		Model:       deps.LLMModel(),
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		Temperature: float32(temperature),
		MaxTokens:   1200,
	})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return parseDistillCandidates(resp.Content, maxMemories)
}

// parseDistillObject accepts a top-level single-pair {"key":"value"}
// object as a one-memory answer.
func parseDistillObject(body string) ([]DistillCandidate, bool) {
	s := strings.Index(body, "{")
	e := strings.LastIndex(body, "}")
	if s < 0 || e <= s {
		return nil, false
	}
	var pair struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal([]byte(body[s:e+1]), &pair); err != nil || (pair.Key == "" && pair.Value == "") {
		return nil, false
	}
	c := DistillCandidate{Key: slugifyDistillKey(pair.Key), Value: strings.TrimSpace(pair.Value)}
	if c.Key == "" || c.Value == "" {
		return nil, false
	}
	return []DistillCandidate{c}, true
}

func parseDistillCandidates(raw string, maxMemories int) ([]DistillCandidate, error) {
	body := strings.TrimSpace(raw)
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSuffix(body, "```")
	start := strings.Index(body, "[")
	end := strings.LastIndex(body, "]")
	if start < 0 || end <= start {
		// Some models answer with a single {"key": "value"} object instead
		// of the requested array — accept that shape before giving up.
		if c, ok := parseDistillObject(body); ok {
			return c, nil
		}
		return nil, fmt.Errorf("no JSON array in model output: %.200s", raw)
	}
	// Models don't always respect the field names: a common drift is a
	// single-pair {"<key>": "<value>"} object. Accept both shapes.
	var rawEntries []json.RawMessage
	if err := json.Unmarshal([]byte(body[start:end+1]), &rawEntries); err != nil {
		return nil, fmt.Errorf("bad JSON from model: %w", err)
	}
	var candidates []DistillCandidate
	for _, raw := range rawEntries {
		var kv struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &kv); err == nil && (kv.Key != "" || kv.Value != "") {
			candidates = append(candidates, DistillCandidate{Key: kv.Key, Value: kv.Value})
			continue
		}
		var pair map[string]string
		if err := json.Unmarshal(raw, &pair); err == nil && len(pair) > 0 {
			for k, v := range pair {
				candidates = append(candidates, DistillCandidate{Key: k, Value: v})
			}
		}
	}
	cleaned := candidates[:0]
	for _, c := range candidates {
		c.Key = slugifyDistillKey(c.Key)
		c.Value = strings.TrimSpace(c.Value)
		if c.Key == "" || c.Value == "" {
			continue
		}
		cleaned = append(cleaned, c)
		if len(cleaned) >= maxMemories {
			break
		}
	}
	return cleaned, nil
}

// translitCyrillic maps Cyrillic letters to a stable Latin approximation so
// small local models that answer with Russian keys still yield usable
// kebab-case identifiers instead of being filtered to empty.
var translitCyrillic = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "i", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "h", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
}

func slugifyDistillKey(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.TrimSpace(strings.ToLower(s)) {
		var out string
		switch {
		case unicode.IsLetter(r) && r < 128, unicode.IsDigit(r):
			out = string(r)
		default:
			if t, ok := translitCyrillic[r]; ok {
				out = t
			}
		}
		switch {
		case out != "":
			b.WriteString(out)
			lastDash = false
		case r == '-' || r == '_' || r == ' ':
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// truncateLine caps a single message inside the distill transcript so one
// huge reply cannot consume the whole budget.
func truncateLine(s string) string {
	const perLine = 600
	if len(s) > perLine {
		return s[:perLine] + " …[усечено]"
	}
	return s
}

func shortSessionID(id string) string {
	if len(id) > 13 {
		return id[:13] + "…"
	}
	return id
}

func errorResult(msg string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: "Error: " + msg}}, IsError: true}
}
