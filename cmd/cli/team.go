package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Team onboarding is deliberately a local-password API client. It never uses
// the CLI's global/admin token and never manages directory identities or roles.
type teamPlan struct {
	Version           int              `json:"version"`
	ExistingUsersOnly bool             `json:"existing_users_only,omitempty"`
	Users             []teamUser       `json:"users"`
	Datasets          []teamDataset    `json:"datasets"`
	Grants            []teamGrant      `json:"grants"`
	Collections       []teamCollection `json:"collections,omitempty"`
}
type teamUser struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	PasswordEnv string `json:"password_env"`
	KeyName     string `json:"key_name"`
}
type teamDataset struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}
type teamGrant struct {
	Dataset string `json:"dataset"`
	User    string `json:"user"`
	Role    string `json:"role"`
}
type teamCollection struct {
	Name   string `json:"name"`
	User   string `json:"user"`
	Dim    int    `json:"embedding_dim"`
	Model  string `json:"embedding_model"`
	Metric string `json:"distance_metric"`
}
type teamUserState struct {
	ID         string `json:"id"`
	KeyID      string `json:"key_id,omitempty"`
	Key        string `json:"key,omitempty"`
	KeyPending bool   `json:"key_pending,omitempty"`
}
type teamState struct {
	URL         string                    `json:"url"`
	PlanHash    string                    `json:"plan_hash"`
	Users       map[string]*teamUserState `json:"users"`
	Datasets    map[string]string         `json:"datasets"`
	Collections map[string]bool           `json:"collections"`
	Private     map[string]bool           `json:"private_checks"`
}
type teamStep struct {
	Step       string `json:"step"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}
type teamReport struct {
	Status string     `json:"status"`
	Steps  []teamStep `json:"steps"`
}
type teamFailure struct {
	code    string
	status  int
	unknown bool
}

func (e *teamFailure) Error() string { return e.code }
func teamError(code string) error    { return &teamFailure{code: code} }

type teamRunner struct {
	ctx       context.Context
	base      string
	client    *http.Client
	plan      teamPlan
	state     teamState
	statePath string
	tokens    map[string]string
	report    *teamReport
}

func cmdTeam(args []string) {
	report, err := runTeamCommand(baseURL, args)
	_ = json.NewEncoder(os.Stdout).Encode(report)
	if err != nil {
		os.Exit(1)
	}
}

func runTeamCommand(base string, args []string) (teamReport, error) {
	report := teamReport{Status: "failed", Steps: []teamStep{}}
	fail := func(err error) (teamReport, error) {
		report.Steps = append(report.Steps, teamStep{Step: "plan", Status: "failed", Code: err.Error()})
		return report, err
	}
	if len(args) == 0 || args[0] != "apply" {
		return fail(teamError("usage_team_apply"))
	}
	fs := flag.NewFlagSet("team apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // flag errors can contain user-supplied secret values
	planPath := fs.String("plan", "", "JSON plan")
	statePath := fs.String("state", "", "private journal")
	dry := fs.Bool("dry-run", false, "offline validation")
	timeout := fs.Duration("timeout", 2*time.Minute, "total deadline")
	if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *planPath == "" || (!*dry && *statePath == "") || *timeout < 10*time.Millisecond || *timeout > 10*time.Minute {
		return fail(teamError("invalid_flags"))
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.TrimSuffix(u.Path, "/") != "/api/v1" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()) != nil && net.ParseIP(u.Hostname()).IsLoopback()))) {
		return fail(teamError("invalid_api_url"))
	}
	base = strings.TrimSuffix(u.String(), "/")
	var plan teamPlan
	if err := readTeamJSON(*planPath, &plan, false); err != nil {
		return fail(teamError("invalid_plan_file"))
	}
	if err := validateTeamPlan(plan); err != nil {
		return fail(err)
	}
	for _, user := range plan.Users {
		report.Steps = append(report.Steps, teamStep{Step: "user/" + user.Name, Status: "planned"}, teamStep{Step: "key/" + user.Name, Status: "planned"})
	}
	for _, ds := range plan.Datasets {
		report.Steps = append(report.Steps, teamStep{Step: "dataset/" + ds.Name, Status: "planned"})
	}
	for _, grant := range plan.Grants {
		report.Steps = append(report.Steps, teamStep{Step: "grant/" + grant.Dataset + "/" + grant.User, Status: "planned"})
	}
	for _, col := range plan.Collections {
		report.Steps = append(report.Steps, teamStep{Step: "collection/" + col.Name, Status: "planned"})
	}
	if *dry {
		report.Status = "planned"
		return report, nil
	}
	// Validate every secret reference before the first server mutation.
	for _, user := range plan.Users {
		if os.Getenv(user.PasswordEnv) == "" {
			return fail(teamError("password_environment_missing"))
		}
	}
	if filepath.Clean(*planPath) == filepath.Clean(*statePath) {
		return fail(teamError("state_overlaps_plan"))
	}
	if err := os.MkdirAll(filepath.Dir(*statePath), 0700); err != nil {
		return fail(teamError("state_directory_unavailable"))
	}
	lock, err := os.OpenFile(*statePath+".lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fail(teamError("state_locked_or_unavailable"))
	}
	_ = lock.Close()
	defer os.Remove(*statePath + ".lock")
	encoded, _ := json.Marshal(plan)
	hash := sha256.Sum256(encoded)
	state := teamState{URL: base, PlanHash: hex.EncodeToString(hash[:]), Users: map[string]*teamUserState{}, Datasets: map[string]string{}, Collections: map[string]bool{}, Private: map[string]bool{}}
	if _, err := os.Lstat(*statePath); err == nil {
		var existing teamState
		if readTeamJSON(*statePath, &existing, true) != nil {
			return fail(teamError("state_invalid_or_insecure"))
		}
		if existing.URL != state.URL || existing.PlanHash != state.PlanHash || existing.Users == nil || existing.Datasets == nil || existing.Collections == nil || existing.Private == nil {
			return fail(teamError("state_plan_mismatch"))
		}
		state = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(teamError("state_unavailable"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	r := teamRunner{ctx: ctx, base: base, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, plan: plan, state: state, statePath: *statePath, tokens: map[string]string{}, report: &report}
	err = r.apply()
	// These short-lived login sessions are never persisted in the journal.
	for _, user := range plan.Users {
		if bearer := r.tokens[user.Name]; bearer != "" {
			_, _, _ = r.request("POST", "/auth/logout", bearer, "", nil)
		}
	}
	if err != nil {
		report.Status = "partial"
		for i := range report.Steps {
			if report.Steps[i].Status == "planned" {
				report.Steps[i].Status = "blocked"
			}
		}
	} else {
		report.Status = "complete"
	}
	return report, err
}

func validateTeamPlan(p teamPlan) error {
	if p.Version != 1 || len(p.Users) < 2 || len(p.Users) > 8 || len(p.Datasets) == 0 || len(p.Datasets) > 16 || len(p.Grants) > 32 || len(p.Collections) > 16 {
		return teamError("invalid_plan_shape")
	}
	users, emails, datasets, grants, collections := map[string]bool{}, map[string]bool{}, map[string]string{}, map[string]bool{}, map[string]bool{}
	for _, u := range p.Users {
		address, err := mail.ParseAddress(u.Email)
		if !teamIdentifier(u.Name) || !teamIdentifier(u.PasswordEnv) || !teamText(u.KeyName) || !teamText(u.Email) || err != nil || address.Address != u.Email || users[u.Name] || emails[strings.ToLower(u.Email)] {
			return teamError("invalid_or_duplicate_user")
		}
		users[u.Name], emails[strings.ToLower(u.Email)] = true, true
	}
	for _, d := range p.Datasets {
		if !teamText(d.Name) || !users[d.Owner] || datasets[d.Name] != "" {
			return teamError("invalid_or_duplicate_dataset")
		}
		datasets[d.Name] = d.Owner
	}
	for _, g := range p.Grants {
		key := g.Dataset + "\x00" + g.User
		if datasets[g.Dataset] == "" || !users[g.User] || datasets[g.Dataset] == g.User || (g.Role != "viewer" && g.Role != "editor" && g.Role != "admin") || grants[key] {
			return teamError("invalid_or_duplicate_grant")
		}
		grants[key] = true
	}
	for _, c := range p.Collections {
		if !teamText(c.Name) || strings.ContainsAny(c.Name, "/\\") || c.Name == "." || c.Name == ".." || !users[c.User] || c.Dim <= 0 || c.Dim > 65536 || !teamText(c.Model) || (c.Metric != "cosine" && c.Metric != "l2" && c.Metric != "dot") || collections[c.Name] {
			return teamError("invalid_or_duplicate_collection")
		}
		collections[c.Name] = true
	}
	return nil
}
func teamIdentifier(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func teamText(s string) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) != s || len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func readTeamJSON(path string, out any, private bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 || (private && info.Mode().Perm() != 0600) {
		return teamError("invalid_file")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	dec.DisallowUnknownFields()
	if err = dec.Decode(out); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return teamError("trailing_json")
	}
	return nil
}
func (r *teamRunner) save() error {
	body, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return teamError("state_encode_failed")
	}
	f, err := os.CreateTemp(filepath.Dir(r.statePath), ".team-state-*")
	if err != nil {
		return teamError("state_write_failed")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(body); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, r.statePath)
	}
	if err == nil {
		var dir *os.File
		dir, err = os.Open(filepath.Dir(r.statePath))
		if err == nil {
			err = dir.Sync()
			_ = dir.Close()
		}
	}
	if err != nil {
		return teamError("state_write_failed")
	}
	return nil
}
func (r *teamRunner) step(name string, fn func() (string, error)) error {
	status, err := fn()
	entry := teamStep{Step: name, Status: status}
	if err != nil {
		entry.Status = "failed"
		entry.Code = "operation_failed"
		var failure *teamFailure
		if errors.As(err, &failure) {
			entry.Code, entry.HTTPStatus = failure.code, failure.status
			if failure.unknown {
				entry.Status = "unknown"
			}
		}
	}
	for i := range r.report.Steps {
		if r.report.Steps[i].Step == name {
			r.report.Steps[i] = entry
			return err
		}
	}
	r.report.Steps = append(r.report.Steps, entry)
	return err
}
func (r *teamRunner) request(method, path, bearer, key string, payload any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, teamError("invalid_request")
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.ctx, method, r.base+path, body)
	if err != nil {
		return 0, nil, teamError("invalid_request")
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, nil, &teamFailure{code: "request_failed", unknown: method == "POST"}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return resp.StatusCode, nil, &teamFailure{code: "response_unreadable", status: resp.StatusCode, unknown: method == "POST"}
	}
	return resp.StatusCode, b, nil
}
func (r *teamRunner) call(method, path, bearer, key string, payload, out any, want int) error {
	status, body, err := r.request(method, path, bearer, key, payload)
	if err != nil {
		return err
	}
	if status != want {
		return &teamFailure{code: "unexpected_http_status", status: status, unknown: method == "POST" && status >= 500}
	}
	if out != nil {
		if bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
			return &teamFailure{code: "malformed_response", status: status, unknown: method == "POST"}
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		if dec.Decode(out) != nil || dec.Decode(&struct{}{}) != io.EOF {
			return &teamFailure{code: "malformed_response", status: status, unknown: method == "POST"}
		}
	}
	return nil
}

func (r *teamRunner) apply() error {
	if err := r.step("preflight", func() (string, error) {
		if err := r.call("GET", "/datasets", "", "", nil, nil, 401); err != nil {
			return "", err
		}
		return "verified", r.save()
	}); err != nil {
		return err
	}
	for _, u := range r.plan.Users {
		if err := r.step("user/"+u.Name, func() (string, error) { return r.user(u) }); err != nil {
			return err
		}
	}
	for _, u := range r.plan.Users {
		if err := r.step("key/"+u.Name, func() (string, error) { return r.key(u) }); err != nil {
			return err
		}
	}
	for _, d := range r.plan.Datasets {
		if err := r.step("dataset/"+d.Name, func() (string, error) { return r.dataset(d) }); err != nil {
			return err
		}
	}
	for _, d := range r.plan.Datasets {
		// Confirm the known dataset once, rather than charging the owner's
		// request limit again for every other member's negative probe.
		if err := r.step("owner/"+d.Name, func() (string, error) {
			return "verified", r.call("GET", r.dataPath(d.Name), r.tokens[d.Owner], "", nil, nil, 200)
		}); err != nil {
			return err
		}
		for _, u := range r.plan.Users {
			if u.Name == d.Owner {
				continue
			}
			if err := r.step("private/"+d.Name+"/"+u.Name, func() (string, error) { return r.private(d, u) }); err != nil {
				return err
			}
		}
	}
	for _, g := range r.plan.Grants {
		if err := r.step("grant/"+g.Dataset+"/"+g.User, func() (string, error) { return r.grant(g) }); err != nil {
			return err
		}
	}
	for _, c := range r.plan.Collections {
		if err := r.step("collection/"+c.Name, func() (string, error) { return r.collection(c) }); err != nil {
			return err
		}
	}
	for _, d := range r.plan.Datasets {
		for _, u := range r.plan.Users {
			if err := r.step("access/"+d.Name+"/"+u.Name, func() (string, error) { return r.access(d, u) }); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *teamRunner) user(u teamUser) (string, error) {
	payload := map[string]string{"email": u.Email, "password": os.Getenv(u.PasswordEnv)}
	status, body, err := r.request("POST", "/auth/login", "", "", payload)
	if err != nil {
		return "", err
	}
	created := false
	if status == 401 && r.state.Users[u.Name] == nil && !r.plan.ExistingUsersOnly {
		status, body, err = r.request("POST", "/auth/register", "", "", payload)
		if err != nil {
			return "", err
		}
		if status == 201 {
			created = true
		} else if status == 409 {
			// A racing registration is only reconciled by successful exact login.
			status, body, err = r.request("POST", "/auth/login", "", "", payload)
			if err != nil {
				return "", err
			}
		}
	}
	if (!created && status != 200) || (created && status != 201) {
		return "", &teamFailure{code: "user_login_or_registration_failed", status: status}
	}
	var token struct {
		Token string `json:"access_token"`
	}
	if json.Unmarshal(body, &token) != nil || token.Token == "" {
		return "", teamError("malformed_login_response")
	}
	r.tokens[u.Name] = token.Token
	if created {
		// Registration issues a session, but acceptance explicitly verifies login.
		_, _, _ = r.request("POST", "/auth/logout", token.Token, "", nil)
		token.Token = ""
		if err := r.call("POST", "/auth/login", "", "", payload, &token, 200); err != nil {
			return "", err
		}
		if token.Token == "" {
			return "", teamError("malformed_login_response")
		}
		r.tokens[u.Name] = token.Token
	}
	var me struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		Active    bool   `json:"is_active"`
		Superuser bool   `json:"is_superuser"`
	}
	if err := r.call("GET", "/auth/me", token.Token, "", nil, &me, 200); err != nil {
		return "", err
	}
	if me.ID == "" || me.Email != u.Email || !me.Active || me.Superuser {
		return "", teamError("ordinary_active_exact_identity_required")
	}
	previous := r.state.Users[u.Name]
	if previous != nil && previous.ID != me.ID {
		return "", teamError("user_identity_changed")
	}
	if previous == nil {
		r.state.Users[u.Name] = &teamUserState{ID: me.ID}
	}
	if err := r.save(); err != nil {
		return "", err
	}
	if created {
		return "created", nil
	}
	return "reused", nil
}

type teamKeyDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Permissions string `json:"permissions"`
	Revoked     *bool  `json:"revoked"`
}

func (r *teamRunner) key(u teamUser) (string, error) {
	var keys []teamKeyDTO
	if err := r.call("GET", "/auth/keys", r.tokens[u.Name], "", nil, &keys, 200); err != nil {
		return "", err
	}
	s := r.state.Users[u.Name]
	var matches []teamKeyDTO
	for _, k := range keys {
		if k.ID == "" || k.Name == "" || k.Permissions == "" || k.Revoked == nil {
			return "", teamError("malformed_key_list")
		}
		if k.Name == u.KeyName && !*k.Revoked {
			matches = append(matches, k)
		}
	}
	if s.KeyPending {
		return "", &teamFailure{code: "key_creation_unknown_do_not_retry", unknown: true}
	}
	if len(matches) > 1 {
		return "", teamError("ambiguous_existing_key")
	}
	if s.KeyID != "" {
		if len(matches) != 1 || matches[0].ID != s.KeyID || matches[0].Permissions != "read-write" || s.Key == "" {
			return "", teamError("key_changed_or_revoked")
		}
		return "reused", nil
	}
	if len(matches) > 0 {
		return "", teamError("existing_key_secret_unavailable")
	}
	s.KeyPending = true
	if err := r.save(); err != nil {
		return "", err
	}
	var created struct {
		ID          string `json:"id"`
		Key         string `json:"key"`
		Name        string `json:"name"`
		Permissions string `json:"permissions"`
	}
	err := r.call("POST", "/auth/keys", r.tokens[u.Name], "", map[string]string{"name": u.KeyName, "permissions": "read-write"}, &created, 201)
	if err != nil {
		var failure *teamFailure
		status := 0
		if errors.As(err, &failure) {
			status = failure.status
		}
		return "", &teamFailure{code: "key_creation_unknown_do_not_retry", status: status, unknown: true}
	}
	if created.ID == "" || !strings.HasPrefix(created.Key, "lk_") || created.Name != u.KeyName || created.Permissions != "read-write" {
		return "", &teamFailure{code: "key_creation_unknown_do_not_retry", unknown: true}
	}
	s.KeyID, s.Key, s.KeyPending = created.ID, created.Key, false
	if err := r.save(); err != nil {
		return "", &teamFailure{code: "key_created_journal_write_failed", unknown: true}
	}
	return "created", nil
}

type teamDatasetDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner_id"`
}

