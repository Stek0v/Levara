package http

import "testing"

func TestSyncMemoryGenerationSchemaInventory(t *testing.T) {
	inventory := SchemaInventory()
	for _, provider := range []DBProvider{DBSQLite, DBPostgres} {
		t.Run(string(provider), func(t *testing.T) {
			for _, expected := range []struct{ kind, name string }{
				{SchemaTable, "memory_sync_heads"},
				{SchemaTable, "memory_sync_incarnations"},
				{SchemaTable, "memory_sync_aliases"},
				{SchemaIndex, "idx_memory_sync_incarnations_scope"},
				{SchemaIndex, "idx_memory_sync_incarnations_unresolved"},
			} {
				count := 0
				for _, object := range inventory {
					if object.Provider == string(provider) && object.Kind == expected.kind && object.Name == expected.name {
						count++
					}
				}
				if count != 1 {
					t.Errorf("inventory %s %s: count=%d, want exactly one", expected.kind, expected.name, count)
				}
			}
		})
	}
}
