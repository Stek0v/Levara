package access

import (
	"context"

	"github.com/stek0v/levara/pkg/workspace"
)

// AuthorizeArtifactLocation resolves server-owned source references before a
// verifier reads bytes. Knowing a storage key or digest is not a document grant.
func (p SQLPolicy) AuthorizeArtifactLocation(ctx context.Context, actor Actor, location string) (bool, error) {
	rows, err := p.reader().QueryContext(ctx, p.rewrite(`SELECT dd.dataset_id,dd.data_id FROM data d
 JOIN dataset_data dd ON dd.data_id=d.id WHERE d.raw_data_location=$1 OR d.original_data_location=$2 LIMIT 257`), location, location)
	if err != nil {
		return false, err
	}
	var refs []DocumentRef
	for rows.Next() {
		var ref DocumentRef
		if err := rows.Scan(&ref.DatasetID, &ref.DataID); err != nil {
			rows.Close()
			return false, err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(refs) > 256 {
		return false, ErrDocumentInvalid
	}
	for _, ref := range refs {
		d, err := p.AuthorizeDocument(ctx, actor, ref, ActionRead)
		if err != nil {
			return false, err
		}
		if d.Allowed {
			return true, nil
		}
	}
	return false, nil
}

// Workspace directories use a lossy identifier. Resolve exactly one persisted
// project; a colliding directory can never acquire another project's grant.
func (p SQLPolicy) AuthorizeWorkspaceDirectory(ctx context.Context, actor Actor, directory string) (bool, error) {
	rows, err := p.reader().QueryContext(ctx, `SELECT id FROM datasets ORDER BY id LIMIT 10001`)
	if err != nil {
		return false, err
	}
	project, count, matches := "", 0, 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		count++
		if workspace.SafeID(id) == directory {
			project, matches = id, matches+1
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	// ponytail: bounded directory resolution; store canonical IDs if >10k projects need this path.
	if count > 10000 || matches != 1 {
		return false, nil
	}
	d, err := p.AuthorizeDataset(ctx, actor, project, ActionRead)
	return d.Allowed, err
}