func (r *teamRunner) dataset(d teamDataset) (string, error) {
	var datasets []teamDatasetDTO
	if err := r.call("GET", "/datasets", r.tokens[d.Owner], "", nil, &datasets, 200); err != nil {
		return "", err
	}
	var found *teamDatasetDTO
	for i := range datasets {
		if datasets[i].ID == "" || datasets[i].Name == "" {
			return "", teamError("malformed_dataset_list")
		}
		if datasets[i].Name == d.Name {
			if found != nil {
				return "", teamError("ambiguous_dataset")
			}
			found = &datasets[i]
		}
	}
	status := "reused"
	if found == nil {
		if r.state.Datasets[d.Name] != "" {
			return "", teamError("dataset_missing_since_previous_apply")
		}
		var created teamDatasetDTO
		if err := r.call("POST", "/datasets", r.tokens[d.Owner], "", map[string]string{"name": d.Name}, &created, 201); err != nil {
			return "", err
		}
		found = &created
		status = "created"
	}
	if found.ID == "" || found.Name != d.Name || found.Owner != r.state.Users[d.Owner].ID {
		return "", teamError("dataset_owner_or_name_conflict")
	}
	if old := r.state.Datasets[d.Name]; old != "" && old != found.ID {
		return "", teamError("dataset_identity_changed")
	}
	r.state.Datasets[d.Name] = found.ID
	return status, r.save()
}
func (r *teamRunner) owner(name string) string {
	for _, d := range r.plan.Datasets {
		if d.Name == name {
			return d.Owner
		}
	}
	return ""
}
func (r *teamRunner) role(dataset, user string) string {
	for _, g := range r.plan.Grants {
		if g.Dataset == dataset && g.User == user {
			return g.Role
		}
	}
	return ""
}
func (r *teamRunner) dataPath(name string) string {
	return "/datasets/" + url.PathEscape(r.state.Datasets[name]) + "/data"
}
func (r *teamRunner) private(d teamDataset, u teamUser) (string, error) {
	key := d.Name + "\x00" + u.Name
	status, _, err := r.request("GET", r.dataPath(d.Name), r.tokens[u.Name], "", nil)
	if err != nil {
		return "", err
	}
	if status == 200 && r.role(d.Name, u.Name) != "" && r.state.Private[key] {
		return "reused", nil
	}
	if status != 403 {
		return "", &teamFailure{code: "private_access_denial_required", status: status}
	}
	r.state.Private[key] = true
	return "verified", r.save()
}

