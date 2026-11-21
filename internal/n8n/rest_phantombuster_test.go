package n8n

import (
	"strings"
	"testing"
)

func TestPhantombusterNode(t *testing.T) {
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		switch r.method + " " + r.path {
		case "POST /agents/launch":
			return 200, "", map[string]interface{}{"containerId": "c1"}
		case "GET /containers/fetch":
			return 200, "", map[string]interface{}{"id": r.query.Get("id"), "status": "running"}
		case "GET /agents/fetch":
			return 200, "", map[string]interface{}{"id": r.query.Get("id"), "name": "Scraper"}
		case "GET /agents/fetch-output":
			return 200, "", map[string]interface{}{"containerId": "c2", "status": "finished"}
		case "GET /containers/fetch-result-object":
			return 200, "", map[string]interface{}{"resultObject": `[{"name":"Ann"},{"name":"Bob"}]`}
		case "GET /agents/fetch-all":
			return 200, "", []interface{}{map[string]interface{}{"id": "1"}, map[string]interface{}{"id": "2"}, map[string]interface{}{"id": "3"}}
		case "POST /agents/launch-sync":
			return 200, "application/x-ndjson", "{\"type\":\"start\",\"data\":{\"containerId\":\"c3\"}}\n{\"type\":\"log\",\"data\":\"..\"}\n{\"type\":\"summary\",\"data\":{\"exitCode\":0}}\n"
		}
		return 404, "", map[string]interface{}{"error": r.path}
	})
	bases := map[string]string{"phantombuster": ""}
	creds := map[string]map[string]interface{}{"phantombusterApi": {"apiKey": "pb-1"}}
	typ := "n8n-nodes-base.phantombuster"

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"agentId": "a1", "jsonParameters": true,
		"additionalFields": map[string]interface{}{"argumentsJson": `{"q":"x"}`, "manualLaunch": true}}, creds, nil)
	r := f.find(t, "POST", "/agents/launch")
	b := r.json(t)
	if out[0].JSON["id"] != "c1" || b["id"] != "a1" || mustJSON(b["arguments"]) != `{"q":"x"}` || b["manualLaunch"] != true || b["argumentsJson"] != nil ||
		r.header.Get("X-Phantombuster-Key") != "pb-1" {
		t.Fatalf("launch: %v %v", out, b)
	}

	cmtyRun(t, f, bases, typ, map[string]interface{}{"agentId": "a1", "resolveData": false,
		"additionalFields": map[string]interface{}{"argumentsUi": map[string]interface{}{"argumentValues": []interface{}{map[string]interface{}{"key": "k", "value": "v"}}}}}, creds, nil)
	if b := f.find(t, "POST", "/agents/launch").json(t); mustJSON(b) != `{"arguments":{"k":"v"},"id":"a1"}` {
		t.Fatalf("launch ui body: %v", b)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "get", "agentId": "a7"}, creds, nil)
	if out[0].JSON["name"] != "Scraper" || f.find(t, "GET", "/agents/fetch").query.Get("id") != "a7" {
		t.Fatalf("get: %v", out)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "getOutput", "agentId": "a7", "additionalFields": map[string]interface{}{"prevStatus": "running"}}, creds, nil)
	if len(out) != 2 || out[1].JSON["name"] != "Bob" || f.find(t, "GET", "/containers/fetch-result-object").query.Get("id") != "c2" ||
		f.find(t, "GET", "/agents/fetch-output").query.Get("prevStatus") != "running" {
		t.Fatalf("getOutput: %v", out)
	}
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "getOutput", "agentId": "a7", "resolveData": false}, creds, nil)
	if out[0].JSON["containerId"] != "c2" {
		t.Fatalf("getOutput raw: %v", out)
	}

	if out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "getAll", "limit": 2}, creds, nil); len(out) != 2 {
		t.Fatalf("getAll: %v", out)
	}
	if out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "getAll", "returnAll": true}, creds, nil); len(out) != 3 {
		t.Fatalf("getAll all: %v", out)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "launchSync", "agentId": "a1"}, creds, nil)
	if len(out) != 1 || out[0].JSON["exitCode"] != float64(0) || !strings.Contains(f.find(t, "POST", "/agents/launch-sync").body, `"id":"a1"`) {
		t.Fatalf("launchSync: %v", out)
	}
}
