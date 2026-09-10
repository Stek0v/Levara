package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func cmdDocuments(args []string) {
	if len(args) == 0 {
		fatalf("usage: levara documents [policy|register|recipients|shared|grant|revoke|group-create|group-members] ...")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "policy", "recipients":
		pos := positionalArgs(args)
		if len(pos) != 2 {
			fatalf("usage: levara documents %s <dataset-id> <document-id>", sub)
		}
		path := documentCLIPath(pos[0], pos[1]) + "/" + sub
		documentPrint(http.MethodGet, path, nil)
	case "register":
		pos := positionalArgs(args)
		if len(pos) != 2 {
			fatalf("usage: levara documents register <dataset-id> <document-id> [--mode=restricted] [--tenant=id]")
		}
		payload := map[string]any{"mode": flagValue(args, "--mode", "restricted")}
		if tenant := flagValue(args, "--tenant", ""); tenant != "" {
			payload["tenant_id"] = tenant
		}
		documentPrint(http.MethodPost, documentCLIPath(pos[0], pos[1])+"/policy", payload)
	case "shared":
		limit := flagValue(args, "--limit", "50")
		documentPrint(http.MethodGet, "/documents/shared?limit="+url.QueryEscape(limit), nil)
	case "grant":
		pos := positionalArgs(args)
		if len(pos) != 5 || (pos[2] != "user" && pos[2] != "group") || (pos[4] != "viewer" && pos[4] != "editor" && pos[4] != "admin") {
			fatalf("usage: levara documents grant <dataset-id> <document-id> <user|group> <principal-id> <viewer|editor|admin>")
		}
		revision := documentPolicyRevision(pos[0], pos[1])
		documentPrint(http.MethodPost, documentCLIPath(pos[0], pos[1])+"/grants", map[string]any{
			"acl_revision": revision, "principal_kind": pos[2], "principal_id": pos[3], "role": pos[4],
		})
	case "revoke":
		pos := positionalArgs(args)
		if len(pos) != 4 || (pos[2] != "user" && pos[2] != "group") {
			fatalf("usage: levara documents revoke <dataset-id> <document-id> <user|group> <principal-id>")
		}
		revision := documentPolicyRevision(pos[0], pos[1])
		path := documentCLIPath(pos[0], pos[1]) + "/grants/" + url.PathEscape(pos[2]) + "/" + url.PathEscape(pos[3])
		documentPrint(http.MethodDelete, path, map[string]any{"acl_revision": revision})
	case "group-create":
		name := strings.Join(positionalArgs(args), " ")
		if strings.TrimSpace(name) == "" {
			fatalf("usage: levara documents group-create <name> [--tenant=id]")
		}
		payload := map[string]any{"name": name}
		if tenant := flagValue(args, "--tenant", ""); tenant != "" {
			payload["tenant_id"] = tenant
		}
		documentPrint(http.MethodPost, "/document-groups", payload)
	case "group-members":
		pos := positionalArgs(args)
		if len(pos) != 1 {
			fatalf("usage: levara documents group-members <group-id> [--members=user-id,user-id]")
		}
		groupPath := "/document-groups/" + url.PathEscape(pos[0])
		body, status := documentRequest(http.MethodGet, groupPath, nil)
		if status < 200 || status >= 300 {
			fatalf("document group lookup failed (%d): %s", status, body)
		}
		var group struct {
			Revision int64 `json:"revision"`
		}
		if json.Unmarshal(body, &group) != nil || group.Revision < 1 {
			fatalf("invalid document group response")
		}
		members := splitCSV(flagValue(args, "--members", ""))
		if members == nil {
			members = []string{}
		}
		documentPrint(http.MethodPut, groupPath+"/members", map[string]any{
			"revision": group.Revision, "members": members,
		})
	default:
		fatalf("unknown documents subcommand: %s", sub)
	}
}

func documentCLIPath(datasetID, documentID string) string {
	return "/datasets/" + url.PathEscape(datasetID) + "/data/" + url.PathEscape(documentID)
}

func documentPolicyRevision(datasetID, documentID string) int64 {
	body, status := documentRequest(http.MethodGet, documentCLIPath(datasetID, documentID)+"/policy", nil)
	if status < 200 || status >= 300 {
		fatalf("document policy lookup failed (%d): %s", status, body)
	}
	var policy struct {
		ACLRevision int64 `json:"acl_revision"`
	}
	if json.Unmarshal(body, &policy) != nil || policy.ACLRevision < 1 {
		fatalf("invalid document policy response")
	}
	return policy.ACLRevision
}

func documentPrint(method, path string, payload map[string]any) {
	body, status := documentRequest(method, path, payload)
	if status < 200 || status >= 300 {
		fatalf("document operation failed (%d): %s", status, body)
	}
	printJSON(body)
}

func documentRequest(method, path string, payload map[string]any) ([]byte, int) {
	var body io.Reader
	if payload != nil {
		data, _ := json.Marshal(payload)
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, baseURL+path, body)
	if err != nil {
		fatalf("create request: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	applyAuth(req)
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		fatalf("connection failed: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		fatalf("read response: %v", err)
	}
	return data, resp.StatusCode
}
