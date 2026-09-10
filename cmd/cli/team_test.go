package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func teamTestPlan() teamPlan {
	return teamPlan{Version: 1, Users: []teamUser{{Name: "alice", Email: "алиса@example.invalid", PasswordEnv: "LEVARA_TEAM_TEST_A", KeyName: "CLI Алиса"}, {Name: "bob", Email: "bob@example.invalid", PasswordEnv: "LEVARA_TEAM_TEST_B", KeyName: "CLI Bob"}}, Datasets: []teamDataset{{Name: "Общий & # / набор", Owner: "alice"}, {Name: "Private B", Owner: "bob"}}, Grants: []teamGrant{{Dataset: "Общий & # / набор", User: "bob", Role: "viewer"}}}
}
func writeTeamPlan(t *testing.T, p teamPlan) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.json")
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestTeamDryRunIsOfflineAndDispatches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected", 500) }))
	defer server.Close()
	plan := writeTeamPlan(t, teamTestPlan())
	state := filepath.Join(t.TempDir(), "state.json")
	out, err := runCLICommand(t, server.URL, "team", "apply", "--plan="+plan, "--state="+state, "--dry-run")
	if err != nil || !strings.Contains(out, `"status":"planned"`) {
		t.Fatalf("dry-run: %v %s", err, out)
	}
	if calls.Load() != 0 {
		t.Fatal("dry-run contacted server")
	}
	if _, err = os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote journal")
	}
	if strings.Contains(out, "isolated-cli-token") {
		t.Fatal("global token leaked")
	}
}
func TestTeamPlanRejectsAmbiguousIdentitiesAndReferences(t *testing.T) {
	for _, kind := range []string{"duplicate-email", "case-duplicate-email", "duplicate-name", "control-character", "unknown-owner", "duplicate-grant", "unknown-field"} {
		t.Run(kind, func(t *testing.T) {
			p := teamTestPlan()
			switch kind {
			case "duplicate-email":
				p.Users[1].Email = p.Users[0].Email
			case "case-duplicate-email":
				p.Users[1].Email = strings.ToUpper(p.Users[0].Email)
			case "duplicate-name":
				p.Users[1].Name = p.Users[0].Name
			case "control-character":
				p.Datasets[0].Name = "bad\nname"
			case "unknown-owner":
				p.Datasets[0].Owner = "missing"
			case "duplicate-grant":
				p.Grants = append(p.Grants, p.Grants[0])
			}
			path := writeTeamPlan(t, p)
			if kind == "unknown-field" {
				os.WriteFile(path, []byte(`{"version":1,"admin_token":"must-never-print"}`), 0600)
			}
			report, err := runTeamCommand("http://127.0.0.1:1/api/v1", []string{"apply", "--dry-run", "--plan=" + path})
			b, _ := json.Marshal(report)
			if err == nil || strings.Contains(string(b), "must-never-print") {
				t.Fatalf("invalid plan: %v %s", err, b)
			}
		})
	}
}

func teamKeyFixture(t *testing.T, keyResponse func(http.ResponseWriter, *http.Request), keyList ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "isolated-cli-token") {
			t.Error("team reused administrator/global token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/datasets":
			w.WriteHeader(401)
		case "/api/v1/auth/login":
			var p map[string]string
			json.NewDecoder(r.Body).Decode(&p)
			name := "bob"
			if p["email"] == "алиса@example.invalid" {
				name = "alice"
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "jwt-" + name})
		case "/api/v1/auth/me":
			name := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer jwt-")
			email := "bob@example.invalid"
			if name == "alice" {
				email = "алиса@example.invalid"
			}
			json.NewEncoder(w).Encode(map[string]any{"id": "id-" + name, "email": email, "is_active": true, "is_superuser": false})
		case "/api/v1/auth/logout":
			w.WriteHeader(204)
		case "/api/v1/auth/keys":
			if r.Method == "GET" {
				if len(keyList) > 0 {
					w.Write([]byte(keyList[0]))
				} else {
					json.NewEncoder(w).Encode([]teamKeyDTO{})
				}
				return
			}
			creates.Add(1)
			keyResponse(w, r)
		default:
			http.Error(w, "unexpected", 500)
		}
	}))
	t.Cleanup(server.Close)
	return server, &creates
}

