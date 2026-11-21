package n8n

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// restRun imports and runs Start -> one service node against a fake API and
// returns the node's output items. apiKey is the APIBaseURLs key of the
// service; creds maps credential types to their data.
func restRun(t *testing.T, f *fakeAPI, apiKey, nodeType string, params map[string]interface{}, creds map[string]map[string]interface{}) []map[string]interface{} {
	t.Helper()
	sc := NewStaticCredentials()
	var credType string
	for k, v := range creds {
		sc.Set(k, "*", v)
		credType = k
	}
	h := newHarness(t, &Options{Credentials: sc, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{apiKey: f.srv.URL}})
	if !strings.Contains(nodeType, ".") {
		nodeType = "n8n-nodes-base." + nodeType
	}
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(map[string]interface{}{"name": "Svc", "type": nodeType, "typeVersion": 1, "parameters": params}, credType),
	}, "Start->Svc")
	_, rep, err := Import(data, ImportOptions{})
	if err != nil || !rep.Runnable {
		t.Fatalf("not runnable: %v %+v", err, rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	if steps["Svc"].Error != "" {
		t.Fatalf("node failed: %s", steps["Svc"].Error)
	}
	return itemsOf(t, steps["Svc"], 0)
}

func jsonBody(t *testing.T, r recorded) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return m
}

func TestRESTFieldList(t *testing.T) {
	ec := &execCtx{params: map[string]interface{}{
		"name":             "Task",
		"additionalFields": map[string]interface{}{"dueDate": "2024-01-02T00:00:00Z", "tags": "a, b", "priority": "3", "fooBar": 1, "empty": ""},
		"ui":               map[string]interface{}{"pairs": []interface{}{map[string]interface{}{"key": "k", "value": "v"}}},
	}}
	dst := map[string]interface{}{}
	if err := ec.restFields(0, "name:task.title @additionalFields dueDate:due:ms tags:tags:csv priority::int * @ ui:custom:kv=pairs =fixed:true", dst); err != nil {
		t.Fatal(err)
	}
	want := `{"custom":{"k":"v"},"due":1704153600000,"fixed":true,"fooBar":1,"priority":3,"tags":["a","b"],"task":{"title":"Task"}}`
	if got := mustJSON(dst); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestClickUpNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "pk_1" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/list/L1/task":
			return 200, "application/json", map[string]interface{}{"id": "t1", "name": "Write"}
		case r.method == "GET" && r.path == "/list/L1/task":
			if strings.Contains(r.query, "page=0") {
				return 200, "application/json", map[string]interface{}{"tasks": []interface{}{map[string]interface{}{"id": "a"}, map[string]interface{}{"id": "b"}}}
			}
			if strings.Contains(r.query, "page=1") {
				return 200, "application/json", map[string]interface{}{"tasks": []interface{}{map[string]interface{}{"id": "c"}}}
			}
			return 200, "application/json", map[string]interface{}{"tasks": []interface{}{}}
		case r.method == "DELETE":
			return 204, "application/json", ""
		case r.method == "POST" && r.path == "/folder/F1/list":
			return 200, "application/json", map[string]interface{}{"id": "l9"}
		}
		return 404, "application/json", map[string]interface{}{"err": r.path}
	})
	cred := map[string]map[string]interface{}{"clickUpApi": {"accessToken": "pk_1"}}

	out := restRun(t, f, "clickUp", "clickUp", map[string]interface{}{"list": "L1", "name": "Write", "additionalFields": map[string]interface{}{
		"assignees": []interface{}{"12"}, "dueDate": "2024-01-02T00:00:00Z", "priority": 2, "tags": []interface{}{"x"}, "timeEstimate": 10}}, cred)
	if len(out) != 1 || out[0]["id"] != "t1" {
		t.Fatalf("create: %v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/list/L1/task"))
	if b["name"] != "Write" || b["due_date"] != float64(1704153600000) || b["time_estimate"] != float64(60000) || mustJSON(b["assignees"]) != "[12]" || b["priority"] != float64(2) {
		t.Fatalf("create body: %v", b)
	}

	out = restRun(t, f, "clickUp", "clickUp", map[string]interface{}{"operation": "getAll", "list": "L1", "returnAll": true, "filters": map[string]interface{}{"archived": false, "statuses": []interface{}{"open"}}}, cred)
	if len(out) != 3 || out[2]["id"] != "c" {
		t.Fatalf("getAll: %v", out)
	}
	if q := f.find(t, "GET", "/list/L1/task").query; !strings.Contains(q, "archived=false") || !strings.Contains(q, "statuses%5B%5D=open") {
		t.Fatalf("getAll query %q", q)
	}
	out = restRun(t, f, "clickUp", "clickUp", map[string]interface{}{"operation": "getAll", "list": "L1", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("getAll limit: %v", out)
	}

	out = restRun(t, f, "clickUp", "clickUp", map[string]interface{}{"operation": "delete", "id": "t1"}, cred)
	if len(out) != 1 || out[0]["success"] != true {
		t.Fatalf("delete: %v", out)
	}
	out = restRun(t, f, "clickUp", "clickUp", map[string]interface{}{"resource": "list", "space": "S1", "folder": "F1", "name": "Sprint"}, cred)
	if out[0]["id"] != "l9" {
		t.Fatalf("list create: %v", out)
	}
}

func TestRESTImportChecks(t *testing.T) {
	for _, c := range []struct {
		params   map[string]interface{}
		runnable bool
	}{
		{map[string]interface{}{"list": "x", "name": "n"}, true},
		{map[string]interface{}{"resource": "task", "operation": "getAll", "list": "x"}, true},
		{map[string]interface{}{"resource": "task", "operation": "teleport"}, false},
		{map[string]interface{}{"resource": "={{ $json.r }}", "operation": "get"}, true},
	} {
		data := wfJSON(t, []map[string]interface{}{
			nd("Start", "manualTrigger", 1, nil),
			{"name": "CU", "type": "n8n-nodes-base.clickUp", "typeVersion": 1, "parameters": c.params},
		}, "Start->CU")
		if _, rep, _ := Import(data, ImportOptions{}); rep.Runnable != c.runnable {
			t.Errorf("%v: runnable=%v, want %v (%+v)", c.params, rep.Runnable, c.runnable, rep.Blocking)
		}
	}
}
