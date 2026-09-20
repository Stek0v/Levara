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
	switch chatimport.Platform(platform) {
	case chatimport.PlatformCodex, chatimport.PlatformClaudeCode, chatimport.PlatformCursor:
	default:
		return errorResult(fmt.Sprintf("invalid platform %q (codex|claude-code|cursor)", platform))
	}
	if sessionID == "" {
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

	transcript, title, err := distillLoadTranscript(ctx, db, deps.Q, platform, sessionID)
	if err != nil {
		return errorResult(err.Error())
	}
	if transcript == "" {
		return errorResult("imported session not found or has no distillable messages")
	}

	// Local models need 50-90s per pass; the MCP request context is
	// tighter. Distillation (LLM + saves) runs under its own budget.
	opCtx, cancelOp := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancelOp()
	candidates, err := distillViaLLM(opCtx, deps, prov, transcript, hall, maxMemories)
	if err != nil {
		return errorResult("llm distillation failed: " + err.Error())
	}
	if len(candidates) == 0 {
		return jsonResult(map[string]any{
			"session_id": sessionID, "platform": platform, "saved": 0,
			"candidates": []any{}, "message": "model returned no memorable items",
		})
	}

	provenance := fmt.Sprintf("[источник: %s session %s, %s]", platform, shortSessionID(sessionID), title)
	for i := range candidates {
		candidates[i].Value = strings.TrimSpace(candidates[i].Value) + " " + provenance
	}
	if dryRun {
		return jsonResult(map[string]any{"session_id": sessionID, "platform": platform, "hall": hall, "dry_run": true, "candidates": candidates})
	}

	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	if room == "" {
		room = "chat-import"
	}
	ownerID := extractOwnerID(ctx)
	now := time.Now().UTC().Format(time.RFC3339)
	saved := 0
	for _, c := range candidates {
		if c.Key == "" || c.Value == "" {
			continue
		}
		var canonicalID string
		if err := db.QueryRowContext(opCtx, deps.Q(`
			INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
			VALUES ($1, $2, $3, 'project', $4, $5, $6, $7, false, 0, $8, $9)
			ON CONFLICT(key, owner_id, collection_name) DO UPDATE SET value = $10, room = $11, hall = $12, updated_at = $13
			RETURNING id
		`),
			uuid.NewString(), c.Key, c.Value, ownerID, collectionName, room, hall, now, now,
			c.Value, room, hall, now).Scan(&canonicalID); err != nil {
			return errorResult("save candidate " + c.Key + ": " + err.Error())
		}
		// Same vector-sidecar contract as save_memory so semantic recall
		// sees distilled records, not just SQL listing.
		if deps.EmbedAvailable() {
			indexMemorySync(deps, collectionName, canonicalID, c.Key, c.Value, "project")
		}
		saved++
	}
	return jsonResult(map[string]any{
		"session_id": sessionID, "platform": platform, "hall": hall,
		"saved": saved, "candidates": candidates,
	})
}

// distillLoadTranscript renders the conversation into a compact
// user/assistant/reasoning transcript, skipping system boilerplate and
// truncating tool chatter — the model sees the dialogue, not the noise.
func distillLoadTranscript(ctx context.Context, db *sql.DB, q func(string) string, platform, sessionID string) (string, string, error) {
	rows, err := db.QueryContext(ctx, q(`
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
	return b.String(), title, nil
}

// distillViaLLM prompts for a strict JSON array and parses defensively:
// small local models wrap JSON in prose or code fences.
func distillViaLLM(ctx context.Context, deps Deps, prov llm.Provider, transcript, hall string, maxMemories int) ([]DistillCandidate, error) {
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

	candidates, err := distillOnce(ctx, deps, prov, prompt, 0, maxMemories)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		// Small local models are brittle: near-identical prompts flip
		// between four items and []. One retry with mild sampling usually
		// recovers the extraction.
		return distillOnce(ctx, deps, prov, prompt, 0.3, maxMemories)
	}
	return candidates, nil
}

func distillOnce(ctx context.Context, deps Deps, prov llm.Provider, prompt string, temperature float64, maxMemories int) ([]DistillCandidate, error) {
	resp, err := prov.ChatCompletion(ctx, llm.CompletionRequest{
		Model:       deps.LLMModel(),
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		Temperature: float32(temperature),
		MaxTokens:   1200,
	})
	if err != nil {
		return nil, err
	}
	return parseDistillCandidates(resp.Content, maxMemories)
}

func parseDistillCandidates(raw string, maxMemories int) ([]DistillCandidate, error) {
	body := strings.TrimSpace(raw)
	body = strings.TrimPrefix(body, "```json")
	body = strings.TrimPrefix(body, "```")
	body = strings.TrimSuffix(body, "```")
	start := strings.Index(body, "[")
	end := strings.LastIndex(body, "]")
	if start < 0 || end <= start {
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
