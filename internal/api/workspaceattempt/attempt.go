// attempt.go — workspace generation-attempt sidecar protocol.
//
// Extracted verbatim from internal/http/workspace_generation_files.go
// (Phase-2 workspace campaign continuation). One retained attempt per
// branch: callers hold the native project lock. The sidecar records an
// in-flight generation publication so a crashed run's orphan vectors can
// be identified and cleaned by attempt identity on the next attempt.
package workspaceattempt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/stek0v/levara/internal/api/workspacesvc"
	"github.com/stek0v/levara/pkg/vectorstore"
	"github.com/stek0v/levara/pkg/workspace"
)

// Attempt is the sidecar payload identifying one in-flight publication.
type Attempt struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	Branch     string `json:"branch"`
	Generation string `json:"generation"`
	Collection string `json:"collection"`
}

// Path renders the attempt sidecar location for a project/branch.
func Path(root, projectID, branch string) string {
	return workspacesvc.ManifestPath(root, projectID, branch) + ".attempt.json"
}

// Write persists a new attempt sidecar.
func Write(ctx context.Context, root string, attempt Attempt) error {
	raw, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	return workspacesvc.WriteFile(ctx, root, Path(root, attempt.ProjectID, attempt.Branch), raw)
}

// Remove deletes the attempt sidecar; absence is success.
func Remove(root, projectID, branch string) error {
	dir, name, err := workspacesvc.OpenFileParent(root, Path(root, projectID, branch), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	err = dir.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Recover reads the sidecar (if any), validates its identity against the
// manifest, and deletes orphan vectors whose metadata carries this
// attempt's identity but which the manifest does not own. Callers hold
// the native project lock.
func Recover(ctx context.Context, root string, manifest *workspace.Manifest, store vectorstore.VectorStore) error {
	data, err := workspacesvc.ReadFile(root, Path(root, manifest.ProjectID, manifest.Branch))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var attempt Attempt
	if err := json.Unmarshal(data, &attempt); err != nil {
		return err
	}
	if _, err := uuid.Parse(attempt.ID); err != nil {
		return errors.New("invalid workspace attempt identity")
	}
	if attempt.ProjectID != manifest.ProjectID || attempt.Branch != manifest.Branch || attempt.Generation == "" || attempt.Collection == "" {
		return errors.New("workspace attempt target mismatch")
	}
	if err := ValidateCollection(attempt.Collection); err != nil {
		return err
	}
	if store.Has(attempt.Collection) {
		records, err := store.Scan(attempt.Collection)
		if err != nil {
			return err
		}
		var ids []string
		for _, record := range records {
			var metadata map[string]any
			if err := json.Unmarshal(record.Metadata, &metadata); err != nil {
				continue
			}
			if metadata["attempt_id"] != attempt.ID || metadata["project_id"] != attempt.ProjectID || metadata["branch"] != attempt.Branch || metadata["generation"] != attempt.Generation {
				continue
			}
			if current, ok := manifest.Chunks[record.ID]; ok && current.Collection == attempt.Collection {
				continue
			}
			ids = append(ids, record.ID)
		}
		if len(ids) > 0 {
			if errs := store.DeleteMany(attempt.Collection, ids); len(errs) > 0 {
				return fmt.Errorf("workspace attempt cleanup: %v", errs)
			}
		}
	}
	return Remove(root, manifest.ProjectID, manifest.Branch)
}

// ValidateCollection rejects empty collection names. The full validation
// against the embedding contract stays with the http caller.
func ValidateCollection(collection string) error {
	if collection == "" {
		return errors.New("workspace collection required")
	}
	return nil
}