type teamShareDTO struct {
	ID        string `json:"id"`
	DatasetID string `json:"dataset_id"`
	UserID    string `json:"user_id"`
	Role      string `json:"role"`
}

func (r *teamRunner) grant(g teamGrant) (string, error) {
	id := r.state.Datasets[g.Dataset]
	user := r.state.Users[g.User].ID
	bearer := r.tokens[r.owner(g.Dataset)]
	path := "/datasets/" + url.PathEscape(id) + "/shares"
	var shares []teamShareDTO
	if err := r.call("GET", path, bearer, "", nil, &shares, 200); err != nil {
		return "", err
	}
	for _, s := range shares {
		if s.ID == "" || s.DatasetID != id || s.UserID == "" || (s.Role != "viewer" && s.Role != "editor" && s.Role != "admin") {
			return "", teamError("malformed_share_list")
		}
		if s.UserID == user {
			if s.DatasetID != id || s.Role != g.Role || s.ID == "" {
				return "", teamError("existing_grant_conflict")
			}
			return "reused", nil
		}
	}
	var created teamShareDTO
	if err := r.call("POST", path, bearer, "", map[string]string{"user_id": user, "role": g.Role}, &created, 201); err != nil {
		return "", err
	}
	if created.ID == "" || created.DatasetID != id || created.UserID != user || created.Role != g.Role {
		return "", teamError("malformed_grant_response")
	}
	return "created", nil
}
func (r *teamRunner) access(d teamDataset, u teamUser) (string, error) {
	want := 403
	if u.Name == d.Owner || r.role(d.Name, u.Name) != "" {
		want = 200
	}
	for _, auth := range [][2]string{{r.tokens[u.Name], ""}, {"", r.state.Users[u.Name].Key}} {
		if err := r.call("GET", r.dataPath(d.Name), auth[0], auth[1], nil, nil, want); err != nil {
			return "", err
		}
	}
	if r.role(d.Name, u.Name) == "viewer" {
		path := "/datasets/" + url.PathEscape(r.state.Datasets[d.Name]) + "/shares"
		// If a broken server permits this request it changes a grant, so record
		// failure without cleanup or pretending this is a harmless dry-run.
		payload := map[string]string{"user_id": r.state.Users[u.Name].ID, "role": "admin"}
		if err := r.call("POST", path, r.tokens[u.Name], "", payload, nil, 403); err != nil {
			return "", teamError("viewer_could_mutate_grants")
		}
	}
	return "verified", nil
}
func (r *teamRunner) collection(c teamCollection) (string, error) {
	path := "/collections/" + url.PathEscape(c.Name) + "/meta"
	status, body, err := r.request("GET", path, r.tokens[c.User], "", nil)
	if err != nil {
		return "", err
	}
	created := false
	if status == 404 {
		status, body, err = r.request("POST", "/collections", r.tokens[c.User], "", map[string]any{"name": c.Name, "embedding_dim": c.Dim, "embedding_model": c.Model, "distance_metric": c.Metric})
		if err != nil {
			return "", err
		}
		created = true
	}
	if status != 200 && status != 201 {
		return "", &teamFailure{code: "collection_unavailable", status: status}
	}
	var meta struct {
		Name   string `json:"name"`
		Dim    int    `json:"embedding_dim"`
		Model  string `json:"embedding_model"`
		Metric string `json:"distance_metric"`
	}
	if json.Unmarshal(body, &meta) != nil || meta.Name != c.Name || meta.Dim != c.Dim || meta.Model != c.Model || meta.Metric != c.Metric {
		return "", teamError("collection_contract_mismatch")
	}
	if !created && !r.state.Collections[c.Name] {
		return "", teamError("global_collection_not_owned_by_journal")
	}
	r.state.Collections[c.Name] = true
	if err := r.save(); err != nil {
		return "", err
	}
	if created {
		return "created", nil
	}
	return "reused", nil
}
