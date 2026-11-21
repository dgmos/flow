package n8n

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dgmos/flow/internal/models"
)

// hookEnv runs workflows behind the n8n handler and collects the items
// each execution's trigger emitted.
type hookEnv struct {
	*mcpEnv
}

func (e *hookEnv) post(t *testing.T, path, method string, headers map[string]string, body string) (int, string) {
	req, _ := http.NewRequest(method, e.srv.URL+"/webhook/"+path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// triggered returns the trigger outputs of all executions of a workflow.
func (e *hookEnv) triggered(t *testing.T, wfID, node string) [][]map[string]interface{} {
	t.Helper()
	var out [][]map[string]interface{}
	if wf, err := e.eng.WorkflowDefinition(context.Background(), "ns", wfID); err == nil {
		for _, n := range wf.Nodes {
			if n.Name == node {
				node = n.ID
			}
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out = nil
		pending := false
		execs, _ := e.eng.ListExecutions(context.Background(), "ns")
		for _, x := range execs {
			if x.WorkflowID != wfID {
				continue
			}
			steps, _ := e.eng.GetExecutionSteps(context.Background(), "ns", x.ID)
			found := false
			for _, s := range steps {
				if s.NodeID == node && s.Status == models.TaskCompleted {
					var items []map[string]interface{}
					for _, it := range OutputItems(s.Output) {
						items = append(items, it.JSON)
					}
					out = append(out, items)
					found = true
				}
			}
			pending = pending || !found
		}
		if !pending || time.Now().After(deadline) {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestServiceWebhookTriggers(t *testing.T) {
	creds := NewStaticCredentials()
	creds.Set("slackApi", "*", map[string]interface{}{"accessToken": "xoxb", "signatureSecret": "slk"})
	creds.Set("stripeApi", "*", map[string]interface{}{"secretKey": "sk", "signatureSecret": "whsec"})
	creds.Set("shopifyApi", "*", map[string]interface{}{"sharedSecret": "shp"})
	creds.Set("hubspotDeveloperApi", "*", map[string]interface{}{"clientSecret": "hs"})
	env := &hookEnv{newMCPEnv(t, creds)}
	trig := func(id, typ string, params map[string]interface{}, cred string) {
		n := map[string]interface{}{"name": "T", "type": "n8n-nodes-base." + typ, "typeVersion": 1, "webhookId": id, "parameters": params}
		if typ == "typeformTrigger" {
			n["typeVersion"] = 1.1
		}
		if cred != "" {
			n = credNode(n, cred)
		}
		data := wfJSON(t, []map[string]interface{}{n, nd("Out", "noOp", 1, nil)}, "T->Out")
		_, rep, _ := Import(data, ImportOptions{})
		if !rep.Runnable || rep.Triggers[0].Kind != "webhook" || rep.Triggers[0].Path != id {
			t.Fatalf("%s: %+v %+v", typ, rep.Blocking, rep.Triggers)
		}
		env.register(t, id, data)
	}
	now := strconv.FormatInt(time.Now().Unix(), 10)

	// Slack: signature, URL verification, event and channel filters.
	trig("slack", "slackTrigger", map[string]interface{}{"trigger": []interface{}{"message"}, "channelId": map[string]interface{}{"__rl": true, "mode": "id", "value": "C1"}}, "slackApi")
	slack := func(body string) (int, string) {
		return env.post(t, "slack", "POST", map[string]string{"Content-Type": "application/json", "X-Slack-Request-Timestamp": now,
			"X-Slack-Signature": "v0=" + hexHMAC256("slk", []byte("v0:"+now+":"+body))}, body)
	}
	if code, body := slack(`{"type":"url_verification","challenge":"abc"}`); code != 200 || !strings.Contains(body, `"challenge":"abc"`) {
		t.Fatalf("slack challenge: %d %s", code, body)
	}
	slack(`{"event":{"type":"message","channel":"C2","text":"other channel"}}`)
	slack(`{"event":{"type":"reaction_added","channel":"C1"}}`)
	slack(`{"event":{"type":"message","channel":"C1","text":"hi"}}`)
	if code, _ := env.post(t, "slack", "POST", map[string]string{"X-Slack-Request-Timestamp": now, "X-Slack-Signature": "v0=bad"}, `{}`); code != 401 {
		t.Fatalf("slack bad signature: %d", code)
	}

	// Stripe: t=…,v1=… signature and event filter.
	trig("stripe", "stripeTrigger", map[string]interface{}{"events": []interface{}{"charge.succeeded"}}, "stripeApi")
	stripe := func(body string) int {
		code, _ := env.post(t, "stripe", "POST", map[string]string{"Content-Type": "application/json",
			"Stripe-Signature": "t=" + now + ",v1=" + hexHMAC256("whsec", []byte(now+"."+body))}, body)
		return code
	}
	stripe(`{"id":"evt_1","type":"charge.failed"}`)
	stripe(`{"id":"evt_2","type":"charge.succeeded"}`)

	// Typeform: simplified answers keyed by question title.
	trig("typeform", "typeformTrigger", map[string]interface{}{"formId": "f1"}, "")
	env.post(t, "typeform", "POST", map[string]string{"Content-Type": "application/json"},
		`{"form_response":{"definition":{"fields":[{"id":"q1","title":"Your {{name}}?"},{"id":"q2","title":"Plan"}]},"answers":[{"type":"text","text":"Ada","field":{"id":"q1"}},{"type":"choice","choice":{"label":"Pro"},"field":{"id":"q2"}}]}}`)

	// Shopify: base64 HMAC and topic.
	trig("shopify", "shopifyTrigger", map[string]interface{}{"topic": "orders/create"}, "shopifyApi")
	order := `{"id":1001}`
	env.post(t, "shopify", "POST", map[string]string{"Content-Type": "application/json", "X-Shopify-Topic": "orders/create", "X-Shopify-Shop-Domain": "s.myshopify.com",
		"X-Shopify-Api-Version": "2024-07", "X-Shopify-Hmac-Sha256": b64HMAC256("shp", []byte(order))}, order)

	// HubSpot: one item per event with typed object ids.
	trig("hubspot", "hubspotTrigger", map[string]interface{}{}, "hubspotDeveloperApi")
	events := `[{"subscriptionType":"contact.creation","objectId":7},{"subscriptionType":"deal.propertyChange","objectId":8}]`
	sum := sha256Hex("hs" + events)
	env.post(t, "hubspot", "POST", map[string]string{"Content-Type": "application/json", "X-Hubspot-Signature": sum}, events)

	// Trello: HEAD validation answers without running.
	trig("trello", "trelloTrigger", map[string]interface{}{"id": "board1"}, "")
	if code, _ := env.post(t, "trello", "HEAD", nil, ""); code != 200 {
		t.Fatalf("trello HEAD: %d", code)
	}

	check := func(wf string, want ...string) {
		t.Helper()
		got := env.triggered(t, wf, "T")
		var flat []string
		for _, run := range got {
			b, _ := json.Marshal(run)
			flat = append(flat, string(b))
		}
		if len(flat) != len(want) {
			t.Fatalf("%s: %d executions %v, want %d", wf, len(flat), flat, len(want))
		}
		for i, w := range want {
			if !strings.Contains(flat[i], w) {
				t.Fatalf("%s run %d: %s does not contain %s", wf, i, flat[i], w)
			}
		}
	}
	check("slack", `"text":"hi"`)
	check("stripe", `"id":"evt_2"`)
	check("typeform", `"Your [name]?":"Ada"`)
	check("typeform", `"Plan":"Pro"`)
	check("shopify", `"id":1001`)
	check("hubspot", `"contactId":7`)
	check("hubspot", `"dealId":8`)
	check("trello")
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestServiceWebhookRegistration(t *testing.T) {
	var ghSecret string
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.method == "POST" && r.path == "/repos/octo/app/hooks":
			var b map[string]interface{}
			_ = json.Unmarshal([]byte(r.body), &b)
			ghSecret = toString(asMap(b["config"])["secret"])
			return 201, "application/json", map[string]interface{}{"id": 55}
		case r.method == "DELETE" && r.path == "/repos/octo/app/hooks/55":
			return 204, "application/json", ""
		case r.method == "POST" && r.path == "/v1/webhook_endpoints":
			return 200, "application/json", map[string]interface{}{"id": "we_1", "secret": "whsec_reg", "status": "enabled"}
		}
		return 404, "application/json", map[string]interface{}{}
	})
	creds := NewStaticCredentials()
	creds.Set("githubApi", "*", map[string]interface{}{"accessToken": "ghp"})
	creds.Set("stripeApi", "*", map[string]interface{}{"secretKey": "sk"})
	env := &hookEnv{newMCPEnv(t, creds)}
	env.opts.PollState = &memPollState{m: map[string]string{}}
	env.opts.APIBaseURLs = map[string]string{"github": f.srv.URL, "stripe": f.srv.URL}
	gh := wfJSON(t, []map[string]interface{}{credNode(map[string]interface{}{"name": "T", "type": "n8n-nodes-base.githubTrigger", "typeVersion": 1, "webhookId": "gh",
		"parameters": map[string]interface{}{"owner": map[string]interface{}{"__rl": true, "mode": "name", "value": "octo"},
			"repository": map[string]interface{}{"__rl": true, "mode": "name", "value": "app"}, "events": []interface{}{"push"}}}, "githubApi"), nd("Out", "noOp", 1, nil)}, "T->Out")
	st := wfJSON(t, []map[string]interface{}{credNode(map[string]interface{}{"name": "T", "type": "n8n-nodes-base.stripeTrigger", "typeVersion": 1, "webhookId": "st",
		"parameters": map[string]interface{}{"events": []interface{}{"*"}}}, "stripeApi"), nd("Out", "noOp", 1, nil)}, "T->Out")
	env.register(t, "gh", gh)
	env.register(t, "st", st)
	for _, id := range []string{"gh", "st"} {
		wf, _ := env.eng.WorkflowDefinition(context.Background(), "ns", id)
		regs := RegisterWebhooks(context.Background(), env.opts, "ns", wf, "https://hooks.example.com/n8n")
		if len(regs) != 1 || regs[0].Error != "" || regs[0].URL != "https://hooks.example.com/n8n/webhook/"+id {
			t.Fatalf("%s registration: %+v", id, regs)
		}
	}
	if b := f.find(t, "POST", "/repos/octo/app/hooks").body; !strings.Contains(b, `"url":"https://hooks.example.com/n8n/webhook/gh"`) || !strings.Contains(b, `"events":["push"]`) || len(ghSecret) != 64 {
		t.Fatalf("github hook body: %s", b)
	}
	if b := f.find(t, "POST", "/v1/webhook_endpoints").body; !strings.Contains(b, "enabled_events%5B%5D=%2A") {
		t.Fatalf("stripe endpoint body: %s", b)
	}
	push := `{"ref":"refs/heads/main","action":null}`
	if code, _ := env.post(t, "gh", "POST", map[string]string{"Content-Type": "application/json", "X-Hub-Signature-256": "sha256=" + hexHMAC256("wrong", []byte(push))}, push); code != 401 {
		t.Fatalf("github accepted a bad signature: %d", code)
	}
	env.post(t, "gh", "POST", map[string]string{"Content-Type": "application/json", "X-GitHub-Event": "push", "X-Hub-Signature-256": "sha256=" + hexHMAC256(ghSecret, []byte(push))}, push)
	now := strconv.FormatInt(time.Now().Unix(), 10)
	ev := `{"id":"evt_9","type":"invoice.paid"}`
	env.post(t, "st", "POST", map[string]string{"Content-Type": "application/json", "Stripe-Signature": "t=" + now + ",v1=" + hexHMAC256("whsec_reg", []byte(now+"."+ev))}, ev)
	if got := env.triggered(t, "gh", "T"); len(got) != 1 || asMap(got[0][0]["body"])["ref"] != "refs/heads/main" || asMap(got[0][0]["headers"])["x-github-event"] != "push" {
		t.Fatalf("github delivery: %v", got)
	}
	if got := env.triggered(t, "st", "T"); len(got) != 1 || got[0][0]["id"] != "evt_9" {
		t.Fatalf("stripe delivery: %v", got)
	}
	wf, _ := env.eng.WorkflowDefinition(context.Background(), "ns", "gh")
	if err := UnregisterWebhooks(context.Background(), env.opts, "ns", wf); err != nil {
		t.Fatal(err)
	}
	f.find(t, "DELETE", "/repos/octo/app/hooks/55")
}
