// settings.go — Runtime settings API for React frontend.
// GET /settings — read current config
// PUT /settings — update config (per-user if DB available, else global env)
package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/gofiber/fiber/v2"
)

// SettingsDTO matches Levara frontend expected format.
type SettingsDTO struct {
	Theme  string `json:"theme,omitempty"`
	Locale string `json:"locale,omitempty"`
	// DefaultCollection is the collection preselected in the chat/search pages ("" = all).
	DefaultCollection string `json:"default_collection,omitempty"`
	LLMProvider       string `json:"llm_provider"`
	LLMModel          string `json:"llm_model"`
	LLMEndpoint       string `json:"llm_endpoint"`
	LLMAPIKey         string `json:"llm_api_key,omitempty"`
	EmbedProvider     string `json:"embedding_provider"`
	EmbedModel        string `json:"embedding_model"`
	EmbedEndpoint     string `json:"embedding_endpoint"`
	EmbedDimension    int    `json:"embedding_dimension"`
	GraphEngine       string `json:"graph_engine"`
	GraphURL          string `json:"graph_url"`
	GraphDatabase     string `json:"graph_database"`
	VectorEngine      string `json:"vector_engine"`
	ChunkStrategy     string `json:"chunk_strategy"`
	ChunkSize         int    `json:"chunk_size"`
}

// userSettings is used only without a database; DB-backed requests always read SQL.
var userSettings sync.Map // user_id → immutable *SettingsDTO

// ponytail: serialize no-DB read/merge/write; use per-user locks only if contention matters.
var userSettingsMu sync.Mutex

// settingsGetHandler — GET /settings.
//
// @Summary     Read user settings
// @Description Theme, locale, LLM/embed config, and any backend-stored UI preferences. The WebUI hydrates from this on session start (T9). Falls back to defaults derived from env when the user has nothing persisted.
// @Tags        settings
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} SettingsDTO
// @Router      /settings [get]
func settingsGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)
		defaults := defaultSettings(cfg)
		if cfg.DB != nil && userID != "" {
			ctx, cancel := apiRequestContext(c)
			defer cancel()
			var raw string
			err := cfg.DB.QueryRowContext(ctx, Q("SELECT settings FROM user_settings WHERE user_id = $1"), userID).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				return c.JSON(defaults)
			}
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"detail": "settings read failed"})
			}
			_, settings, err := mergeSettings(defaults, []byte(raw), nil)
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"detail": "stored settings invalid"})
			}
			return c.JSON(settings)
		}
		if cfg.DB == nil && userID != "" {
			if value, ok := userSettings.Load(userID); ok {
				return c.JSON(value)
			}
		}
		return c.JSON(defaults)
	}
}

// settingsPutHandler merges only present fields and returns committed settings.
// Omitted values remain unchanged; an explicit empty default_collection clears it.
//
// @Summary     Update user settings
// @Tags        settings
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body SettingsDTO true "Partial settings; omitted fields remain unchanged"
// @Success     200 {object} SettingsDTO
// @Failure     400 {object} map[string]any "invalid settings"
// @Failure     500 {object} map[string]any "settings persistence failed"
// @Router      /settings [put]
func settingsPutHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)
		var patch map[string]json.RawMessage
		if json.Unmarshal(c.Body(), &patch) != nil || patch == nil || validateSettingsPatch(patch) != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid settings"})
		}
		defaults := defaultSettings(cfg)
		if cfg.DB != nil && userID != "" {
			ctx, cancel := apiRequestContext(c)
			defer cancel()
			settings, err := persistSettingsPatch(ctx, cfg.DB, userID, defaults, patch)
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"detail": "settings persistence failed"})
			}
			return c.JSON(settings)
		}

		userSettingsMu.Lock()
		defer userSettingsMu.Unlock()
		var raw []byte
		if cfg.DB == nil && userID != "" {
			if current, ok := userSettings.Load(userID); ok {
				raw, _ = json.Marshal(current)
			}
		}
		_, settings, err := mergeSettings(defaults, raw, patch)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "stored settings invalid"})
		}
		if cfg.DB == nil && userID != "" {
			userSettings.Store(userID, &settings)
		}
		return c.JSON(settings)
	}
}