func TestTeamMalformedListsAndDeadlineStopBeforeMutation(t *testing.T) {
	t.Setenv("LEVARA_TEAM_TEST_A", "fixture")
	t.Setenv("LEVARA_TEAM_TEST_B", "fixture")
	for _, body := range []string{"null", "[{}]", `[{"id":"x","name":"CLI Алиса","permissions":"read-write"}]`, strings.Repeat("x", (1<<20)+1)} {
		t.Run(fmt.Sprint(len(body)), func(t *testing.T) {
			server, creates := teamKeyFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("malformed list caused key creation") }, body)
			plan := writeTeamPlan(t, teamTestPlan())
			state := filepath.Join(t.TempDir(), "state.json")
			_, err := runTeamCommand(server.URL+"/api/v1", []string{"apply", "--plan=" + plan, "--state=" + state})
			if err == nil || creates.Load() != 0 {
				t.Fatalf("malformed success accepted: %v", err)
			}
		})
	}
	t.Run("deadline", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer server.Close()
		plan := writeTeamPlan(t, teamTestPlan())
		state := filepath.Join(t.TempDir(), "state.json")
		start := time.Now()
		_, err := runTeamCommand(server.URL+"/api/v1", []string{"apply", "--plan=" + plan, "--state=" + state, "--timeout=20ms"})
		if err == nil || time.Since(start) > time.Second {
			t.Fatalf("deadline ignored: %v in %v", err, time.Since(start))
		}
	})
}

func TestTeamExistingUsersOnlyDoesNotRegister(t *testing.T) {
	t.Setenv("LEVARA_TEAM_TEST_A", "fixture")
	t.Setenv("LEVARA_TEAM_TEST_B", "fixture")
	var registers atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/register") {
			registers.Add(1)
		}
		w.WriteHeader(401)
	}))
	defer server.Close()
	p := teamTestPlan()
	p.ExistingUsersOnly = true
	plan := writeTeamPlan(t, p)
	state := filepath.Join(t.TempDir(), "state.json")
	if _, err := runTeamCommand(server.URL+"/api/v1", []string{"apply", "--plan=" + plan, "--state=" + state}); err == nil || registers.Load() != 0 {
		t.Fatalf("existing-users-only registered account: %v", err)
	}
}

