package n8n

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

func TestGoogleGeminiNode(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/img.png" && r.Header.Get("x-goog-api-key") != "gk" && !strings.HasPrefix(r.URL.Path, "/resumable") {
			t.Errorf("missing API key on %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/img.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNGDATA"))
		case strings.HasSuffix(r.URL.Path, ":generateContent"):
			var lastGen map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&lastGen)
			if strings.Contains(mustJSON(lastGen["generationConfig"]), "IMAGE") {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"candidates": []interface{}{map[string]interface{}{"content": map[string]interface{}{"parts": []interface{}{
					map[string]interface{}{"text": "here"}, map[string]interface{}{"inlineData": map[string]interface{}{"mimeType": "image/png", "data": base64.StdEncoding.EncodeToString([]byte("GEN"))}}}}}}})
				return
			}
			contents := asList(lastGen["contents"])
			parts := asList(asMap(contents[len(contents)-1])["parts"])
			text := toString(asMap(parts[len(parts)-1])["text"])
			if len(parts) > 1 {
				text = "saw " + toString(asMap(asMap(parts[0])["inlineData"])["mimeType"]) + ": " + text
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"candidates": []interface{}{map[string]interface{}{"content": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"text": text}}, "role": "model"}, "finishReason": "STOP"}}})
		case r.URL.Path == "/upload/v1beta/files":
			if r.Header.Get("X-Goog-Upload-Header-Content-Type") != "image/png" {
				t.Errorf("upload start headers: %v", r.Header)
			}
			w.Header().Set("X-Goog-Upload-URL", srv.URL+"/resumable/1")
			_, _ = w.Write([]byte("{}"))
		case r.URL.Path == "/resumable/1":
			b, _ := io.ReadAll(r.Body)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"file": map[string]interface{}{"name": "files/1", "uri": srv.URL + "/v1beta/files/1", "mimeType": "image/png", "sizeBytes": len(b), "state": "ACTIVE"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	creds := NewStaticCredentials()
	creds.Set("googlePalmApi", "*", map[string]interface{}{"apiKey": "gk", "host": srv.URL})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1", "localhost"}}})
	model := map[string]interface{}{"__rl": true, "mode": "list", "value": "models/gemini-2.5-flash"}
	gem := func(name string, p map[string]interface{}) map[string]interface{} {
		p["modelId"] = model
		return withCred(lc(name, "googleGemini", 1, p), "googlePalmApi")
	}
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		gem("Text", map[string]interface{}{"messages": map[string]interface{}{"values": []interface{}{map[string]interface{}{"content": "hello gemini"}}}, "options": map[string]interface{}{"systemMessage": "be brief"}}),
		gem("Analyze", map[string]interface{}{"resource": "image", "operation": "analyze", "text": "what is it?", "imageUrls": strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/img.png"}),
		gem("Generate", map[string]interface{}{"resource": "image", "operation": "generate", "prompt": "a cat"}),
		gem("Upload", map[string]interface{}{"resource": "file", "operation": "upload", "fileUrl": srv.URL + "/img.png"}),
		nd("Use", "set", 3.4, map[string]interface{}{"mode": "raw", "jsonOutput": `={"t": {{ JSON.stringify($json.content.parts[0].text) }} }`}),
	}, "Start->Text", "Text->Use", "Start->Analyze", "Start->Generate", "Start->Upload")
	_, rep, _ := Import(data, ImportOptions{})
	if !rep.Runnable {
		t.Fatalf("not runnable: %+v", rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	if got := itemsOf(t, steps["Use"], 0); len(got) != 1 || got[0]["t"] != "hello gemini" {
		t.Fatalf("text message: %v (%s)", got, steps["Text"].Error)
	}
	if got := itemsOf(t, steps["Analyze"], 0); len(got) != 1 || toString(asMap(asList(asMap(got[0]["content"])["parts"])[0])["text"]) != "saw image/png: what is it?" {
		t.Fatalf("image analyze: %v (%s)", got, steps["Analyze"].Error)
	}
	gen := OutputItems(steps["Generate"].Output)
	if len(gen) != 1 || toString(asMap(gen[0].Binary["data"])["data"]) != base64.StdEncoding.EncodeToString([]byte("GEN")) || gen[0].JSON["mimeType"] != "image/png" {
		t.Fatalf("image generate: %s (%s)", steps["Generate"].Output, steps["Generate"].Error)
	}
	if got := itemsOf(t, steps["Upload"], 0); len(got) != 1 || got[0]["state"] != "ACTIVE" || got[0]["sizeBytes"] != 7.0 {
		t.Fatalf("file upload: %v (%s)", got, steps["Upload"].Error)
	}
}

func TestVendorToolVariantsSupported(t *testing.T) {
	for _, typ := range []string{"@n8n/n8n-nodes-langchain.googleGeminiTool", "@n8n/n8n-nodes-langchain.anthropicTool", "@n8n/n8n-nodes-langchain.agentTool",
		"n8n-nodes-base.dataTableTool", "n8n-nodes-base.hubspotTool", "n8n-nodes-base.perplexityTool", "@apify/n8n-nodes-apify.apifyTool"} {
		if !Supported(typ) {
			t.Errorf("%s should be supported", typ)
		}
	}
	if Supported("@n8n/n8n-nodes-langchain.chainLlmTool") {
		t.Error("only vendor nodes have tool variants")
	}
}
