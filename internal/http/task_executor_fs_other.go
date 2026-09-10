//go:build !darwin && !linux

package http

import (
	"context"
	"errors"
	"github.com/stek0v/levara/pkg/mcp"
)

func taskWorkspaceFile(ctx context.Context, root string, e *mcp.TaskExecution, rel string, write []byte, expected *string) ([]byte, error) {
	return nil, errors.New("task workspace confinement is supported only on Darwin and Linux")
}

func readConfinedArtifact(ctx context.Context, root, relative string) ([]byte, error) {
	return nil, errors.New("confined artifact reads require Linux or macOS")
}
