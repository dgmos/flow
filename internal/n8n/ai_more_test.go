package n8n

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// aiFake answers chat completions with "echo: <last message>", embeddings
// with fakeEmbedding and Cohere rerank by preferring documents with "cat".
func aiFake(t *testing.T) (*httptest.Server, *sync.Map) {
	seen := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(b, &body)
		seen.Store(r.URL.Path, map[string]string{"query": r.URL.RawQuery, "body": string(b), "auth": r.Header.Get("Authorization"), "apikey": r.Header.Get("api-key")})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			msgs := asList(body["messages"])
			last := toString(asMap(msgs[len(msgs)-1])["content"])
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": map[string]interface{}{"role": "assistant", "content": "echo: " + last}}}})
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			var data []interface{}
			for i, in := range asList(body["input"]) {
				data = append(data, map[string]interface{}{"index": i, "embedding": fakeEmbedding(toString(in))})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
		case strings.HasSuffix(r.URL.Path, "/v2/rerank"):
			var res []interface{}
			for i, d := range asList(body["documents"]) {
				score := 0.1
				if strings.Contains(toString(d), "cat") {
					score = 0.9
				}
				res = append(res, map[string]interface{}{"index": i, "relevance_score": score})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": res})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestMoreModelsRerankerAndLangChainCode(t *testing.T) {
	srv, seen := aiFake(t)
	creds := NewStaticCredentials()
	creds.Set("azureOpenAiApi", "*", map[string]interface{}{"apiKey": "az", "endpoint": srv.URL, "apiVersion": "2024-06-01"})
	creds.Set("huggingFaceApi", "*", map[string]interface{}{"apiKey": "hf", "url": srv.URL})
	creds.Set("ollamaApi", "*", map[string]interface{}{"baseUrl": srv.URL})
	creds.Set("openAiApi", "*", map[string]interface{}{"apiKey": "k", "url": srv.URL})
	creds.Set("cohereApi", "*", map[string]interface{}{"apiKey": "co", "url": srv.URL})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}})
	chain := func(name, text string) map[string]interface{} {
		return lc(name, "chainLlm", 1.4, map[string]interface{}{"promptType": "define", "text": text})
	}
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		chain("Azure", "hi azure"), withCred(lc("AzureModel", "lmChatAzureOpenAi", 1, map[string]interface{}{"model": "gpt4o-deploy"}), "azureOpenAiApi"),
		chain("HF", "hi hf"), withCred(lc("HFModel", "lmOpenHuggingFaceInference", 1, map[string]interface{}{"model": "Qwen/Qwen2.5"}), "huggingFaceApi"),
		chain("Ollama", "hi ollama"), withCred(lc("OllamaModel", "lmOllama", 1, nil), "ollamaApi"),
		nd("Docs", "code", 2, map[string]interface{}{"jsCode": "return [{json:{text:'dogs bark'}},{json:{text:'the cat sleeps'}}];"}),
		lc("Store", "vectorStoreInMemory", 1.3, map[string]interface{}{"mode": "insert", "memoryKey": "rr", "clearStore": true}),
		lc("Loader", "documentDefaultDataLoader", 1, map[string]interface{}{"jsonMode": "expressionData", "jsonData": "={{ $json.text }}"}),
		withCred(lc("Emb1", "embeddingsOpenAi", 1.2, nil), "openAiApi"),
		lc("Find", "vectorStoreInMemory", 1.3, map[string]interface{}{"mode": "load", "memoryKey": "rr", "prompt": "dogs", "topK": 2, "useReranker": true}),
		withCred(lc("Emb2", "embeddingsOpenAi", 1.2, nil), "openAiApi"),
		withCred(lc("Rerank", "rerankerCohere", 1, nil), "cohereApi"),
		lc("LC", "code", 1, map[string]interface{}{"code": map[string]interface{}{"execute": map[string]interface{}{"code": `
const { PromptTemplate } = require('@langchain/core/prompts');
const prompt = PromptTemplate.fromTemplate('Tell me about {topic}');
const llm = await this.getInputConnectionData('ai_languageModel', 0);
const out = await prompt.pipe(llm).invoke({ topic: 'flow' });
return [{ json: { output: out.content, n: this.getInputData().length } }];`}}}),
		withCred(lc("LCModel", "lmChatOpenAi", 1, nil), "openAiApi"),
	}, "Start->Azure", "AzureModel~ai_languageModel->Azure", "Start->HF", "HFModel~ai_languageModel->HF", "Start->Ollama", "OllamaModel~ai_languageModel->Ollama",
		"Start->Docs", "Docs->Store", "Loader~ai_document->Store", "Emb1~ai_embedding->Store", "Store->Find", "Emb2~ai_embedding->Find", "Rerank~ai_reranker->Find",
		"Start->LC", "LCModel~ai_languageModel->LC")
	_, rep, _ := Import(data, ImportOptions{})
	if !rep.Runnable {
		t.Fatalf("not runnable: %+v", rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	for name, want := range map[string]string{"Azure": "echo: hi azure", "HF": "echo: hi hf", "Ollama": "echo: hi ollama"} {
		if got := itemsOf(t, steps[name], 0); len(got) != 1 || got[0]["text"] != want {
			t.Fatalf("%s: %v (%s)", name, got, steps[name].Error)
		}
	}
	az, _ := seen.Load("/openai/deployments/gpt4o-deploy/chat/completions")
	if m, _ := az.(map[string]string); m["apikey"] != "az" || m["query"] != "api-version=2024-06-01" {
		t.Fatalf("azure request: %v", az)
	}
	found := itemsOf(t, steps["Find"], 0)
	// Load runs once per input item (2 inserted chunks), each reranked.
	if len(found) != 4 || asMap(found[0]["document"])["pageContent"] != "the cat sleeps" || found[0]["score"] != 0.9 || found[1]["score"] != 0.1 {
		t.Fatalf("reranked results: %v", found)
	}
	if got := itemsOf(t, steps["LC"], 0); len(got) != 1 || got[0]["output"] != "echo: Tell me about flow" || got[0]["n"] != 1.0 {
		t.Fatalf("langchain code: %v (%s)", got, steps["LC"].Error)
	}
}

func TestAgentTool(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		msgs := asList(body["messages"])
		last := asMap(msgs[len(msgs)-1])
		reply := func(m map[string]interface{}) {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": m}}})
		}
		switch {
		case len(asList(body["tools"])) == 0:
			reply(map[string]interface{}{"role": "assistant", "content": "inner: " + toString(last["content"])})
		case last["role"] == "tool":
			reply(map[string]interface{}{"role": "assistant", "content": "outer got " + toString(last["content"])})
		default:
			fn := asMap(asMap(asList(body["tools"])[0])["function"])
			if fn["name"] != "Researcher" || !strings.Contains(mustJSON(fn["parameters"]), "Prompt__User_Message_") {
				t.Errorf("agent tool definition: %v", fn)
			}
			reply(map[string]interface{}{"role": "assistant", "tool_calls": []interface{}{map[string]interface{}{"id": "1", "type": "function",
				"function": map[string]interface{}{"name": "Researcher", "arguments": `{"Prompt__User_Message_":"find go news"}`}}}})
		}
	}))
	defer llm.Close()
	creds := NewStaticCredentials()
	creds.Set("openAiApi", "*", map[string]interface{}{"apiKey": "k", "url": llm.URL})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		lc("Agent", "agent", 2.2, map[string]interface{}{"promptType": "define", "text": "what is new in go?"}),
		withCred(lc("Model", "lmChatOpenAi", 1, nil), "openAiApi"),
		lc("Researcher", "agentTool", 2.2, map[string]interface{}{"toolDescription": "Researches topics", "options": map[string]interface{}{"systemMessage": "You research."}}),
		withCred(lc("Model2", "lmChatOpenAi", 1, nil), "openAiApi"),
	}, "Start->Agent", "Model~ai_languageModel->Agent", "Researcher~ai_tool->Agent", "Model2~ai_languageModel->Researcher")
	_, steps := h.run(t, data, "")
	if got := itemsOf(t, steps["Agent"], 0); len(got) != 1 || got[0]["output"] != "outer got inner: find go news" {
		t.Fatalf("agent tool: %v (%s)", got, steps["Agent"].Error)
	}
}
