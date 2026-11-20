package n8n

import (
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

func TestApifyNode(t *testing.T) {
	polls := 0
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer ap-tok" {
			t.Errorf("auth %q on %s", r.auth, r.path)
		}
		switch {
		case r.method == "POST" && r.path == "/v2/acts/apify~google-search-scraper/runs":
			return 201, "application/json", map[string]interface{}{"data": map[string]interface{}{"id": "run1", "status": "RUNNING"}}
		case r.path == "/v2/actor-runs/run1":
			polls++
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"id": "run1", "status": "SUCCEEDED", "defaultDatasetId": "ds1"}}
		case r.path == "/v2/datasets/ds1/items":
			return 200, "application/json", []interface{}{map[string]interface{}{"title": "a"}, map[string]interface{}{"title": "b"}}
		}
		return 404, "application/json", map[string]interface{}{}
	})
	creds := NewStaticCredentials()
	creds.Set("apifyApi", "*", map[string]interface{}{"apiToken": "ap-tok"})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{"apify": f.srv.URL}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(map[string]interface{}{"name": "Apify", "type": "@apify/n8n-nodes-apify.apify", "typeVersion": 1, "parameters": map[string]interface{}{
			"operation": "Run actor and get dataset", "actorId": map[string]interface{}{"__rl": true, "mode": "list", "value": "apify/google-search-scraper"},
			"customBody": "={\"queries\": \"{{ 'n8n' }}\"}", "memory": 1024, "timeout": 60}}, "apifyApi"),
	}, "Start->Apify")
	_, rep, _ := Import(data, ImportOptions{})
	if !rep.Runnable {
		t.Fatalf("not runnable: %+v", rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	if got := itemsOf(t, steps["Apify"], 0); len(got) != 2 || got[1]["title"] != "b" {
		t.Fatalf("apify: %v (%s)", got, steps["Apify"].Error)
	}
	start := f.find(t, "POST", "/runs")
	if start.body != `{"queries":"n8n"}` || !strings.Contains(start.query, "memory=1024") || !strings.Contains(start.query, "waitForFinish=0") {
		t.Fatalf("start request: %+v", start)
	}
	if polls == 0 {
		t.Fatal("run was not polled")
	}
}

func TestApifyImportChecks(t *testing.T) {
	for _, c := range []struct {
		params   map[string]interface{}
		runnable bool
	}{
		{map[string]interface{}{"resource": "Datasets", "datasetId": "x"}, true},
		{map[string]interface{}{"operation": "Run actor and get dataset", "actorId": "a"}, true},
		{map[string]interface{}{"resource": "Actors", "operation": "Web fetch"}, false},
	} {
		data := wfJSON(t, []map[string]interface{}{
			nd("Start", "manualTrigger", 1, nil),
			{"name": "Apify", "type": "@apify/n8n-nodes-apify.apify", "typeVersion": 1, "parameters": c.params},
		}, "Start->Apify")
		if _, rep, _ := Import(data, ImportOptions{}); rep.Runnable != c.runnable {
			t.Errorf("%v: runnable=%v, want %v (%+v)", c.params, rep.Runnable, c.runnable, rep.Blocking)
		}
	}
}
