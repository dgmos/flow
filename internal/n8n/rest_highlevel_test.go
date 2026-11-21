package n8n

import (
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// restRunV is restRun for a given node typeVersion.
func restRunV(t *testing.T, f *fakeAPI, apiKey, nodeType string, version float64, params map[string]interface{}, creds map[string]map[string]interface{}) []map[string]interface{} {
	t.Helper()
	sc := NewStaticCredentials()
	var credType string
	for k, v := range creds {
		sc.Set(k, "*", v)
		credType = k
	}
	h := newHarness(t, &Options{Credentials: sc, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{apiKey: f.srv.URL}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(map[string]interface{}{"name": "Svc", "type": "n8n-nodes-base." + nodeType, "typeVersion": version, "parameters": params}, credType),
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

func TestHighLevelNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.method == "POST" && r.path == "/contacts/upsert/":
			return 200, "application/json", map[string]interface{}{"contact": map[string]interface{}{"id": "c1", "locationId": "L1"}}
		case r.method == "POST" && r.path == "/contacts/c1/notes":
			return 201, "application/json", map[string]interface{}{"note": map[string]interface{}{"id": "n1"}}
		case r.path == "/contacts/":
			if strings.Contains(r.query, "startAfterId=c2") {
				return 200, "application/json", map[string]interface{}{"contacts": []interface{}{map[string]interface{}{"id": "c3"}}, "meta": map[string]interface{}{"total": 3}}
			}
			return 200, "application/json", map[string]interface{}{"contacts": []interface{}{map[string]interface{}{"id": "c1"}, map[string]interface{}{"id": "c2"}},
				"meta": map[string]interface{}{"total": 3, "startAfterId": "c2", "startAfter": 1700}}
		case r.path == "/opportunities/search":
			return 200, "application/json", map[string]interface{}{"opportunities": []interface{}{map[string]interface{}{"id": "o1"}, map[string]interface{}{"id": "o2"}}, "meta": map[string]interface{}{"total": 9}}
		case r.method == "GET" && r.path == "/contacts/c1/tasks/t1/":
			return 200, "application/json", map[string]interface{}{"id": "t1", "title": "Call", "dueDate": "2024-05-01T10:00:00.000Z"}
		case r.method == "PUT" && r.path == "/contacts/c1/tasks/t1/":
			return 200, "application/json", map[string]interface{}{"id": "t1"}
		case r.method == "POST" && r.path == "/contacts":
			return 200, "application/json", map[string]interface{}{"contact": map[string]interface{}{"id": "v1c"}}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"highLevelOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "at", "locationId": "L1"}}}

	out := restRunV(t, f, "highLevel", "highLevel", 2, map[string]interface{}{"email": "a@b.io", "additionalFields": map[string]interface{}{
		"tags": "x, y", "notes": "hello", "customFields": map[string]interface{}{"values": []interface{}{map[string]interface{}{"fieldId": map[string]interface{}{"__rl": true, "value": "cf1"}, "fieldValue": "v"}}}}}, cred)
	if len(out) != 1 || out[0]["id"] != "c1" {
		t.Fatalf("contact create: %v", out)
	}
	r := f.find(t, "POST", "/contacts/upsert/")
	if r.auth != "Bearer at" || mustJSON(jsonBody(t, r)) != `{"customFields":[{"fieldValue":"v","id":"cf1"}],"email":"a@b.io","locationId":"L1","tags":["x","y"]}` {
		t.Fatalf("contact create: %s %s", r.auth, r.body)
	}
	if b := jsonBody(t, f.find(t, "POST", "/contacts/c1/notes")); b["userId"] != "L1" || b["body"] != "hello" {
		t.Fatalf("note: %v", b)
	}

	out = restRunV(t, f, "highLevel", "highLevel", 2, map[string]interface{}{"operation": "getAll", "returnAll": true, "filters": map[string]interface{}{"query": "jo"}}, cred)
	if len(out) != 3 || out[2]["id"] != "c3" {
		t.Fatalf("contact getAll: %v", out)
	}
	if q := f.find(t, "GET", "/contacts/").query; !strings.Contains(q, "locationId=L1") || !strings.Contains(q, "limit=100") || !strings.Contains(q, "query=jo") {
		t.Fatalf("contact getAll query %q", q)
	}

	out = restRunV(t, f, "highLevel", "highLevel", 2, map[string]interface{}{"resource": "opportunity", "operation": "getAll", "limit": 1, "filters": map[string]interface{}{"pipelineId": "p1"}}, cred)
	if q := f.find(t, "GET", "/opportunities/search").query; len(out) != 1 || !strings.Contains(q, "location_id=L1") || !strings.Contains(q, "limit=1") || !strings.Contains(q, "pipeline_id=p1") {
		t.Fatalf("opportunity getAll: %v %q", out, q)
	}

	out = restRunV(t, f, "highLevel", "highLevel", 2, map[string]interface{}{"resource": "task", "operation": "update", "contactId": "c1", "taskId": "t1", "updateFields": map[string]interface{}{"completed": true}}, cred)
	if out[0]["contactId"] != "c1" {
		t.Fatalf("task update: %v", out)
	}
	if b := jsonBody(t, f.find(t, "PUT", "/contacts/c1/tasks/t1/")); b["title"] != "Call" || b["dueDate"] != "2024-05-01T10:00:00Z" || b["completed"] != true {
		t.Fatalf("task update body: %v", b)
	}

	// Version 1: old API, API key, customField map.
	out = restRunV(t, f, "highLevel", "highLevel", 1, map[string]interface{}{"phone": "+15550100", "additionalFields": map[string]interface{}{
		"customFields": map[string]interface{}{"values": []interface{}{map[string]interface{}{"fieldId": "f9", "fieldValue": 3}}}}}, map[string]map[string]interface{}{"highLevelApi": {"apiKey": "k1"}})
	f.mu.Lock()
	r = f.reqs[len(f.reqs)-1]
	f.mu.Unlock()
	if out[0]["id"] != "v1c" || r.auth != "Bearer k1" || mustJSON(jsonBody(t, r)) != `{"customField":{"f9":3},"phone":"+15550100"}` {
		t.Fatalf("v1 contact create: %v %s", out, r.body)
	}
}
