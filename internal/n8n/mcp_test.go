package n8n

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/node"
	"github.com/dgmos/flow/internal/storage/memory"
)

// mcpEnv runs an engine with the n8n handler: one workflow serves an MCP
// server (an "adder" Code tool), others can call it.
type mcpEnv struct {
	eng   *engine.Engine
	store *memory.MemoryStorage
	srv   *httptest.Server
	opts  *Options
}

func newMCPEnv(t *testing.T, creds *StaticCredentials) *mcpEnv {
	store := memory.NewMemoryStorage()
	reg := engine.NewRegistry()
	opts := &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}}
	Register(reg, opts)
	eng := engine.NewEngine(store, reg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go eng.StartWorkerWithOptions(ctx, "ns", "w", engine.WorkerOptions{Concurrency: 4, PollInterval: 2 * time.Millisecond})
	srv := httptest.NewServer(NewHandler(eng, opts, HandlerOptions{Namespace: "ns", ResponseTimeout: 10 * time.Second}))
	t.Cleanup(srv.Close)
	env := &mcpEnv{eng: eng, store: store, srv: srv, opts: opts}
	server := wfJSON(t, []map[string]interface{}{
		lc("MCP", "mcpTrigger", 2, map[string]interface{}{"path": "calc", "authentication": "bearerAuth"}),
		lc("adder", "toolCode", 1.1, map[string]interface{}{"name": "adder", "description": "Adds two numbers written as a+b", "jsCode": "const [a, b] = query.split('+'); return String(Number(a) + Number(b));"}),
	}, "adder~ai_tool->MCP")
	server = withNodeCred(t, server, "MCP", "httpBearerAuth")
	env.register(t, "mcp-server", server)
	return env
}

func withNodeCred(t *testing.T, data []byte, nodeName, credType string) []byte {
	var wf map[string]interface{}
	_ = json.Unmarshal(data, &wf)
	for _, n := range asList(wf["nodes"]) {
		if m := asMap(n); m["name"] == nodeName {
			m["credentials"] = map[string]interface{}{credType: map[string]interface{}{"id": "1", "name": credType}}
		}
	}
	b, _ := json.Marshal(wf)
	return b
}

func (e *mcpEnv) register(t *testing.T, id string, data []byte) {
	wf, rep, err := Import(data, ImportOptions{ID: id})
	if err != nil || !rep.Runnable {
		t.Fatalf("import %s: %v %+v", id, err, rep.Blocking)
	}
	if err := e.eng.RegisterWorkflow(context.Background(), "ns", wf); err != nil {
		t.Fatal(err)
	}
}

func rpc(t *testing.T, url, session string, body string) (map[string]interface{}, string) {
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	return m, resp.Header.Get("Mcp-Session-Id")
}

