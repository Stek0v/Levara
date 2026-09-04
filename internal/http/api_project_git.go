package http

import (
	"log"
	"time"

	"github.com/gofiber/fiber/v2"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/git"
)

// GitHub/repo binding for projects (block ④ of the WebUI plan).
// The git tooling works against local repo paths on the server
// (pkg/git.ParseLog) — there is no GitHub REST client in the tree, so
// "github binding" v1 = a stored repo path + a commit feed endpoint.

type CommitDTO struct {
	Hash    string   `json:"hash"`
	Author  string   `json:"author"`
	Date    string   `json:"date"`
	Message string   `json:"message"`
	Files   []string `json:"files"`
}

// datasetSetRepoHandler — PATCH /datasets/:id { "github_repo": "..." }.
// Requires admin on the dataset (owner or admin share).
func datasetSetRepoHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dsID := c.Params("id")
		var req struct {
			GitHubRepo string `json:"github_repo"`
		}
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid body"})
		}

		if err := authorizeDatasetFiber(c, cfg, dsID, accesspkg.ActionShare); err != nil {
			return err
		}

		if _, err := cfg.DB.ExecContext(ctx,
			Q(`UPDATE datasets SET github_repo = $1, updated_at = $2 WHERE id = $3`),
			req.GitHubRepo, time.Now().UTC(), dsID); err != nil {
			log.Printf("[datasets] set repo ds=%s: %v", dsID, err)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "update failed"})
		}
		return c.JSON(fiber.Map{"id": dsID, "github_repo": req.GitHubRepo})
	}
}

// datasetCommitsHandler — GET /datasets/:id/commits?limit=50.
// Returns the commit feed of the bound repository. A missing binding is
// an empty list; an invalid path is a 400 with the parse error text.
func datasetCommitsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dsID := c.Params("id")
		if err := authorizeDatasetFiber(c, cfg, dsID, accesspkg.ActionRead); err != nil {
			return err
		}

		var repo string
		if err := cfg.DB.QueryRowContext(ctx,
			Q(`SELECT COALESCE(github_repo, '') FROM datasets WHERE id = $1`), dsID).Scan(&repo); err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"detail": "dataset not found"})
		}
		if repo == "" {
			return c.JSON([]CommitDTO{})
		}

		limit := c.QueryInt("limit", 50)
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		commits, err := git.ParseLog(repo, "", limit)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"detail": err.Error()})
		}

		out := make([]CommitDTO, 0, len(commits))
		for _, cm := range commits {
			out = append(out, CommitDTO{
				Hash: cm.Hash, Author: cm.Author,
				Date: cm.Date.Format("2006-01-02 15:04"), Message: cm.Message,
				Files: cm.Files,
			})
		}
		return c.JSON(out)
	}
}
