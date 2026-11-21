package flow_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dgmos/flow"
)

func n8nWorkflow(nodes []map[string]interface{}, conns map[string]interface{}) []byte {
	for i, n := range nodes {
		n["id"] = n["name"]
		if _, ok := n["parameters"]; !ok {
			n["parameters"] = map[string]interface{}{}
		}
		nodes[i] = n
	}
	b, _ := json.Marshal(map[string]interface{}{"name": "api test", "nodes": nodes, "connections": conns})
	return b
}

func link(to string) map[string]interface{} {
	return map[string]interface{}{"main": [][]map[string]interface{}{{{"node": to, "type": "main", "index": 0}}}}
}

func setNode(name string, assignments ...[3]string) map[string]interface{} {
	var list []interface{}
	for _, a := range assignments {
		list = append(list, map[string]interface{}{"id": a[0], "name": a[0], "type": a[1], "value": a[2]})
	}
	return map[string]interface{}{"name": name, "type": "n8n-nodes-base.set", "typeVersion": 3.4,
		"parameters": map[string]interface{}{"assignments": map[string]interface{}{"assignments": list}}}
}

func startN8N(t *testing.T) (*flow.Engine, *httptest.Server, *flow.N8NStaticCredentials) {
	t.Helper()
	eng := flow.NewEngine(flow.NewMemoryStorage(), flow.NewRegistry())
	creds := flow.NewN8NStaticCredentials()
	eng.EnableN8N(flow.N8NOptions{Credentials: creds})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go eng.StartWorkerWithOptions(ctx, "default", "w", flow.WorkerOptions{Concurrency: 4, PollInterval: 5 * time.Millisecond})
	srv := httptest.NewServer(http.StripPrefix("/n8n", eng.N8NHandler(flow.N8NHandlerOptions{Namespace: "default", ResponseTimeout: 10 * time.Second})))
	t.Cleanup(srv.Close)
	return eng, srv, creds
}

func install(t *testing.T, eng *flow.Engine, id string, data []byte) *flow.N8NInstallResult {
	t.Helper()
	res, err := eng.InstallN8NWorkflow(context.Background(), "default", data, flow.N8NInstallOptions{N8NImportOptions: flow.N8NImportOptions{ID: id}})
	if err != nil {
		t.Fatalf("install: %v (%+v)", err, res)
	}
	return res
}