func TestMCPServerTrigger(t *testing.T) {
	creds := NewStaticCredentials()
	creds.Set("httpBearerAuth", "*", map[string]interface{}{"token": "secret"})
	env := newMCPEnv(t, creds)
	u := env.srv.URL + "/mcp/calc"

	init, session := rpc(t, u, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	if session == "" || asMap(asMap(init["result"])["serverInfo"])["name"] == nil {
		t.Fatalf("initialize: %v", init)
	}
	list, _ := rpc(t, u, session, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := asList(asMap(list["result"])["tools"])
	if len(tools) != 1 || asMap(tools[0])["name"] != "adder" || asMap(tools[0])["inputSchema"] == nil {
		t.Fatalf("tools/list: %v", list)
	}
	call, _ := rpc(t, u, session, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"adder","arguments":{"query":"2+3"}}}`)
	content := asList(asMap(call["result"])["content"])
	if len(content) != 1 || asMap(content[0])["text"] != "5" {
		t.Fatalf("tools/call: %v", call)
	}

	req, _ := http.NewRequest("POST", u, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing bearer token must be rejected: %v %v", resp, err)
	}
}

func TestMCPClientToolAgainstServer(t *testing.T) {
	creds := NewStaticCredentials()
	creds.Set("httpBearerAuth", "*", map[string]interface{}{"token": "secret"})
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		msgs := mustJSON(body["messages"])
		if strings.Contains(msgs, `"role":"tool"`) {
			last := asMap(asList(body["messages"])[len(asList(body["messages"]))-1])
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": map[string]interface{}{"role": "assistant", "content": "sum is " + toString(last["content"])}}}})
			return
		}
		name := toString(asMap(asMap(asList(body["tools"])[0])["function"])["name"])
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": map[string]interface{}{"role": "assistant",
			"tool_calls": []interface{}{map[string]interface{}{"id": "c1", "type": "function", "function": map[string]interface{}{"name": name, "arguments": `{"query":"20+22"}`}}}}}}})
	}))
	defer llm.Close()
	creds.Set("openAiApi", "*", map[string]interface{}{"apiKey": "k", "url": llm.URL})
	creds.Set("mcpClientHttpApi", "*", map[string]interface{}{"httpStreamUrl": "", "headers": "Authorization: Bearer secret"})
	env := newMCPEnv(t, creds)
	for _, transport := range []string{"httpStreamable", "sse"} {
		t.Run(transport, func(t *testing.T) {
			endpoint := env.srv.URL + "/mcp/calc"
			if transport == "sse" {
				endpoint += "/sse"
			}
			agent := wfJSON(t, []map[string]interface{}{
				nd("Start", "manualTrigger", 1, nil),
				lc("Agent", "agent", 1.7, map[string]interface{}{"promptType": "define", "text": "what is 20+22?"}),
				withCred(lc("Model", "lmChatOpenAi", 1, nil), "openAiApi"),
				withCred(lc("Calc MCP", "mcpClientTool", 1.2, map[string]interface{}{"endpointUrl": endpoint, "serverTransport": transport, "authentication": "bearerAuth"}), "httpBearerAuth"),
			}, "Start->Agent", "Model~ai_languageModel->Agent", "Calc MCP~ai_tool->Agent")
			h := &harness{eng: env.eng, store: env.store}
			_, steps := h.run(t, agent, "")
			if got := itemsOf(t, steps["Agent"], 0); len(got) != 1 || got[0]["output"] != "sum is 42" {
				t.Fatalf("agent via MCP (%s): %v (%s)", transport, got, steps["Agent"].Error)
			}
		})
	}

	// Community MCP Client node over HTTP; STDIO is disabled by default.
	creds.Set("mcpClientHttpApi", "*", map[string]interface{}{"httpStreamUrl": env.srv.URL + "/mcp/calc", "headers": "Authorization: Bearer secret"})
	creds.Set("mcpClientApi", "*", map[string]interface{}{"command": "npx", "args": "-y some-server"})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(map[string]interface{}{"name": "Exec", "type": "n8n-nodes-mcp.mcpClient", "typeVersion": 1, "parameters": map[string]interface{}{"connectionType": "http", "operation": "executeTool", "toolName": "adder", "toolParameters": `{"query":"1+1"}`}}, "mcpClientHttpApi"),
		credNode(map[string]interface{}{"name": "Stdio", "type": "n8n-nodes-mcp.mcpClient", "typeVersion": 1, "parameters": map[string]interface{}{}}, "mcpClientApi"),
	}, "Start->Exec")
	_, rep, _ := Import(data, ImportOptions{})
	if len(rep.Warnings) == 0 || !strings.Contains(fmt.Sprint(rep.Warnings), "AllowLocalProcesses") {
		t.Fatalf("STDIO MCP should be flagged at import: %+v", rep.Warnings)
	}
	h := &harness{eng: env.eng, store: env.store}
	_, steps := h.run(t, data, "")
	got := itemsOf(t, steps["Exec"], 0)
	if len(got) != 1 || !strings.Contains(mustJSON(got[0]["result"]), `"text":"2"`) {
		t.Fatalf("community executeTool: %v (%s)", got, steps["Exec"].Error)
	}
	// Official MCP Client node.
	official := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		withCred(lc("Call", "mcpClient", 1.1, map[string]interface{}{"endpointUrl": env.srv.URL + "/mcp/calc", "authentication": "bearerAuth",
			"tool": map[string]interface{}{"__rl": true, "mode": "list", "value": "adder"}, "parameters": map[string]interface{}{"value": map[string]interface{}{"query": "2+3"}}}), "httpBearerAuth"),
	}, "Start->Call")
	if _, rep, _ := Import(official, ImportOptions{}); !rep.Runnable {
		t.Fatalf("official MCP client not runnable: %+v", rep.Blocking)
	}
	_, steps = h.run(t, official, "")
	if got := itemsOf(t, steps["Call"], 0); len(got) != 1 || mustJSON(got[0]["content"]) != `[{"text":5,"type":"text"}]` {
		t.Fatalf("official MCP client: %v (%s)", got, steps["Call"].Error)
	}
	ec := &execCtx{ctx: context.Background(), opts: env.opts}
	if _, err := ec.mcpConnect(mcpConfig{transport: "stdio", command: "npx"}); err == nil || !strings.Contains(err.Error(), "AllowLocalProcesses") {
		t.Fatalf("STDIO must be refused by default: %v", err)
	}
}
