package n8n

import (
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

func TestAnthropicAndPerplexityNodes(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch r.path {
		case "/v1/messages":
			return 200, "application/json", map[string]interface{}{"id": "m1", "role": "assistant", "stop_reason": "end_turn",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hi "}, map[string]interface{}{"type": "text", "text": "there"}}}
		case "/chat/completions":
			return 200, "application/json", map[string]interface{}{"id": "p1", "created": 1, "citations": []interface{}{"https://a"},
				"choices": []interface{}{map[string]interface{}{"message": map[string]interface{}{"content": "answer"}}}}
		}
		return 404, "application/json", map[string]interface{}{}
	})
	creds := NewStaticCredentials()
	creds.Set("anthropicApi", "*", map[string]interface{}{"apiKey": "ak", "url": f.srv.URL})
	creds.Set("perplexityApi", "*", map[string]interface{}{"apiKey": "pk"})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{"perplexity": f.srv.URL}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		withCred(lc("Claude", "anthropic", 1, map[string]interface{}{"modelId": map[string]interface{}{"__rl": true, "mode": "list", "value": "claude-sonnet-4-5"},
			"messages": map[string]interface{}{"values": []interface{}{map[string]interface{}{"content": "hello"}}}, "options": map[string]interface{}{"system": "be nice", "includeMergedResponse": true}}), "anthropicApi"),
		credNode(map[string]interface{}{"name": "Pplx", "type": "n8n-nodes-base.perplexity", "typeVersion": 1, "parameters": map[string]interface{}{
			"messages": map[string]interface{}{"message": []interface{}{map[string]interface{}{"content": "q?"}}}, "simplify": true,
			"options": map[string]interface{}{"searchDomainFilter": "a.com, b.com", "searchRecency": "week"}}}, "perplexityApi"),
	}, "Start->Claude", "Start->Pplx")
	_, steps := h.run(t, data, "")
	got := itemsOf(t, steps["Claude"], 0)
	if len(got) != 1 || got[0]["merged_response"] != "hi there" || len(asList(got[0]["content"])) != 2 {
		t.Fatalf("anthropic: %v (%s)", got, steps["Claude"].Error)
	}
	if b := f.find(t, "POST", "/v1/messages").body; !strings.Contains(b, `"system":"be nice"`) || !strings.Contains(b, `"model":"claude-sonnet-4-5"`) {
		t.Fatalf("anthropic body: %s", b)
	}
	if got := itemsOf(t, steps["Pplx"], 0); len(got) != 1 || got[0]["message"] != "answer" || got[0]["id"] != "p1" {
		t.Fatalf("perplexity: %v (%s)", got, steps["Pplx"].Error)
	}
	if r := f.find(t, "POST", "/chat/completions"); r.auth != "Bearer pk" || !strings.Contains(r.body, `"search_domain_filter":["a.com","b.com"]`) || !strings.Contains(r.body, `"model":"sonar"`) {
		t.Fatalf("perplexity request: %+v", r)
	}
}