func do(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func TestN8NWebhookResponseModes(t *testing.T) {
	eng, srv, creds := startN8N(t)

	// lastNode: respond with the last node's first item.
	install(t, eng, "greet", n8nWorkflow([]map[string]interface{}{
		{"name": "Hook", "type": "n8n-nodes-base.webhook", "typeVersion": 2, "parameters": map[string]interface{}{"path": "greet", "httpMethod": "POST", "responseMode": "lastNode"}},
		setNode("Reply", [3]string{"greeting", "string", "=Hello {{ $json.body.name }} ({{ $json.query.lang }})"}),
	}, map[string]interface{}{"Hook": link("Reply")}))
	req, _ := http.NewRequest("POST", srv.URL+"/n8n/webhook/greet?lang=tr", strings.NewReader(`{"name":"Ada"}`))
	req.Header.Set("Content-Type", "application/json")
	if code, body := do(t, req); code != 200 || body != `{"greeting":"Hello Ada (tr)"}` {
		t.Fatalf("lastNode: %d %s", code, body)
	}

	// responseNode: Respond to Webhook decides status, headers and body.
	install(t, eng, "respond", n8nWorkflow([]map[string]interface{}{
		{"name": "Hook", "type": "n8n-nodes-base.webhook", "typeVersion": 2, "parameters": map[string]interface{}{"path": "page", "responseMode": "responseNode"}},
		{"name": "Respond", "type": "n8n-nodes-base.respondToWebhook", "typeVersion": 1.1, "parameters": map[string]interface{}{
			"respondWith": "text", "responseBody": "=<h1>{{ $json.query.title }}</h1>",
			"options": map[string]interface{}{"responseCode": 201, "responseHeaders": map[string]interface{}{"entries": []interface{}{map[string]interface{}{"name": "Content-Type", "value": "text/html"}}}}}},
	}, map[string]interface{}{"Hook": link("Respond")}))
	req, _ = http.NewRequest("GET", srv.URL+"/n8n/webhook/page?title=Hi", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 || string(b) != "<h1>Hi</h1>" || resp.Header.Get("Content-Type") != "text/html" {
		t.Fatalf("responseNode: %d %q %s", resp.StatusCode, b, resp.Header.Get("Content-Type"))
	}

	// onReceived + header auth.
	creds.Set("httpHeaderAuth", "*", map[string]interface{}{"name": "X-Token", "value": "s3cret"})
	hook := map[string]interface{}{"name": "Hook", "type": "n8n-nodes-base.webhook", "typeVersion": 2, "parameters": map[string]interface{}{"path": "secure", "httpMethod": "POST", "authentication": "headerAuth"},
		"credentials": map[string]interface{}{"httpHeaderAuth": map[string]interface{}{"id": "1", "name": "token"}}}
	install(t, eng, "secure", n8nWorkflow([]map[string]interface{}{hook, setNode("Noop")}, map[string]interface{}{"Hook": link("Noop")}))
	req, _ = http.NewRequest("POST", srv.URL+"/n8n/webhook/secure", nil)
	if code, _ := do(t, req); code != 401 {
		t.Fatalf("auth should fail: %d", code)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/n8n/webhook/secure", nil)
	req.Header.Set("X-Token", "s3cret")
	if code, body := do(t, req); code != 200 || !strings.Contains(body, "Workflow was started") {
		t.Fatalf("onReceived: %d %s", code, body)
	}

	req, _ = http.NewRequest("GET", srv.URL+"/n8n/webhook/nope", nil)
	if code, _ := do(t, req); code != 404 {
		t.Fatalf("unknown webhook: %d", code)
	}
}

func TestN8NChatAndTelegramTriggers(t *testing.T) {
	eng, srv, _ := startN8N(t)
	install(t, eng, "bot", n8nWorkflow([]map[string]interface{}{
		{"name": "Chat", "type": "@n8n/n8n-nodes-langchain.chatTrigger", "typeVersion": 1.1},
		setNode("Answer", [3]string{"output", "string", "=echo: {{ $json.chatInput }} [{{ $json.sessionId }}]"}),
	}, map[string]interface{}{"Chat": link("Answer")}))
	req, _ := http.NewRequest("POST", srv.URL+"/n8n/chat/bot", strings.NewReader(`{"sessionId":"s1","chatInput":"hi"}`))
	if code, body := do(t, req); code != 200 || body != `{"output":"echo: hi [s1]"}` {
		t.Fatalf("chat: %d %s", code, body)
	}

	tg := map[string]interface{}{"name": "Telegram", "type": "n8n-nodes-base.telegramTrigger", "typeVersion": 1.1, "webhookId": "tg-hook-1"}
	res := install(t, eng, "tg", n8nWorkflow([]map[string]interface{}{tg, setNode("Text", [3]string{"text", "string", "={{ $json.message.text }}"})}, map[string]interface{}{"Telegram": link("Text")}))
	if res.Report.Triggers[0].Path != "tg-hook-1" {
		t.Fatalf("telegram trigger spec: %+v", res.Report.Triggers)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/n8n/webhook/tg-hook-1", strings.NewReader(`{"update_id":1,"message":{"text":"/start"}}`))
	if code, _ := do(t, req); code != 200 {
		t.Fatalf("telegram webhook: %d", code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		execs, _ := eng.ListExecutions(context.Background(), "default")
		for _, e := range execs {
			if e.WorkflowID == "tg" && e.Status == flow.StatusCompleted {
				if items := flow.N8NOutputItems(e.Output); len(items) == 1 && items[0].JSON["text"] == "/start" {
					return
				}
				t.Fatalf("telegram output: %s", e.Output)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("telegram execution did not complete")
}

func TestN8NInstallSchedulesAndRefusal(t *testing.T) {
	eng := flow.NewEngine(flow.NewMemoryStorage(), flow.NewRegistry())
	if _, err := eng.InstallN8NWorkflow(context.Background(), "default", []byte(`{}`), flow.N8NInstallOptions{}); err == nil {
		t.Fatal("install without EnableN8N must fail")
	}
	eng.EnableN8N(flow.N8NOptions{Timezone: "UTC"})
	sched := n8nWorkflow([]map[string]interface{}{
		{"name": "Every day", "type": "n8n-nodes-base.scheduleTrigger", "typeVersion": 1.2, "parameters": map[string]interface{}{"rule": map[string]interface{}{"interval": []interface{}{map[string]interface{}{"triggerAtHour": 9}}}}},
		setNode("Do"),
	}, map[string]interface{}{"Every day": link("Do")})
	res, err := eng.InstallN8NWorkflow(context.Background(), "default", sched, flow.N8NInstallOptions{N8NImportOptions: flow.N8NImportOptions{ID: "daily"}})
	if err != nil || len(res.ScheduleIDs) != 1 {
		t.Fatalf("schedules: %v %+v", err, res)
	}
	s, err := eng.CronManager.GetSchedule(context.Background(), "default", res.ScheduleIDs[0])
	if err != nil || s.CronExpression != "0 0 9 * * *" || !strings.Contains(s.Input, `"__trigger"`) {
		t.Fatalf("schedule: %v %+v", err, s)
	}

	bad := n8nWorkflow([]map[string]interface{}{
		{"name": "Start", "type": "n8n-nodes-base.manualTrigger", "typeVersion": 1},
		{"name": "Sheets", "type": "n8n-nodes-base.acmeCrm", "typeVersion": 4},
	}, map[string]interface{}{"Start": link("Sheets")})
	res, err = eng.InstallN8NWorkflow(context.Background(), "default", bad, flow.N8NInstallOptions{})
	if !errors.Is(err, flow.ErrN8NNotRunnable) || res.Report.Runnable {
		t.Fatalf("want ErrN8NNotRunnable, got %v", err)
	}
	if !flow.N8NNodeSupported("n8n-nodes-base.set") || flow.N8NNodeSupported("n8n-nodes-base.acmeCrm") {
		t.Fatal("N8NNodeSupported")
	}
}

func waitStatus(t *testing.T, eng *flow.Engine, execID string, want flow.TaskStatus) *flow.Execution {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e, err := eng.GetExecution(context.Background(), "default", execID)
		if err == nil && e.Status == want {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("execution %s did not reach %s", execID, want)
	return nil
}

func waitingStep(t *testing.T, eng *flow.Engine, execID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		steps, _ := eng.GetExecutionSteps(context.Background(), "default", execID)
		for _, s := range steps {
			if s.Status == "WAITING" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no waiting step")
}

func TestN8NHumanInTheLoop(t *testing.T) {
	var tgBody string
	var tgMu = make(chan struct{}, 1)
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		tgBody = string(b)
		select {
		case tgMu <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer tg.Close()

	eng := flow.NewEngine(flow.NewMemoryStorage(), flow.NewRegistry())
	creds := flow.NewN8NStaticCredentials()
	creds.Set("telegramApi", "*", map[string]interface{}{"accessToken": "T"})
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	eng.EnableN8N(flow.N8NOptions{Credentials: creds, ResumeBaseURL: srv.URL + "/n8n", ResumeSecret: "s3cret",
		Outbound: flow.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{"telegram": tg.URL}})
	mux.Handle("/n8n/", http.StripPrefix("/n8n", eng.N8NHandler(flow.N8NHandlerOptions{Namespace: "default", ResponseTimeout: 10 * time.Second})))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.StartWorkerWithOptions(ctx, "default", "w", flow.WorkerOptions{Concurrency: 2, PollInterval: 5 * time.Millisecond})

	// 1) Telegram approval.
	ask := map[string]interface{}{"name": "Ask", "type": "n8n-nodes-base.telegram", "typeVersion": 1.2,
		"parameters":  map[string]interface{}{"operation": "sendAndWait", "chatId": "1", "message": "Deploy?", "approvalOptions": map[string]interface{}{"values": map[string]interface{}{"approvalType": "double"}}},
		"credentials": map[string]interface{}{"telegramApi": map[string]interface{}{"id": "1", "name": "tg"}}}
	install(t, eng, "approve", n8nWorkflow([]map[string]interface{}{
		{"name": "Start", "type": "n8n-nodes-base.manualTrigger", "typeVersion": 1}, ask,
		setNode("Result", [3]string{"approved", "boolean", "={{ $json.data.approved }}"}),
	}, map[string]interface{}{"Start": link("Ask"), "Ask": link("Result")}))
	execID, _ := eng.Execute(ctx, "default", "approve", []byte(`{}`))
	<-tgMu
	waitingStep(t, eng, execID)
	var msg struct {
		ReplyMarkup struct {
			InlineKeyboard [][]struct{ Text, URL string } `json:"inline_keyboard"`
		} `json:"reply_markup"`
	}
	_ = json.Unmarshal([]byte(tgBody), &msg)
	if len(msg.ReplyMarkup.InlineKeyboard[0]) != 2 {
		t.Fatalf("approval buttons: %s", tgBody)
	}
	forged := strings.Replace(msg.ReplyMarkup.InlineKeyboard[0][0].URL, "sig=", "sig=00", 1)
	req, _ := http.NewRequest("GET", forged, nil)
	if code, _ := do(t, req); code != 403 {
		t.Fatalf("forged resume link must be rejected: %d", code)
	}
	declineURL := msg.ReplyMarkup.InlineKeyboard[0][1].URL
	tampered := strings.Replace(declineURL, "approved=false", "approved=true", 1)
	req, _ = http.NewRequest("POST", tampered, nil)
	if code, _ := do(t, req); code != 403 {
		t.Fatalf("turning a decline link into an approval must fail: %d", code)
	}
	req, _ = http.NewRequest("GET", declineURL, nil) // link scanners only see a confirmation page
	if code, body := do(t, req); code != 200 || !strings.Contains(body, "Confirm") {
		t.Fatalf("GET should show a confirmation page: %d %s", code, body)
	}
	waitingStep(t, eng, execID)
	req, _ = http.NewRequest("POST", declineURL, nil)
	if code, body := do(t, req); code != 200 || !strings.Contains(body, "recorded") {
		t.Fatalf("approval click: %d %s", code, body)
	}
	e := waitStatus(t, eng, execID, flow.StatusCompleted)
	if items := flow.N8NOutputItems(e.Output); items[0].JSON["approved"] != false {
		t.Fatalf("approval result: %s", e.Output)
	}
	if !strings.Contains(msg.ReplyMarkup.InlineKeyboard[0][0].URL, "node=Ask") {
		t.Fatalf("approval links must be bound to their node: %s", msg.ReplyMarkup.InlineKeyboard[0][0].URL)
	}
	req, _ = http.NewRequest("POST", msg.ReplyMarkup.InlineKeyboard[0][0].URL, nil) // answered already
	if code, _ := do(t, req); code != 409 {
		t.Fatalf("reusing an answered approval link must fail: %d", code)
	}

	// 2) Multi-page form.
	install(t, eng, "signup", n8nWorkflow([]map[string]interface{}{
		{"name": "Form", "type": "n8n-nodes-base.formTrigger", "typeVersion": 2.2},
		{"name": "Details", "type": "n8n-nodes-base.form", "typeVersion": 1, "parameters": map[string]interface{}{
			"formFields": map[string]interface{}{"values": []interface{}{map[string]interface{}{"fieldLabel": "Company"}}}}},
		{"name": "Done", "type": "n8n-nodes-base.form", "typeVersion": 1, "parameters": map[string]interface{}{"operation": "completion", "completionTitle": "=Thanks {{ $('Form').item.json.name }}", "completionMessage": "=We will contact {{ $json.Company }}"}},
	}, map[string]interface{}{"Form": link("Details"), "Details": link("Done")}))
	req, _ = http.NewRequest("POST", srv.URL+"/n8n/form/signup", strings.NewReader(`{"name":"Ada"}`))
	code, body := do(t, req)
	var page struct {
		Page      map[string]interface{} `json:"page"`
		ResumeURL string                 `json:"resumeUrl"`
	}
	_ = json.Unmarshal([]byte(body), &page)
	if code != 200 || page.ResumeURL == "" || !strings.Contains(body, "Company") {
		t.Fatalf("form page: %d %s", code, body)
	}
	req, _ = http.NewRequest("POST", page.ResumeURL, strings.NewReader(`{"Company":"Acme"}`))
	if code, body = do(t, req); code != 200 || !strings.Contains(body, `"title":"Thanks Ada"`) || !strings.Contains(body, "We will contact Acme") {
		t.Fatalf("form completion: %d %s", code, body)
	}

	// 2b) Wait node resumed by a form submission.
	install(t, eng, "survey", n8nWorkflow([]map[string]interface{}{
		{"name": "Form", "type": "n8n-nodes-base.formTrigger", "typeVersion": 2.2},
		{"name": "More", "type": "n8n-nodes-base.wait", "typeVersion": 1.1, "parameters": map[string]interface{}{"resume": "form", "formTitle": "One more thing",
			"formFields": map[string]interface{}{"values": []interface{}{map[string]interface{}{"fieldLabel": "Role"}}}}},
		{"name": "Done", "type": "n8n-nodes-base.form", "typeVersion": 1, "parameters": map[string]interface{}{"operation": "completion", "completionTitle": "=Bye {{ $json.Role }}"}},
	}, map[string]interface{}{"Form": link("More"), "More": link("Done")}))
	req, _ = http.NewRequest("POST", srv.URL+"/n8n/form/survey", strings.NewReader(`{"name":"Ada"}`))
	code, body = do(t, req)
	page.ResumeURL = ""
	_ = json.Unmarshal([]byte(body), &page)
	if code != 200 || page.ResumeURL == "" || !strings.Contains(body, "One more thing") || !strings.Contains(body, "Role") {
		t.Fatalf("wait form page: %d %s", code, body)
	}
	req, _ = http.NewRequest("POST", page.ResumeURL, strings.NewReader(`{"Role":"CTO"}`))
	if code, body = do(t, req); code != 200 || !strings.Contains(body, `"title":"Bye CTO"`) {
		t.Fatalf("wait form completion: %d %s", code, body)
	}

	// 3) Wait for webhook via $execution.resumeUrl.
	install(t, eng, "callback", n8nWorkflow([]map[string]interface{}{
		{"name": "Start", "type": "n8n-nodes-base.manualTrigger", "typeVersion": 1},
		setNode("Remember", [3]string{"url", "string", "={{ $execution.resumeUrl }}"}),
		{"name": "Wait", "type": "n8n-nodes-base.wait", "typeVersion": 1.1, "parameters": map[string]interface{}{"resume": "webhook", "httpMethod": "POST"}},
		setNode("Got", [3]string{"status", "string", "={{ $json.body.status }}"}),
	}, map[string]interface{}{"Start": link("Remember"), "Remember": link("Wait"), "Wait": link("Got")}))
	execID, _ = eng.Execute(ctx, "default", "callback", []byte(`{}`))
	waitingStep(t, eng, execID)
	steps, _ := eng.GetExecutionSteps(ctx, "default", execID)
	var resumeURL string
	for _, s := range steps {
		if items := flow.N8NOutputItems(s.Output); len(items) == 1 && items[0].JSON["url"] != nil {
			resumeURL = items[0].JSON["url"].(string)
		}
	}
	req, _ = http.NewRequest("POST", resumeURL, strings.NewReader(`{"status":"done"}`))
	req.Header.Set("Content-Type", "application/json")
	if code, _ := do(t, req); code != 200 {
		t.Fatalf("resume: %d", code)
	}
	e = waitStatus(t, eng, execID, flow.StatusCompleted)
	if items := flow.N8NOutputItems(e.Output); items[0].JSON["status"] != "done" {
		t.Fatalf("wait webhook: %s", e.Output)
	}
}
