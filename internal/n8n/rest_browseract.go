package n8n

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
)

// BrowserAct community nodes: n8n-nodes-browseract.browserAct (runs a bot
// or template and waits, API v2 as in package 1.0–1.3, whose bot inputs are
// stored in the workflow) and n8n-nodes-browseract-workflows.browserAct
// (workflow task operations).

// browserActPoll is the bot status polling interval of
// n8n-nodes-browseract (tests shorten it).
var browserActPoll = 5 * time.Second

var browserActTerminal = map[string]bool{"finished": true, "canceled": true, "failed": true}

func init() {
	registerREST("n8n-nodes-browseract.browserAct", &restService{
		defResource: "workflow",
		defOps:      map[string]string{"workflow": "runWorkflow"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := browserActClient(ec, i, "browserAct", "https://api.browseract.com/v2")
			if err == nil {
				c.headers["api-channel-ak"] = "n8nak"
			}
			return c, err
		},
		ops: map[string]*restOp{"workflow/runWorkflow": {run: browserActRun}},
	})

	q := func(params ...string) string { return strings.Join(params, " ") }
	registerREST("n8n-nodes-browseract-workflows.browserAct", &restService{
		defResource: "workflow",
		defOps:      map[string]string{"workflow": "runWorkflowAndGetData"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			return browserActClient(ec, i, "browserActWorkflows", "https://api.browseract.com/v2/workflow")
		},
		ops: map[string]*restOp{
			"workflow/runTask":       {method: "POST", path: "/run-task", build: browserActTaskBody},
			"workflow/getTask":       {run: browserActGetTask},
			"workflow/getTaskStatus": {path: "/get-task-status", query: q("taskId:task_id")},
			"workflow/stopTask":      {method: "PUT", path: "/stop-task", query: q("taskId:task_id")},
			"workflow/listTasks":     {path: "/list-tasks", query: q("workflowIdForOtherOps:workflow_id @pagination *")},
			"workflow/listWorkflows": {path: "/list-workflows", query: q("@pagination *")},
			"workflow/getWorkflow":   {path: "/get-workflow", query: q("workflowIdForOtherOps:workflow_id")},
			"workflow/runWorkflowAndGetData": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				req := &restRequest{method: "POST", path: "/run-task", query: url.Values{}, body: map[string]interface{}{}}
				if err := browserActTaskBody(ec, i, req); err != nil {
					return nil, err
				}
				v, err := c.do(ec, req)
				if err != nil {
					return nil, err
				}
				id := toString(asMap(v)["id"])
				if id == "" {
					return nil, permanentError{"Failed to start task or get a task ID."}
				}
				return browserActWait(ec, i, c, id, "runPollingInterval", "runMaxWaitTime")
			}},
		},
	})
}

func browserActClient(ec *execCtx, i int, key, base string) (*restClient, error) {
	c, _, err := ec.restCred(i, "browserActApi", nil)
	if err != nil {
		return nil, err
	}
	return bearerClient(ec.apiBase(key, base), c, "apiKey"), nil
}

// browserActTaskBody builds a run-task body (workflows package).
func browserActTaskBody(ec *execCtx, i int, r *restRequest) error {
	a := &cmtyArgs{ec: ec, i: i}
	r.body["workflow_id"] = a.get("workflowId", "")
	params := asList(asMap(a.get("inputParameters", nil))["parameters"])
	if params == nil {
		params = []interface{}{}
	}
	r.body["input_parameters"] = params
	f := fieldsOf(ec, i, "additionalFields")
	if toBool(f["saveBrowserData"]) {
		r.body["save_browser_data"] = f["saveBrowserData"]
	}
	if !restEmpty(f["profileId"]) {
		r.body["profile_id"] = f["profileId"]
	}
	return a.err
}

func browserActGetTask(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	a := &cmtyArgs{ec: ec, i: i}
	id := a.str("taskId", "")
	if a.err != nil {
		return nil, a.err
	}
	if !a.bool("waitForFinish", false) {
		v, err := cmtyCall(ec, c, "GET", "/get-task", url.Values{"task_id": {id}}, nil)
		if err != nil {
			return nil, err
		}
		return restItems(v), nil
	}
	return browserActWait(ec, i, c, id, "pollingInterval", "maxWaitTime")
}

