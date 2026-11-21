package n8n

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/models"
	"github.com/dgmos/flow/internal/node"
)

func TestContinueOnFailKeepsSuccessfulItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") == "2" {
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":` + r.URL.Query().Get("id") + `}`))
	}))
	defer srv.Close()
	h := newHarness(t, &Options{Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}})
	call := nd("Call", "httpRequest", 4.2, map[string]interface{}{"url": "=" + srv.URL + "/?id={{ $json.id }}"})
	call["onError"] = "continueRegularOutput"
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		nd("Ids", "code", 2, map[string]interface{}{"jsCode": "return [1,2,3].map(id => ({json:{id}}));"}),
		call,
		nd("Tag", "set", 3.4, map[string]interface{}{"includeOtherFields": true, "assignments": map[string]interface{}{"assignments": []interface{}{
			assign("src", "number", "={{ $('Ids').item.json.id }}")}}}),
	}, "Start->Ids", "Ids->Call", "Call->Tag")
	_, steps := h.run(t, data, "")
	got := itemsOf(t, steps["Tag"], 0)
	if len(got) != 3 || got[0]["ok"] != 1.0 || got[2]["ok"] != 3.0 || !strings.Contains(toString(got[1]["error"]), "500") {
		t.Fatalf("per-item errors: %v", got)
	}
	if got[0]["src"] != 1.0 || got[1]["src"] != 2.0 || got[2]["src"] != 3.0 {
		t.Fatalf("paired items after per-item execution: %v", got)
	}
}

func TestDurableWaitFreesWorker(t *testing.T) {
	old := durableWaitThreshold
	durableWaitThreshold = 10 * time.Millisecond
	defer func() { durableWaitThreshold = old }()
	h := newHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		nd("Wait", "wait", 1.1, map[string]interface{}{"amount": 0.2, "unit": "seconds"}),
		nd("After", "set", 3.4, map[string]interface{}{"includeOtherFields": true, "assignments": map[string]interface{}{"assignments": []interface{}{assign("done", "boolean", true)}}}),
	}, "Start->Wait", "Wait->After")
	wf, _, _ := Import(data, ImportOptions{ID: "timer"})
	_ = h.eng.RegisterWorkflow(ctx, "ns", wf)
	id, _ := h.eng.StartWorkflow(ctx, "ns", "timer", `{"x":1}`)
	go h.eng.StartWorkerWithOptions(ctx, "ns", "w", engineOpts())
	var waiting bool
	for i := 0; i < 200 && !waiting; i++ {
		steps, _ := h.store.GetExecutionSteps(ctx, "ns", id)
		for _, s := range steps {
			if s.Status == models.TaskWaiting && s.DueAt != nil {
				waiting = true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("long wait should be stored as a WAITING timer step")
	}
	_ = h.eng.ReapTimeouts(ctx, "ns") // too early: must not resume
	if e, _ := h.store.GetExecution(ctx, "ns", id); e.Status != models.TaskRunning {
		t.Fatalf("resumed before due: %s", e.Status)
	}
	time.Sleep(250 * time.Millisecond)
	_ = h.eng.ReapTimeouts(ctx, "ns")
	for i := 0; i < 200; i++ {
		if e, _ := h.store.GetExecution(ctx, "ns", id); e.Status == models.TaskCompleted {
			if items := OutputItems(e.Output); items[0].JSON["done"] != true || items[0].JSON["x"] != 1.0 {
				t.Fatalf("output after timer: %s", e.Output)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timer step was not resumed")
}

func TestCredentialDomainBinding(t *testing.T) {
	if credentialHostAllowed("openAiApi", map[string]interface{}{"apiKey": "k"}, "evil.example.com") {
		t.Fatal("OpenAI key must not be sent to arbitrary hosts")
	}
	if !credentialHostAllowed("openAiApi", map[string]interface{}{"apiKey": "k"}, "api.openai.com") {
		t.Fatal("OpenAI key must work with OpenAI")
	}
	if !credentialHostAllowed("openAiApi", map[string]interface{}{"apiKey": "k", "url": "https://proxy.corp.com/v1"}, "proxy.corp.com") {
		t.Fatal("credential base URL must be allowed")
	}
	if credentialHostAllowed("httpHeaderAuth", map[string]interface{}{"allowedDomains": "api.acme.com"}, "evil.com") ||
		!credentialHostAllowed("httpHeaderAuth", map[string]interface{}{"allowedDomains": "api.acme.com"}, "api.acme.com") {
		t.Fatal("allowedDomains must be enforced")
	}
	if _, err := safeAddressList("a@x.com\r\nBcc: victim@y.com"); err == nil {
		t.Fatal("header injection must be rejected")
	}
}

func engineOpts() engine.WorkerOptions {
	return engine.WorkerOptions{Concurrency: 2, PollInterval: 2 * time.Millisecond}
}

// An AI connection drawn from a root node to its model (the wrong way) is
// reported instead of producing a workflow with a dangling edge.
func TestReversedAIConnectionIsBlocked(t *testing.T) {
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		lc("Agent", "agent", 2.2, map[string]interface{}{"promptType": "define", "text": "hi"}),
		lc("Model", "lmChatOpenAi", 1.2, nil),
	}, "Start->Agent", "Agent~ai_languageModel->Model")
	_, rep, err := Import(data, ImportOptions{})
	if err != nil || rep.Runnable || !strings.Contains(mustJSON(rep.Blocking), "not a sub-node") {
		t.Fatalf("err=%v runnable=%v blocking=%+v", err, rep.Runnable, rep.Blocking)
	}
}
