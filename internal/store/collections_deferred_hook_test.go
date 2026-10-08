package store

import "testing"

func TestCollectionInsertDeferredHookPreservesNativeContract(t *testing.T) {
	cm, err := NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	calls := 0
	cm.SetAfterInsertHook(func(collection, id string, meta any) {
		calls++
		if collection != "source" || !cm.HasRecord(collection, id) {
			t.Fatal("hook ran without native publication")
		}
	})
	hook, err := cm.InsertDeferredHook("source", "deferred", []float32{1, 0}, map[string]any{"text": "value"})
	if err != nil || hook == nil || calls != 0 || !cm.HasRecord("source", "deferred") {
		t.Fatalf("deferred native effect: hook nil=%t calls=%d err=%v", hook == nil, calls, err)
	}
	hook()
	if calls != 1 {
		t.Fatal("deferred callback was lost")
	}
	if err = cm.Insert("source", "immediate", []float32{0, 1}, nil); err != nil || calls != 2 {
		t.Fatalf("existing synchronous API changed: calls=%d err=%v", calls, err)
	}
	if hook, err = cm.InsertDeferredHook("source", "wrong-dimension", []float32{1}, nil); err == nil || hook != nil || calls != 2 || cm.HasRecord("source", "wrong-dimension") {
		t.Fatalf("invalid native publication reached callback: hook nil=%t calls=%d err=%v", hook == nil, calls, err)
	}
}
