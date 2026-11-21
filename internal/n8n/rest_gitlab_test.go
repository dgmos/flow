package n8n

import (
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestGitlabNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.method == "GET" && r.path == "/projects/acme/web/repository/files/docs/a.md":
			return 200, "application/json", map[string]interface{}{"file_name": "a.md", "file_path": "docs/a.md", "content": base64.StdEncoding.EncodeToString([]byte("# Hi"))}
		case r.method == "PUT" && r.path == "/projects/acme/web/repository/files/docs/a.md":
			return 200, "application/json", map[string]interface{}{"file_path": "docs/a.md", "branch": "main"}
		case r.method == "POST" && r.path == "/projects/acme/web/issues":
			return 201, "application/json", map[string]interface{}{"iid": 3}
		case r.method == "GET" && r.path == "/projects/acme/web/issues":
			if strings.Contains(r.query, "page=2") {
				return 200, "application/json", []interface{}{map[string]interface{}{"iid": 3}}
			}
			return 200, "application/json", []interface{}{map[string]interface{}{"iid": 1}, map[string]interface{}{"iid": 2}}
		case r.method == "GET" && r.path == "/projects/acme/web":
			return 200, "application/json", map[string]interface{}{"id": 42}
		case r.method == "GET" && r.path == "/projects/42/releases":
			return 200, "application/json", []interface{}{map[string]interface{}{"tag_name": "v2"}, map[string]interface{}{"tag_name": "v1"}}
		}
		return 404, "application/json", map[string]interface{}{"message": r.path}
	})
	var mu sync.Mutex
	raw := map[string]string{}
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		raw[r.Method+" "+r.URL.Path] = r.URL.EscapedPath()
		mu.Unlock()
		if r.URL.Path == "/projects/acme/web/issues" && !strings.Contains(r.URL.RawQuery, "page=2") {
			w.Header().Set("Link", `<http://x/issues?page=2>; rel="next"`)
		}
		inner.ServeHTTP(w, r)
	})
	token := headerSpy(f, "Private-Token")
	creds := map[string]map[string]interface{}{"gitlabApi": {"server": "https://gitlab.example.com/", "accessToken": "glpat-1"}}
	typ := "n8n-nodes-base.gitlab"

	out := communityRun(t, f, "gitlab", typ, 1, map[string]interface{}{"resource": "file", "operation": "get", "owner": "acme", "repository": "web",
		"filePath": "docs/a.md", "additionalParameters": map[string]interface{}{"reference": "main"}}, creds, "", "")
	bin := asMap(out[0].Binary["data"])
	if d, _ := base64.StdEncoding.DecodeString(toString(bin["data"])); string(d) != "# Hi" || bin["fileName"] != "a.md" {
		t.Fatalf("file get: %v", out)
	}
	if q := f.find(t, "GET", "/repository/files/").query; q != "ref=main" || token("/projects/acme/web/repository/files/docs/a.md") != "glpat-1" {
		t.Fatalf("file get query %q", q)
	}
	if p := raw["GET /projects/acme/web/repository/files/docs/a.md"]; p != "/projects/acme%2Fweb/repository/files/docs%2Fa.md" {
		t.Fatalf("escaped path %q", p)
	}

	out = communityRun(t, f, "gitlab", typ, 1, map[string]interface{}{"resource": "file", "operation": "edit", "owner": "acme", "repository": "web",
		"filePath": "docs/a.md", "branch": "main", "commitMessage": "update", "fileContent": "# Bye",
		"additionalParameters": map[string]interface{}{"author": map[string]interface{}{"name": "Bot", "email": "bot@x.io"}}}, creds, "", "")
	b := jsonBody(t, f.find(t, "PUT", "/repository/files/"))
	if out[0].JSON["branch"] != "main" || b["content"] != "# Bye" || b["commit_message"] != "update" || b["author_name"] != "Bot" || b["branch"] != "main" {
		t.Fatalf("file edit: %v %v", out, b)
	}

	communityRun(t, f, "gitlab", typ, 1, map[string]interface{}{"owner": "acme", "repository": "web", "title": "Bug", "body": "Broken",
		"labels":       []interface{}{map[string]interface{}{"label": "bug"}, map[string]interface{}{"label": "p1"}},
		"assignee_ids": []interface{}{map[string]interface{}{"assignee": 5}}}, creds, "", "")
	b = jsonBody(t, f.find(t, "POST", "/projects/acme/web/issues"))
	if b["title"] != "Bug" || b["description"] != "Broken" || b["labels"] != "bug,p1" || mustJSON(b["assignee_ids"]) != "[5]" {
		t.Fatalf("issue create: %v", b)
	}

	out = communityRun(t, f, "gitlab", typ, 1, map[string]interface{}{"resource": "repository", "owner": "acme", "repository": "web", "returnAll": true,
		"getRepositoryIssuesFilters": map[string]interface{}{"state": "opened"}}, creds, "", "")
	if len(out) != 3 || !strings.Contains(f.find(t, "GET", "/projects/acme/web/issues").query, "per_page=100") {
		t.Fatalf("getIssues: %v", out)
	}

	out = communityRun(t, f, "gitlab", typ, 1, map[string]interface{}{"resource": "repository", "operation": "get", "owner": "acme", "repository": "web"}, creds, "", "")
	if out[0].JSON["id"] != float64(42) {
		t.Fatalf("repository get: %v", out)
	}

	f.reqs = nil
	oauth := map[string]map[string]interface{}{"gitlabOAuth2Api": {"server": "https://gitlab.com", "oauthTokenData": map[string]interface{}{"access_token": "gl-oauth"}}}
	out = communityRun(t, f, "gitlab", typ, 1, map[string]interface{}{"authentication": "oAuth2", "resource": "release", "operation": "getAll", "projectId": "42", "limit": 1}, oauth, "", "")
	if len(out) != 1 || f.reqs[0].auth != "Bearer gl-oauth" || !strings.Contains(f.reqs[0].query, "per_page=1") {
		t.Fatalf("releases: %v %+v", out, f.reqs)
	}
}