// browserActWait polls a task's status until it ends, then returns the task
// (workflows package).
func browserActWait(ec *execCtx, i int, c *restClient, id, intervalParam, maxParam string) ([]Item, error) {
	interval, _ := ec.num(i, intervalParam, 10)
	maxWait, _ := ec.num(i, maxParam, 300)
	start := time.Now()
	status := ""
	for {
		if time.Since(start).Seconds() > maxWait {
			return nil, permanentError{fmt.Sprintf("Task polling timed out after %v seconds.", maxWait)}
		}
		v, err := cmtyCall(ec, c, "GET", "/get-task-status", url.Values{"task_id": {id}}, nil)
		if err != nil {
			return nil, err
		}
		if status = toString(asMap(v)["status"]); browserActTerminal[status] {
			break
		}
		if err := cmtySleep(ec, time.Duration(interval*float64(time.Second))); err != nil {
			return nil, err
		}
	}
	v, err := cmtyCall(ec, c, "GET", "/get-task", url.Values{"task_id": {id}}, nil)
	if err != nil {
		return nil, err
	}
	if status == "failed" {
		fi := asMap(asMap(v)["task_failure_info"])
		return nil, permanentError{fmt.Sprintf("Task failed with code: %v. Message: %s", fi["code"], firstNonEmpty(toString(fi["message"]), "No message provided."))}
	}
	return restItems(v), nil
}

// browserActRun runs a bot or template with the inputs of the node's Bot
// Inputs mapper and waits for it (n8n-nodes-browseract).
func browserActRun(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	a := &cmtyArgs{ec: ec, i: i}
	timeout := a.num("timeout", 3600)
	if timeout == 0 {
		timeout = 3600
	}
	if timeout < 0 {
		return nil, permanentError{"Timeout needs to be greater than 0"}
	}
	limit := int(math.Min(math.Ceil(timeout*1000/5000), 16560))
	typ := a.str("type", "")
	if typ == "" {
		// Package 1.0 had only bots; later versions default to templates.
		typ = "TEMPLATE"
		if !a.has("templateId") && a.has("workflowId") {
			typ = "WORKFLOW"
		}
	}
	body := map[string]interface{}{}
	path := "/workflow/run-task"
	if typ == "TEMPLATE" {
		path = "/workflow/run-task-by-template"
		body["workflow_template_id"] = a.get("templateId", "")
		if p := a.str("proxyRegion", ""); p != "" {
			body["proxyRegion"] = p
		}
	} else {
		body["workflow_id"] = rlValue(a.get("workflowId", ""))
		body["open_incognito_mode"] = a.bool("open_incognito_mode", true)
	}
	cfg := asMap(a.get("workflowConfig", nil))
	if a.err != nil {
		return nil, a.err
	}
	values, ok := cfg["value"].(map[string]interface{})
	if !ok {
		return nil, permanentError{"Please select a bot to run"}
	}
	params := []interface{}{}
	var missing []string
	if schema := asList(cfg["schema"]); len(schema) > 0 {
		for _, f := range schema {
			m := asMap(f)
			id := toString(m["id"])
			v := strings.TrimSpace(toString(values[id]))
			if v != "" && strings.HasPrefix(id, "input-") {
				params = append(params, map[string]interface{}{"name": strings.TrimPrefix(id, "input-"), "value": v})
			}
			if toBool(m["required"]) && v == "" {
				missing = append(missing, toString(m["displayName"]))
			}
		}
	} else {
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v := strings.TrimSpace(toString(values[k])); v != "" && strings.HasPrefix(k, "input-") {
				params = append(params, map[string]interface{}{"name": strings.TrimPrefix(k, "input-"), "value": v})
			}
		}
	}
	if len(missing) > 0 {
		return nil, permanentError{"Please fill in the required fields: " + strings.Join(missing, ", ")}
	}
	body["input_parameters"] = params
	v, err := cmtyCall(ec, c, "POST", path, nil, body)
	if err != nil {
		return nil, err
	}
	id := toString(asMap(v)["id"])
	if id == "" {
		return restItems(map[string]interface{}{"error": "No task id returned", "taskId": asMap(v)["id"]}), nil
	}
	q := url.Values{"task_id": {id}}
	for j := 0; j < limit; j++ {
		if err := cmtySleep(ec, browserActPoll); err != nil {
			return nil, err
		}
		d, err := cmtyCall(ec, c, "GET", "/workflow/get-task", q, nil)
		if err != nil {
			return nil, err
		}
		if browserActTerminal[toString(asMap(d)["status"])] {
			return restItems(d), nil
		}
	}
	if _, err := cmtyCall(ec, c, "PUT", "/workflow/stop-task", url.Values{"task_id": {id}, "biz_code": {"101"}}, nil); err != nil {
		return nil, err
	}
	d, err := cmtyCall(ec, c, "GET", "/workflow/get-task", q, nil)
	if err != nil {
		return nil, err
	}
	return restItems(d), nil
}
