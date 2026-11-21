package n8n

import (
	"net/url"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// todoistRun runs a Todoist node of the given typeVersion against f.
func todoistRun(t *testing.T, f *fakeAPI, version float64, params map[string]interface{}) []map[string]interface{} {
	t.Helper()
	sc := NewStaticCredentials()
	sc.Set("todoistApi", "*", map[string]interface{}{"apiKey": "td"})
	h := newHarness(t, &Options{Credentials: sc, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}},
		APIBaseURLs: map[string]string{"todoist": f.srv.URL, "todoistSync": f.srv.URL + "/sync9"}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(nd("Svc", "todoist", version, params), "todoistApi"),
	}, "Start->Svc")
	if _, rep, err := Import(data, ImportOptions{}); err != nil || !rep.Runnable {
		t.Fatalf("not runnable: %v %+v", err, rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	if steps["Svc"].Error != "" {
		t.Fatalf("node failed: %s", steps["Svc"].Error)
	}
	return itemsOf(t, steps["Svc"], 0)
}

func TestTodoistNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer td" {
			t.Errorf("auth %q", r.auth)
		}
		q, _ := url.ParseQuery(r.query)
		switch {
		case r.method == "POST" && r.path == "/tasks":
			return 200, "application/json", map[string]interface{}{"id": "t1", "content": "Buy"}
		case r.method == "GET" && r.path == "/labels/7":
			return 200, "application/json", map[string]interface{}{"id": "7", "name": "home"}
		case r.method == "GET" && r.path == "/tasks/filter":
			if q.Get("cursor") == "n2" {
				return 200, "application/json", map[string]interface{}{"results": []interface{}{map[string]interface{}{"id": "c"}}, "next_cursor": nil}
			}
			return 200, "application/json", map[string]interface{}{"results": []interface{}{map[string]interface{}{"id": "a"}, map[string]interface{}{"id": "b"}}, "next_cursor": "n2"}
		case r.method == "GET" && r.path == "/tasks":
			return 200, "application/json", []interface{}{map[string]interface{}{"id": "a"}, map[string]interface{}{"id": "b"}, map[string]interface{}{"id": "c"}}
		case r.method == "POST" && (r.path == "/tasks/t1" || r.path == "/tasks/t1/close"):
			return 204, "application/json", ""
		case r.method == "POST" && strings.HasSuffix(r.path, "/sync"):
			return 200, "application/json", map[string]interface{}{"sync_status": map[string]interface{}{}}
		case r.method == "GET" && r.path == "/sections":
			return 200, "application/json", []interface{}{map[string]interface{}{"id": "s1"}}
		}
		return 404, "application/json", map[string]interface{}{"err": r.path}
	})

	// Version 2.1 (REST v2): label names, priority parsing, due time format.
	out := todoistRun(t, f, 2.1, map[string]interface{}{"content": "Buy", "project": map[string]interface{}{"__rl": true, "mode": "id", "value": "P1"},
		"labels": []interface{}{"home"}, "options": map[string]interface{}{"priority": "4", "dueDateTime": "2024-01-02T10:00:00.000Z", "section": "S1"}})
	if len(out) != 1 || out[0]["id"] != "t1" {
		t.Fatalf("create: %v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/tasks"))
	if b["content"] != "Buy" || b["project_id"] != "P1" || b["priority"] != float64(4) || b["due_datetime"] != "2024-01-02T10:00:00Z" || b["section_id"] != "S1" || mustJSON(b["labels"]) != `["home"]` {
		t.Fatalf("create body: %v", b)
	}

	// Version 1 resolves label ids to names.
	todoistRun(t, f, 1, map[string]interface{}{"content": "Buy", "project": "P1", "labels": []interface{}{7}, "options": map[string]interface{}{}})
	f.mu.Lock()
	last := f.reqs[len(f.reqs)-1]
	f.mu.Unlock()
	if b := jsonBody(t, last); mustJSON(b["labels"]) != `["home"]` || b["priority"] != float64(1) {
		t.Fatalf("v1 create body: %v", b)
	}

	out = todoistRun(t, f, 2, map[string]interface{}{"operation": "getAll", "limit": 2, "filters": map[string]interface{}{"filter": "today"}})
	if len(out) != 2 {
		t.Fatalf("getAll v2: %v", out)
	}
	if q := f.find(t, "GET", "/tasks").query; q != "filter=today" {
		t.Fatalf("getAll v2 query %q", q)
	}

	// Version 2.2 (API v1): /tasks/filter with cursor pages.
	out = todoistRun(t, f, 2.2, map[string]interface{}{"operation": "getAll", "returnAll": true, "filters": map[string]interface{}{"filter": "today"}})
	if len(out) != 3 || out[2]["id"] != "c" {
		t.Fatalf("getAll 2.2: %v", out)
	}
	if q := f.find(t, "GET", "/tasks/filter").query; q != "query=today" {
		t.Fatalf("getAll 2.2 query %q", q)
	}
	out = todoistRun(t, f, 2.2, map[string]interface{}{"operation": "getAll", "limit": 1, "filters": map[string]interface{}{"filter": "today"}})
	if len(out) != 1 {
		t.Fatalf("getAll 2.2 limit: %v", out)
	}

	out = todoistRun(t, f, 2.1, map[string]interface{}{"operation": "update", "taskId": "t1", "updateFields": map[string]interface{}{"content": "Milk", "priority": 2}})
	if out[0]["success"] != true {
		t.Fatalf("update: %v", out)
	}
	if b := jsonBody(t, f.find(t, "POST", "/tasks/t1")); b["content"] != "Milk" || b["priority"] != float64(2) {
		t.Fatalf("update body: %v", b)
	}
	if out = todoistRun(t, f, 2.1, map[string]interface{}{"operation": "close", "taskId": "t1"}); out[0]["success"] != true {
		t.Fatalf("close: %v", out)
	}

	todoistRun(t, f, 2.1, map[string]interface{}{"operation": "move", "taskId": "t1", "project": "P1", "options": map[string]interface{}{"section": "S2"}})
	cmd := asMap(asList(jsonBody(t, f.find(t, "POST", "/sync9/sync"))["commands"])[0])
	if cmd["type"] != "item_move" || mustJSON(cmd["args"]) != `{"id":"t1","section_id":"S2"}` || cmd["uuid"] == "" {
		t.Fatalf("move: %v", cmd)
	}

	if out = todoistRun(t, f, 2.1, map[string]interface{}{"resource": "section", "operation": "getAll", "sectionFilters": map[string]interface{}{"project_id": "P1"}}); len(out) != 1 {
		t.Fatalf("sections: %v", out)
	}
	if q := f.find(t, "GET", "/sections").query; q != "project_id=P1" {
		t.Fatalf("sections query %q", q)
	}
}
