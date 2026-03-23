// settings.go — Runtime settings API for React frontend.
// GET /settings — read current config
// PUT /settings — update config (per-user if DB available, else global env)
package http

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/gofiber/fiber/v2"
)

// SettingsDTO matches Cognee frontend expected format.
type SettingsDTO struct {
	LLMProvider    string `json:"llm_provider"`
	LLMModel       string `json:"llm_model"`
	LLMEndpoint    string `json:"llm_endpoint"`
	LLMAPIKey      string `json:"llm_api_key,omitempty"`
	EmbedProvider  string `json:"embedding_provider"`
	EmbedModel     string `json:"embedding_model"`
	EmbedEndpoint  string `json:"embedding_endpoint"`
	EmbedDimension int    `json:"embedding_dimension"`
	GraphEngine    string `json:"graph_engine"`
	GraphURL       string `json:"graph_url"`
	GraphDatabase  string `json:"graph_database"`
	VectorEngine   string `json:"vector_engine"`
	ChunkStrategy  string `json:"chunk_strategy"`
	ChunkSize      int    `json:"chunk_size"`
}

// userSettings stores per-user overrides (in-memory, keyed by user_id).
var userSettings sync.Map // user_id → *SettingsDTO

func settingsGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)

		// Check per-user override first
		if userID != "" {
			if val, ok := userSettings.Load(userID); ok {
				return c.JSON(val)
			}
		}

		// Check DB for persisted settings
		if cfg.DB != nil && userID != "" {
			var data string
			err := cfg.DB.QueryRowContext(c.Context(),
				Q("SELECT settings FROM user_settings WHERE user_id = $1"), userID).Scan(&data)
			if err == nil && data != "" {
				var s SettingsDTO
				if json.Unmarshal([]byte(data), &s) == nil {
					userSettings.Store(userID, &s)
					return c.JSON(s)
				}
			}
		}

		// Default: build from env vars / config
		return c.JSON(defaultSettings(cfg))
	}
}

func settingsPutHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)

		var req SettingsDTO
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid settings"})
		}

		// Fill defaults for empty fields
		defaults := defaultSettings(cfg)
		if req.LLMProvider == "" {
			req.LLMProvider = defaults.LLMProvider
		}
		if req.LLMModel == "" {
			req.LLMModel = defaults.LLMModel
		}
		if req.EmbedModel == "" {
			req.EmbedModel = defaults.EmbedModel
		}
		if req.EmbedDimension == 0 {
			req.EmbedDimension = defaults.EmbedDimension
		}
		if req.ChunkStrategy == "" {
			req.ChunkStrategy = defaults.ChunkStrategy
		}
		if req.ChunkSize == 0 {
			req.ChunkSize = defaults.ChunkSize
		}
		if req.VectorEngine == "" {
			req.VectorEngine = defaults.VectorEngine
		}

		// Store in memory
		if userID != "" {
			userSettings.Store(userID, &req)
		}

		// Persist to DB if available
		if cfg.DB != nil && userID != "" {
			data, _ := json.Marshal(req)
			upsertSQL, upsertArgs := QArgs(`INSERT INTO user_settings (user_id, settings, updated_at)
				 VALUES ($1, $2, NOW())
				 ON CONFLICT (user_id) DO UPDATE SET settings = $2, updated_at = NOW()`,
				userID, string(data))
			cfg.DB.ExecContext(c.Context(), upsertSQL, upsertArgs...)
		}

		return c.JSON(req)
	}
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