func TestTeamOptionalCollectionRequiresActualContract(t *testing.T) {
	var creates, dimension atomic.Int32
	dimension.Store(768)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer per-user-fixture" {
			t.Error("collection used wrong credential")
		}
		if r.Method == "GET" && creates.Load() == 0 {
			w.WriteHeader(404)
			return
		}
		if r.Method == "POST" {
			creates.Add(1)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 4 || body["embedding_dim"] != float64(768) || body["embedding_model"] != "fixture-model" {
				t.Errorf("collection body=%v", body)
			}
			w.WriteHeader(201)
		}
		json.NewEncoder(w).Encode(map[string]any{"name": "team-index", "embedding_dim": dimension.Load(), "embedding_model": "fixture-model", "distance_metric": "cosine"})
	}))
	defer server.Close()
	r := teamRunner{ctx: context.Background(), base: server.URL, client: server.Client(), tokens: map[string]string{"alice": "per-user-fixture"}, state: teamState{Collections: map[string]bool{}}, statePath: filepath.Join(t.TempDir(), "state.json")}
	c := teamCollection{Name: "team-index", User: "alice", Dim: 768, Model: "fixture-model", Metric: "cosine"}
	for _, want := range []string{"created", "reused"} {
		if status, err := r.collection(c); err != nil || status != want {
			t.Fatalf("collection %s: %s %v", want, status, err)
		}
	}
	dimension.Store(1024)
	if _, err := r.collection(c); err == nil {
		t.Fatal("actual dimension mismatch silently accepted")
	}
	dimension.Store(768)
	r.state.Collections = map[string]bool{}
	if _, err := r.collection(c); err == nil {
		t.Fatal("unrecorded global collection adopted")
	}
	if creates.Load() != 1 {
		t.Fatalf("collection created %d times", creates.Load())
	}
}
func TestTeamLostKeyResponseNeverCreatesDuplicateOnResume(t *testing.T) {
	t.Setenv("LEVARA_TEAM_TEST_A", "password-A-canary")
	t.Setenv("LEVARA_TEAM_TEST_B", "password-B-canary")
	server, creates := teamKeyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		w.Write([]byte(`{"key":"secret-response-canary","malformed":`))
	})
	plan := writeTeamPlan(t, teamTestPlan())
	state := filepath.Join(t.TempDir(), "state.json")
	for range 2 {
		out, err := runCLICommand(t, server.URL, "team", "apply", "--plan="+plan, "--state="+state)
		if err == nil || !strings.Contains(out, "key_creation_unknown_do_not_retry") {
			t.Fatalf("lost key: %v %s", err, out)
		}
		for _, secret := range []string{"password-A-canary", "password-B-canary", "secret-response-canary", "jwt-alice", "isolated-cli-token"} {
			if strings.Contains(out, secret) {
				t.Fatalf("secret leaked: %s", secret)
			}
		}
	}
	if creates.Load() != 1 {
		t.Fatalf("uncertain key was created %d times", creates.Load())
	}
	info, err := os.Stat(state)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions: %v %v", info, err)
	}
	var journal teamState
	if readTeamJSON(state, &journal, true) != nil || !journal.Users["alice"].KeyPending {
		t.Fatal("unknown creation not persisted")
	}
}
func TestTeamRedirectAndURLSecretsNeverLeaveClient(t *testing.T) {
	t.Setenv("LEVARA_TEAM_TEST_A", "password-A-canary")
	t.Setenv("LEVARA_TEAM_TEST_B", "password-B-canary")
	var redirected atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer sink.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/datasets" {
			w.WriteHeader(401)
			return
		}
		http.Redirect(w, r, sink.URL, 307)
	}))
	defer source.Close()
	plan := writeTeamPlan(t, teamTestPlan())
	state := filepath.Join(t.TempDir(), "state.json")
	report, err := runTeamCommand(source.URL+"/api/v1", []string{"apply", "--plan=" + plan, "--state=" + state})
	if err == nil || redirected.Load() != 0 {
		t.Fatalf("redirect followed: %v %#v", err, report)
	}
	for _, base := range []string{"http://user:URL-secret@127.0.0.1/api/v1", "http://example.invalid/api/v1", "https://example.invalid/api/v1?token=URL-secret"} {
		report, err := runTeamCommand(base, []string{"apply", "--plan=" + plan, "--dry-run"})
		b, _ := json.Marshal(report)
		if err == nil || strings.Contains(string(b), "URL-secret") {
			t.Fatalf("invalid URL leaked/accepted: %v %s", err, b)
		}
	}
}
func TestTeamJournalRejectsInsecureFilesAndConcurrentApply(t *testing.T) {
	t.Setenv("LEVARA_TEAM_TEST_A", "fixture")
	t.Setenv("LEVARA_TEAM_TEST_B", "fixture")
	for _, kind := range []string{"permissions", "symlink", "lock", "different-plan"} {
		t.Run(kind, func(t *testing.T) {
			plan := writeTeamPlan(t, teamTestPlan())
			state := filepath.Join(t.TempDir(), "state.json")
			switch kind {
			case "permissions":
				os.WriteFile(state, []byte(`{}`), 0644)
			case "symlink":
				os.Symlink(plan, state)
			case "lock":
				os.WriteFile(state+".lock", nil, 0600)
			case "different-plan":
				os.WriteFile(state, []byte(`{"url":"http://wrong/api/v1","plan_hash":"wrong"}`), 0600)
			}
			report, err := runTeamCommand("http://127.0.0.1:1/api/v1", []string{"apply", "--plan=" + plan, "--state=" + state})
			if err == nil || report.Status != "failed" {
				t.Fatalf("unsafe journal accepted: %v %#v", err, report)
			}
		})
	}
}