func validateSettingsPatch(patch map[string]json.RawMessage) error {
	for key, raw := range patch {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("null setting %s", key)
		}
		switch key {
		case "embedding_dimension", "chunk_size":
			var n int
			if json.Unmarshal(raw, &n) != nil || n <= 0 {
				return fmt.Errorf("invalid positive size %s", key)
			}
		case "theme", "locale", "default_collection", "llm_provider", "llm_model", "llm_endpoint", "llm_api_key",
			"embedding_provider", "embedding_model", "embedding_endpoint", "graph_engine", "graph_url", "graph_database", "vector_engine", "chunk_strategy":
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return fmt.Errorf("invalid string setting %s", key)
			}
			if key == "theme" && value != "light" && value != "dark" && value != "system" {
				return errors.New("invalid theme")
			}
			if key == "locale" && value != "ru" && value != "en" {
				return errors.New("invalid locale")
			}
		default:
			return fmt.Errorf("unknown setting %s", key)
		}
	}
	return nil
}

// Preserve fields written by newer servers in SQL, but keep the existing DTO
// response (including its existing secret handling). Defaults apply only to
// missing fields. Invalid stored objects must not be silently overwritten.
func mergeSettings(defaults SettingsDTO, stored []byte, patch map[string]json.RawMessage) ([]byte, SettingsDTO, error) {
	base, _ := json.Marshal(defaults)
	var merged map[string]json.RawMessage
	_ = json.Unmarshal(base, &merged)
	knownKeys := make(map[string]struct{}, len(merged))
	for key := range merged {
		knownKeys[key] = struct{}{}
	}
	if len(stored) > 0 {
		var current map[string]json.RawMessage
		var dto SettingsDTO
		if json.Unmarshal(stored, &current) != nil || current == nil || json.Unmarshal(stored, &dto) != nil {
			return nil, SettingsDTO{}, errors.New("invalid stored settings")
		}
		for key, value := range current {
			// Null for a known DTO field is corrupt, even though encoding/json
			// would silently accept it into a scalar zero value.
			_, known := merged[key]
			if (known || key == "default_collection" || key == "llm_api_key") && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, SettingsDTO{}, errors.New("invalid stored setting")
			}
			merged[key] = value
		}
	}
	for key, value := range patch {
		merged[key] = value
	}
	known := make(map[string]json.RawMessage, len(knownKeys))
	for key := range knownKeys {
		known[key] = merged[key]
	}
	if err := validateSettingsPatch(known); err != nil {
		return nil, SettingsDTO{}, err
	}
	raw, err := json.Marshal(merged)
	var result SettingsDTO
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return raw, result, err
}

func persistSettingsPatch(ctx context.Context, db *sql.DB, userID string, defaults SettingsDTO, patch map[string]json.RawMessage) (SettingsDTO, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return SettingsDTO{}, err
	}
	defer tx.Rollback()
	initial, _ := json.Marshal(defaults)
	var stored string
	// This no-op UPSERT locks an existing row or creates it before reading.
	// It serializes independent connections on PostgreSQL and obtains SQLite's
	// writer lock without a deferred read-to-write transaction upgrade.
	err = tx.QueryRowContext(ctx, Q(`INSERT INTO user_settings (user_id, settings, updated_at)
		VALUES ($1, $2, NOW()) ON CONFLICT (user_id) DO UPDATE SET user_id = excluded.user_id
		RETURNING settings`), userID, string(initial)).Scan(&stored)
	if err != nil {
		return SettingsDTO{}, err
	}
	raw, result, err := mergeSettings(defaults, []byte(stored), patch)
	if err != nil {
		return SettingsDTO{}, err
	}
	if _, err = tx.ExecContext(ctx, Q(`UPDATE user_settings SET settings=$1, updated_at=NOW() WHERE user_id=$2`), string(raw), userID); err != nil {
		return SettingsDTO{}, err
	}
	if err = tx.Commit(); err != nil {
		return SettingsDTO{}, err
	}
	return result, nil
}

func defaultSettings(cfg APIConfig) SettingsDTO {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	llmProvider := "ollama"
	if llmEndpoint == "" {
		llmProvider = "none"
	}

	embedDim := 1024
	graphEngine := "none"
	if cfg.Neo4jCfg.Neo4jURL != "" {
		graphEngine = "neo4j"
	}

	return SettingsDTO{
		Theme:          "system",
		Locale:         "ru",
		LLMProvider:    llmProvider,
		LLMModel:       llmModel,
		LLMEndpoint:    llmEndpoint,
		EmbedProvider:  "custom",
		EmbedModel:     cfg.EmbedModel,
		EmbedEndpoint:  cfg.EmbedEndpoint,
		EmbedDimension: embedDim,
		GraphEngine:    graphEngine,
		GraphURL:       cfg.Neo4jCfg.Neo4jURL,
		GraphDatabase:  cfg.Neo4jCfg.Neo4jDatabase,
		VectorEngine:   "levara",
		ChunkStrategy:  "merged",
		ChunkSize:      2000,
	}
}
