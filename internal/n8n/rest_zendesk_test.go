package n8n

import (
	"strings"
	"testing"
)

func TestZendeskNode(t *testing.T) {
	var base string
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != basicAuth("me@x.io/token", "tok")["Authorization"] {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/tickets.json":
			return 201, "application/json", map[string]interface{}{"ticket": map[string]interface{}{"id": 7}}
		case r.method == "PUT" && r.path == "/tickets/7.json":
			return 200, "application/json", map[string]interface{}{"ticket": map[string]interface{}{"id": 7, "status": "solved"}}
		case r.path == "/search.json":
			if strings.Contains(r.query, "cursor=2") {
				return 200, "application/json", map[string]interface{}{"results": []interface{}{map[string]interface{}{"id": 3}}, "next_page": nil}
			}
			return 200, "application/json", map[string]interface{}{"results": []interface{}{map[string]interface{}{"id": 1}, map[string]interface{}{"id": 2}}, "next_page": base + "/search.json?cursor=2"}
		case r.path == "/users/search.json":
			return 200, "application/json", map[string]interface{}{"users": []interface{}{map[string]interface{}{"id": 9}}}
		case r.method == "DELETE":
			return 204, "application/json", ""
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	base = f.srv.URL
	cred := map[string]map[string]interface{}{"zendeskApi": {"subdomain": "acme", "email": "me@x.io", "apiToken": "tok"}}

	out := restRun(t, f, "zendesk", "zendesk", map[string]interface{}{"description": "Help", "additionalFields": map[string]interface{}{
		"subject": "Hi", "group": 5, "tags": []interface{}{"a"}, "customFieldsUi": map[string]interface{}{"customFieldsValues": []interface{}{map[string]interface{}{"id": 11, "value": "v"}}}}}, cred)
	if len(out) != 1 || out[0]["id"] != float64(7) {
		t.Fatalf("create: %v", out)
	}
	b := asMap(jsonBody(t, f.find(t, "POST", "/tickets.json"))["ticket"])
	if mustJSON(b) != `{"comment":{"body":"Help"},"custom_fields":[{"id":11,"value":"v"}],"group_id":5,"subject":"Hi","tags":["a"]}` {
		t.Fatalf("create body: %s", mustJSON(b))
	}

	out = restRun(t, f, "zendesk", "zendesk", map[string]interface{}{"operation": "update", "id": "7", "jsonParameters": true, "updateFieldsJson": `{"status":"solved"}`}, cred)
	if out[0]["status"] != "solved" || mustJSON(jsonBody(t, f.find(t, "PUT", "/tickets/7.json"))) != `{"ticket":{"status":"solved"}}` {
		t.Fatalf("update: %v", out)
	}

	out = restRun(t, f, "zendesk", "zendesk", map[string]interface{}{"operation": "getAll", "returnAll": true, "options": map[string]interface{}{"status": "open", "sortBy": "created_at"}}, cred)
	if len(out) != 3 {
		t.Fatalf("getAll: %v", out)
	}
	if q := f.find(t, "GET", "/search.json").query; !strings.Contains(q, "query=type%3Aticket+status%3Aopen") || !strings.Contains(q, "sort_by=created_at") || strings.Contains(q, "per_page") {
		t.Fatalf("getAll query %q", q)
	}
	out = restRun(t, f, "zendesk", "zendesk", map[string]interface{}{"operation": "getAll", "limit": 2}, cred)
	if len(out) != 2 {
		t.Fatalf("getAll limit: %v", out)
	}

	out = restRun(t, f, "zendesk", "zendesk", map[string]interface{}{"resource": "user", "operation": "search", "filters": map[string]interface{}{"query": "jo"}}, cred)
	if q := f.find(t, "GET", "/users/search.json").query; len(out) != 1 || !strings.Contains(q, "query=jo") || !strings.Contains(q, "per_page=100") {
		t.Fatalf("user search: %v %q", out, q)
	}
	out = restRun(t, f, "zendesk", "zendesk", map[string]interface{}{"resource": "organization", "operation": "delete", "id": "4"}, cred)
	if out[0]["success"] != true {
		t.Fatalf("org delete: %v", out)
	}
}
