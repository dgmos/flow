package n8n

import (
	"strings"
	"testing"
	"time"
)

func TestBrowserActNodes(t *testing.T) {
	old := browserActPoll
	browserActPoll = time.Millisecond
	t.Cleanup(func() { browserActPoll = old })
	gets := 0
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.header.Get("Authorization") != "Bearer bk" {
			t.Errorf("auth %q", r.header.Get("Authorization"))
		}
		switch r.method + " " + r.path {
		case "POST /v2/workflow/run-task-by-template", "POST /v2/workflow/run-task", "POST /wf/run-task":
			return 200, "", map[string]interface{}{"id": "T1"}
		case "GET /v2/workflow/get-task", "GET /wf/get-task":
			if gets++; gets == 1 && r.path == "/v2/workflow/get-task" {
				return 200, "", map[string]interface{}{"id": "T1", "status": "running"}
			}
			return 200, "", map[string]interface{}{"id": r.query.Get("task_id"), "status": "finished", "output": map[string]interface{}{"string": "data"}}
		case "GET /wf/get-task-status":
			if r.query.Get("task_id") == "BAD" {
				return 200, "", map[string]interface{}{"status": "failed"}
			}
			return 200, "", map[string]interface{}{"status": "finished"}
		case "GET /wf/list-workflows":
			return 200, "", map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": "W1"}}, "total_count": 1}
		}
		return 404, "", map[string]interface{}{}
	})
	bases := map[string]string{"browserAct": "/v2", "browserActWorkflows": "/wf"}
	cred := map[string]map[string]interface{}{"browserActApi": {"apiKey": "bk"}}

	// n8n-nodes-browseract: run a template with the mapped bot inputs and wait
	cfg := map[string]interface{}{"mappingMode": "defineBelow", "value": map[string]interface{}{"input-keyword": " shoes ", "input-limit": ""},
		"schema": []interface{}{map[string]interface{}{"id": "input-keyword", "displayName": "Keyword", "required": true}, map[string]interface{}{"id": "input-limit", "displayName": "Limit"}}}
	out := cmtyRun(t, f, bases, "n8n-nodes-browseract.browserAct", map[string]interface{}{"templateId": "TPL", "workflowConfig": cfg, "timeout": 60}, cred, nil)
	r := f.find(t, "POST", "/v2/workflow/run-task-by-template")
	if b := r.json(t); b["workflow_template_id"] != "TPL" || mustJSON(b["input_parameters"]) != `[{"name":"keyword","value":"shoes"}]` || r.header.Get("api-channel-ak") != "n8nak" {
		t.Fatalf("run body %v", b)
	}
	if out[0].JSON["status"] != "finished" || f.count("GET", "/v2/workflow/get-task") != 2 {
		t.Fatalf("run should poll until finished: %v", out[0].JSON)
	}
	cmtyRun(t, f, bases, "n8n-nodes-browseract.browserAct", map[string]interface{}{"type": "WORKFLOW", "workflowId": "W1", "open_incognito_mode": false, "workflowConfig": cfg}, cred, nil)
	if b := f.find(t, "POST", "/v2/workflow/run-task").json(t); b["workflow_id"] != "W1" || b["open_incognito_mode"] != false {
		t.Fatalf("bot body %v", b)
	}
	cfg["value"] = map[string]interface{}{"input-keyword": ""}
	if e := cmtyErr(t, f, bases, "n8n-nodes-browseract.browserAct", map[string]interface{}{"templateId": "TPL", "workflowConfig": cfg}, cred); !strings.Contains(e, "required fields: Keyword") {
		t.Fatalf("missing input: %q", e)
	}

	// n8n-nodes-browseract-workflows
	const wf = "n8n-nodes-browseract-workflows.browserAct"
	out = cmtyRun(t, f, bases, wf, map[string]interface{}{"workflowId": "W1", "runPollingInterval": 0, "inputParameters": map[string]interface{}{"parameters": []interface{}{map[string]interface{}{"name": "q", "value": "x"}}},
		"additionalFields": map[string]interface{}{"profileId": "P"}}, cred, nil)
	if b := f.find(t, "POST", "/wf/run-task").json(t); b["workflow_id"] != "W1" || b["profile_id"] != "P" || mustJSON(b["input_parameters"]) != `[{"name":"q","value":"x"}]` {
		t.Fatalf("workflows run body %v", b)
	}
	if out[0].JSON["id"] != "T1" || out[0].JSON["status"] != "finished" {
		t.Fatalf("workflows run out %v", out[0].JSON)
	}
	out = cmtyRun(t, f, bases, wf, map[string]interface{}{"operation": "getTask", "taskId": "T9"}, cred, nil)
	if out[0].JSON["id"] != "T9" || f.count("GET", "/wf/get-task-status") != 1 {
		t.Fatalf("getTask without waiting: %v", out[0].JSON)
	}
	if e := cmtyErr(t, f, bases, wf, map[string]interface{}{"operation": "getTask", "taskId": "BAD", "waitForFinish": true, "pollingInterval": 0}, cred); !strings.Contains(e, "Task failed") {
		t.Fatalf("failed task: %q", e)
	}
	out = cmtyRun(t, f, bases, wf, map[string]interface{}{"operation": "listWorkflows", "pagination": map[string]interface{}{"page": 2, "limit": 10}}, cred, nil)
	if q := f.find(t, "GET", "/wf/list-workflows").query; q.Get("page") != "2" || q.Get("limit") != "10" || len(out) != 1 || out[0].JSON["total_count"] != 1.0 {
		t.Fatalf("list workflows %v %v", q, out)
	}
}
