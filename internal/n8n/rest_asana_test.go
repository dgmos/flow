package n8n

import (
	"strings"
	"testing"
)

func TestAsanaNode(t *testing.T) {
	var f *fakeAPI
	f = newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer tok" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/tasks":
			return 201, "application/json", map[string]interface{}{"data": map[string]interface{}{"gid": "1", "name": "Write"}}
		case r.method == "GET" && r.path == "/tasks":
			if strings.Contains(r.query, "offset=o2") {
				return 200, "application/json", map[string]interface{}{"data": []interface{}{map[string]interface{}{"gid": "c"}}, "next_page": nil}
			}
			return 200, "application/json", map[string]interface{}{"data": []interface{}{map[string]interface{}{"gid": "a"}, map[string]interface{}{"gid": "b"}},
				"next_page": map[string]interface{}{"offset": "o2", "uri": f.srv.URL + "/tasks?limit=100&offset=o2"}}
		case r.method == "POST" && r.path == "/tasks/1/stories":
			return 201, "application/json", map[string]interface{}{"data": map[string]interface{}{"gid": "s1"}}
		case r.method == "POST" && r.path == "/tasks/1/addProject":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{}}
		case r.method == "DELETE":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{}}
		}
		return 404, "application/json", map[string]interface{}{"err": r.path}
	})
	cred := map[string]map[string]interface{}{"asanaApi": {"accessToken": "tok"}}

	out := restRun(t, f, "asana", "asana", map[string]interface{}{"name": "Write", "workspace": "W1", "otherProperties": map[string]interface{}{"notes": "n", "projects": []interface{}{"P1"}}}, cred)
	if len(out) != 1 || out[0]["gid"] != "1" {
		t.Fatalf("create: %v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/tasks"))
	d := asMap(b["data"])
	if d["name"] != "Write" || d["workspace"] != "W1" || d["notes"] != "n" || mustJSON(d["projects"]) != `["P1"]` {
		t.Fatalf("create body: %v", b)
	}

	out = restRun(t, f, "asana", "asana", map[string]interface{}{"operation": "getAll", "returnAll": true, "filters": map[string]interface{}{"project": "P1", "opt_fields": []interface{}{"name", "notes"}}}, cred)
	if len(out) != 3 || out[2]["gid"] != "c" {
		t.Fatalf("getAll: %v", out)
	}
	if q := f.find(t, "GET", "/tasks").query; !strings.Contains(q, "limit=100") || !strings.Contains(q, "project=P1") || !strings.Contains(q, "opt_fields=name%2Cnotes") {
		t.Fatalf("getAll query %q", q)
	}
	out = restRun(t, f, "asana", "asana", map[string]interface{}{"operation": "getAll", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("getAll limit: %v", out)
	}

	out = restRun(t, f, "asana", "asana", map[string]interface{}{"resource": "taskComment", "id": "1", "text": "<body>hi</body>", "isTextHtml": true}, cred)
	if out[0]["gid"] != "s1" {
		t.Fatalf("comment: %v", out)
	}
	if d := asMap(jsonBody(t, f.find(t, "POST", "/tasks/1/stories"))["data"]); d["html_text"] != "<body>hi</body>" || d["text"] != nil {
		t.Fatalf("comment body: %v", d)
	}

	out = restRun(t, f, "asana", "asana", map[string]interface{}{"resource": "taskProject", "id": "1", "project": "P1", "additionalFields": map[string]interface{}{"section": "S1"}}, cred)
	if out[0]["success"] != true {
		t.Fatalf("taskProject: %v", out)
	}
	if d := asMap(jsonBody(t, f.find(t, "POST", "/tasks/1/addProject"))["data"]); d["project"] != "P1" || d["section"] != "S1" {
		t.Fatalf("taskProject body: %v", d)
	}

	out = restRun(t, f, "asana", "asana", map[string]interface{}{"resource": "project", "operation": "delete", "id": "P9"}, cred)
	if out[0]["success"] != true || f.find(t, "DELETE", "/projects/P9").body != "" {
		t.Fatalf("project delete: %v", out)
	}
}
