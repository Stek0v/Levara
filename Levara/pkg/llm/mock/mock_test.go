package mock

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stek0v/cognevra/pkg/llm"
)

func TestMock_SubstringMatchWinsOverAny(t *testing.T) {
	m := New().
		On("alpha").Reply("got-alpha").
		OnAny().Reply("got-default")
	p := m.Provider()

	resp, err := p.ChatCompletion(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: "user", Content: "please alpha now"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "got-alpha" {
		t.Errorf("want got-alpha, got %q", resp.Content)
	}
}

func TestMock_OnAnyFallback(t *testing.T) {
	m := New().OnAny().Reply("fallback")
	resp, err := m.Provider().ChatCompletion(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: "user", Content: "anything"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "fallback" {
		t.Errorf("want fallback, got %q", resp.Content)
	}
}

func TestMock_UnmatchedReturnsErrorNotZero(t *testing.T) {
	// Loud failure contract: no silent zero responses that would mask test bugs.
	m := New().On("xxx").Reply("nope")
	_, err := m.Provider().ChatCompletion(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: "user", Content: "unrelated"}},
	})
	if err == nil || !strings.Contains(err.Error(), "no matching rule") {
		t.Fatalf("want 'no matching rule' err, got %v", err)
	}
}

func TestMock_FailRuleReturnsErr(t *testing.T) {
	boom := errors.New("boom")
	m := New().OnAny().Fail(boom)
	_, err := m.Provider().ChatCompletion(context.Background(), llm.CompletionRequest{
		Messages: []llm.Message{{Role: "user", Content: "x"}},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
}

func TestMock_CallsRecorded(t *testing.T) {
	m := New().OnAny().Reply("ok")
	p := m.Provider()
	for i := 0; i < 3; i++ {
		_, _ = p.ChatCompletion(context.Background(), llm.CompletionRequest{
			Messages: []llm.Message{{Role: "user", Content: "hello"}},
		})
	}
	if got := len(m.Calls()); got != 3 {
		t.Errorf("want 3 recorded calls, got %d", got)
	}
}

func TestMock_CtxCancelled(t *testing.T) {
	m := New().OnAny().Reply("ok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.Provider().ChatCompletion(ctx, llm.CompletionRequest{
		Messages: []llm.Message{{Role: "user", Content: "x"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}
