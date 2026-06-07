package graphdb

import (
	"context"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// RunReadTx executes fn inside a read transaction on the writer's database.
//
// The neo4j-go-driver's managed transactions handle bounded retry on
// transient errors (network blips, leader election, deadlock) automatically
// with exponential backoff up to MaxTransactionRetryTime (default 30s).
// Callers do not need an outer retry loop.
//
// fn may be invoked more than once if the driver retries. It must therefore
// be free of side effects outside the transaction (no caches mutated, no
// counters bumped). Read-only handlers naturally satisfy this.
func RunReadTx[T any](ctx context.Context, w *Writer, fn func(ctx context.Context, tx neo4j.ManagedTransaction) (T, error)) (T, error) {
	var zero T
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)
	out, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		return fn(ctx, tx)
	})
	if err != nil {
		if v, ok := out.(T); ok {
			return v, err
		}
		return zero, err
	}
	if out == nil {
		return zero, nil
	}
	return out.(T), nil
}

// RunWriteTx executes fn inside a write transaction on the writer's database.
//
// Atomicity: every tx.Run inside fn shares the same Neo4j transaction. If
// fn returns a non-nil error, the entire transaction rolls back — partial
// state cannot leak to the database. Use this when one logical operation
// requires multiple Cypher statements (e.g. MERGE nodes followed by MERGE
// edges referencing them).
//
// Bounded retry: see [RunReadTx]. fn must be idempotent because the driver
// will replay it on transient failure.
func RunWriteTx[T any](ctx context.Context, w *Writer, fn func(ctx context.Context, tx neo4j.ManagedTransaction) (T, error)) (T, error) {
	var zero T
	session := w.driver.NewSession(ctx, neo4j.SessionConfig{DatabaseName: w.database})
	defer session.Close(ctx)
	out, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		return fn(ctx, tx)
	})
	if err != nil {
		if v, ok := out.(T); ok {
			return v, err
		}
		return zero, err
	}
	if out == nil {
		return zero, nil
	}
	return out.(T), nil
}
