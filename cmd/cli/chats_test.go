package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stek0v/levara/pkg/chatimport"
)

func TestChatsProjectWorkflow(t *testing.T) {
	oldURL, oldToken, oldSlow := baseURL, token, chatsSlowMode
	t.Cleanup(func() { baseURL, token, chatsSlowMode = oldURL, oldToken, oldSlow })
	token, chatsSlowMode = "chat-test-token", false
	oldTenant := chatsTenant
	t.Cleanup(func() { chatsTenant = oldTenant })
	chatID := "chat/with ?&+unicode-ё"
	project := "project/with &+unicode-ё"
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer chat-test-token" {
			t.Errorf("missing verified-client auth header")
		}
		expectedTenant := map[int]string{1: "a", 2: "a", 3: "b"}[requests]
		if r.Header.Get("X-Tenant-Id") != expectedTenant {
			t.Errorf("tenant header=%q want %q", r.Header.Get("X-Tenant-Id"), expectedTenant)
		}
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 1, 2:
			if r.URL.Path != "/chats/import/sessions/codex/"+chatID+"/project" || r.URL.Query().Get("chat_id") != chatID {
				t.Errorf("association selector corrupted: %s", r.URL.String())
			}
			if requests == 1 {
				var payload map[string]string
				if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&payload) != nil || payload["project_id"] != project || len(payload) != 1 {
					t.Errorf("invalid owner-share request: method=%s payload=%v", r.Method, payload)
				}
			} else if r.Method != http.MethodDelete {
				t.Errorf("unshare method=%s", r.Method)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case 3:
			if r.Method != http.MethodGet || r.URL.Path != "/chats/import/sessions/codex/source /?+ё" || r.URL.Query().Get("chat_id") != chatID {
				t.Errorf("transcript selector corrupted: %s", r.URL.String())
			}
			_, _ = w.Write([]byte(`{"messages":[]}`))
		case 4:
			if r.Method != http.MethodGet || r.URL.Path != "/chats/import/sessions" || r.URL.Query().Get("platform") != "codex" {
				t.Errorf("invalid accessible-session list: %s", r.URL.String())
			}
			_, _ = w.Write([]byte(`{"sessions":[]}`))
		default:
			t.Errorf("unexpected request %d", requests)
		}
	}))
	t.Cleanup(server.Close)
	baseURL = server.URL
	cmdChats([]string{"share", "codex", chatID, "--project=" + project, "--tenant=a"})
	cmdChats([]string{"unshare", "codex", chatID, "--tenant=a"})
	cmdChats([]string{"session", "codex", "source /?+ё", "--chat-id=" + chatID, "--tenant=b"})
	cmdChats([]string{"sessions", "--platform=codex"})
	if requests != 4 {
		t.Fatalf("requests=%d want 4", requests)
	}
}

func TestChatsArtifactTenant(t *testing.T) {
	oldURL, oldToken, oldTenant, oldSlow := baseURL, token, chatsTenant, chatsSlowMode
	t.Cleanup(func() { baseURL, token, chatsTenant, chatsSlowMode = oldURL, oldToken, oldTenant, oldSlow })
	token, chatsSlowMode = "chat-test-token", false
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/add" || r.Header.Get("Authorization") != "Bearer chat-test-token" {
			t.Errorf("invalid artifact/upload request: %s %s", r.Method, r.URL.String())
		}
		switch requests {
		case 1:
			if r.Header.Get("X-Tenant-Id") != "tenant-b" {
				t.Errorf("artifact tenant=%q", r.Header.Get("X-Tenant-Id"))
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("invalid multipart: %v", err)
			} else {
				defer r.MultipartForm.RemoveAll()
				if r.FormValue("datasetName") != "project-dataset" || len(r.MultipartForm.File["data"]) != 1 {
					t.Errorf("artifact fields missing")
				}
			}
		case 2:
			if r.Header.Get("X-Tenant-Id") != "" {
				t.Errorf("ordinary add inherited tenant=%q", r.Header.Get("X-Tenant-Id"))
			}
		default:
			t.Errorf("unexpected request %d", requests)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","items":1,"dataset_name":"project-dataset","dataset_id":"project-id"}`))
	}))
	t.Cleanup(server.Close)
	baseURL, chatsTenant = server.URL, "tenant-b"
	chatsAddArtifact(chatimport.Artifact{FileName: "plan.md", Title: "Plan", Content: "# Plan"}, &chatimport.Conversation{SessionID: "source-session", Platform: chatimport.PlatformCodex}, "project-dataset")
	chatsTenant = ""
	addText("ordinary add", "project-dataset")
	if requests != 2 {
		t.Fatalf("requests=%d want 2", requests)
	}
}
