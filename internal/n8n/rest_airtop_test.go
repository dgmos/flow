package n8n

import (
	"encoding/base64"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAirtopNode(t *testing.T) {
	poll, timeout := airtopPoll, airtopTimeout
	airtopPoll, airtopTimeout = time.Millisecond, 2*time.Second
	defer func() { airtopPoll, airtopTimeout = poll, timeout }()

	var statusCalls, resultCalls int32
	shot := base64.StdEncoding.EncodeToString([]byte("JPEGDATA"))
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer at-key" {
			t.Errorf("auth %q on %s", r.auth, r.path)
		}
		switch {
		case r.method == "POST" && r.path == "/sessions":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"id": "s1", "status": "initializing"}, "meta": map[string]interface{}{"requestId": "rq"}}
		case r.method == "GET" && r.path == "/sessions/s1":
			atomic.AddInt32(&statusCalls, 1)
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"id": "s1", "status": "running"}}
		case r.method == "PUT" && r.path == "/sessions/s1/save-profile-on-termination/my-profile":
			return 200, "application/json", map[string]interface{}{}
		case r.method == "POST" && r.path == "/sessions/s1/windows":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"windowId": "w1"}}
		case r.path == "/sessions/s1/windows/w1/page-query":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"modelResponse": `{"title":"Hello"}`}, "meta": map[string]interface{}{"status": "success"}}
		case r.path == "/sessions/s1/windows/w1/type":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"modelResponse": "typed"}}
		case r.path == "/sessions/s1/windows/w1/screenshot":
			return 200, "application/json", map[string]interface{}{"meta": map[string]interface{}{"screenshots": []interface{}{map[string]interface{}{"dataUrl": "data:image/jpeg;base64," + shot}}}}
		case r.method == "DELETE" && r.path == "/sessions/s1":
			return 200, "application/json", map[string]interface{}{}
		case r.path == "/sessions/s1/events":
			return 200, "text/event-stream", "event: status\ndata: {\"event\":\"status\"}\n\ndata: {\"event\":\"file_status\",\"status\":\"available\",\"fileId\":\"f9\",\"downloadUrl\":\"https://dl/x\"}\n\n"
		case r.path == "/files":
			if strings.Contains(r.query, "offset=100") {
				return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"files": []interface{}{map[string]interface{}{"id": "f3"}}, "pagination": map[string]interface{}{"hasMore": false}}}
			}
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"files": []interface{}{map[string]interface{}{"id": "f1"}, map[string]interface{}{"id": "f2"}}, "pagination": map[string]interface{}{"hasMore": true}}}
		case r.path == "/v2/agents/ag1":
			return 200, "application/json", map[string]interface{}{"webhookId": "wh1"}
		case r.path == "/hooks/agents/ag1/webhooks/wh1":
			return 200, "application/json", map[string]interface{}{"invocationId": "inv1"}
		case r.path == "/hooks/agents/ag1/invocations/inv1/result":
			if atomic.AddInt32(&resultCalls, 1) < 2 {
				return 200, "application/json", map[string]interface{}{"status": "Running"}
			}
			return 200, "application/json", map[string]interface{}{"status": "Completed", "output": map[string]interface{}{"answer": 42}}
		}
		t.Errorf("unexpected %s %s", r.method, r.path)
		return 404, "application/json", map[string]interface{}{}
	})
	creds := map[string]map[string]interface{}{"airtopApi": {"apiKey": "at-key"}}
	run := func(p map[string]interface{}) []map[string]interface{} {
		return restRun(t, f, "airtop", "airtop", p, creds)
	}

	// Default resource/operation: session/create with integrated proxy and save profile.
	out := run(map[string]interface{}{"profileName": "my-profile", "saveProfileOnTermination": true, "proxy": "integrated", "timeoutMinutes": 15})
	if out[0]["sessionId"] != "s1" || jpath(out[0], "data.status") != "initializing" {
		t.Fatalf("create %v", out)
	}
	cfg := asMap(jsonBody(t, f.find(t, "POST", "/sessions"))["configuration"])
	if cfg["profileName"] != "my-profile" || cfg["timeoutMinutes"] != 15.0 || mustJSON(cfg["proxy"]) != `{"country":"US","sticky":true}` || cfg["record"] != false {
		t.Fatalf("config %v", cfg)
	}
	if atomic.LoadInt32(&statusCalls) == 0 {
		t.Fatal("session status not polled")
	}
	f.find(t, "PUT", "/save-profile-on-termination/my-profile")

	// Extraction with an automatically created session, JSON output and auto-termination.
	f.reqs = nil
	out = run(map[string]interface{}{"resource": "extraction", "operation": "query", "sessionMode": "new", "url": "https://example.com",
		"prompt": "Get the title", "additionalFields": map[string]interface{}{"outputSchema": `{"type":"object"}`, "parseJsonOutput": true}})
	if mustJSON(out[0]) != `{"output":{"title":"Hello"}}` {
		t.Fatalf("query %v", out)
	}
	if b := jsonBody(t, f.find(t, "POST", "/windows")); b["url"] != "https://example.com" {
		t.Fatalf("window %v", b)
	}
	q := jsonBody(t, f.find(t, "POST", "/page-query"))
	if q["prompt"] != "Get the title" || mustJSON(q["configuration"]) != `{"experimental":{"includeVisualAnalysis":"disabled"},"outputSchema":"{\"type\":\"object\"}"}` {
		t.Fatalf("query body %v", q)
	}
	f.find(t, "DELETE", "/sessions/s1")

	// Interaction with navigation options.
	out = run(map[string]interface{}{"resource": "interaction", "operation": "type", "sessionId": "s1", "windowId": "w1", "text": "hello",
		"pressEnterKey": true, "additionalFields": map[string]interface{}{"waitForNavigation": "load"}})
	if out[0]["sessionId"] != "s1" || out[0]["windowId"] != "w1" || jpath(out[0], "data.modelResponse") != "typed" {
		t.Fatalf("type %v", out)
	}
	tb := jsonBody(t, f.find(t, "POST", "/type"))
	if tb["text"] != "hello" || tb["pressEnterKey"] != true || tb["elementDescription"] != "" || tb["waitForNavigation"] != true ||
		mustJSON(tb["configuration"]) != `{"waitForNavigationConfig":{"waitUntil":"load"}}` {
		t.Fatalf("type body %v", tb)
	}

	// Screenshot as binary.
	items := restRunItems(t, f, "airtop", "airtop", 1.1, map[string]interface{}{"resource": "window", "operation": "takeScreenshot",
		"sessionId": "s1", "windowId": "w1", "outputImageAsBinary": true}, creds)
	bin := asMap(items[0].Binary["data"])
	if items[0].JSON["image"] != "" || bin["data"] != base64.StdEncoding.EncodeToString([]byte("JPEGDATA")) || bin["fileName"] != "screenshot.jpg" {
		t.Fatalf("screenshot %+v", items[0])
	}

	// Wait for download reads the session's event stream.
	out = run(map[string]interface{}{"operation": "waitForDownload", "sessionId": "s1"})
	if mustJSON(out[0]) != `{"data":{"downloadUrl":"https://dl/x","fileId":"f9"},"sessionId":"s1"}` {
		t.Fatalf("download %v", out)
	}

	// Files: limit, and returnAll with offset pagination split into items.
	f.reqs = nil
	out = run(map[string]interface{}{"resource": "file", "limit": 5, "sessionIds": "s1"})
	if len(out) != 1 || len(asList(jpath(out[0], "data.files"))) != 2 || f.find(t, "GET", "/files").query != "limit=5&sessionIds=s1" {
		t.Fatalf("files %v %+v", out, f.reqs)
	}
	out = run(map[string]interface{}{"resource": "file", "operation": "getMany", "returnAll": true, "outputSingleItem": false})
	if len(out) != 3 || out[2]["id"] != "f3" {
		t.Fatalf("all files %v", out)
	}

	// Agent run: details from /v2, invocation and result polling under /hooks.
	f.reqs = nil
	out = run(map[string]interface{}{"resource": "agent", "agentId": map[string]interface{}{"__rl": true, "mode": "id", "value": "ag1"},
		"agentParameters": map[string]interface{}{"mappingMode": "defineBelow", "value": map[string]interface{}{"topic": "cats"},
			"schema": []interface{}{map[string]interface{}{"id": "topic", "required": true}}}, "profileName": "p-1"})
	if mustJSON(out[0]) != `{"invocationId":"inv1","output":{"answer":42},"status":"Completed"}` {
		t.Fatalf("agent %v", out)
	}
	inv := f.find(t, "POST", "/webhooks/wh1")
	if inv.query != "profileId=p-1" || inv.body != `{"configVars":{"topic":"cats"}}` {
		t.Fatalf("invoke %+v", inv)
	}
}
