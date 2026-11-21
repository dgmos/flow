package n8n

import (
	"strings"
	"testing"
)

func TestVlmRunNode(t *testing.T) {
	var base string
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch r.path {
		case "/files":
			return 200, "application/json", map[string]interface{}{"id": "file1"}
		case "/document/generate", "/video/generate":
			return 200, "application/json", map[string]interface{}{"id": "pred1", "status": "completed", "response": map[string]interface{}{"total": 9}}
		case "/agent/files/presigned-url":
			return 200, "application/json", map[string]interface{}{"url": base + "/upload/x", "public_url": "https://cdn/x.pdf", "content_type": "application/pdf"}
		case "/upload/x":
			return 200, "text/plain", ""
		case "/agent/agent/execute":
			return 200, "application/json", map[string]interface{}{"id": "exec1", "status": "pending"}
		case "/agent/openai/chat/completions":
			return 200, "application/json", map[string]interface{}{"choices": []interface{}{map[string]interface{}{"index": 0, "message": map[string]interface{}{"role": "assistant", "content": `{"a":1}`}}}}
		case "/agent/artifacts":
			return 200, "image/jpeg", "JPEGBYTES"
		}
		return 404, "application/json", map[string]interface{}{"detail": "nope"}
	})
	base = f.srv.URL
	creds := map[string]map[string]interface{}{"vlmRunApi": {"apiKey": "vk", "apiBaseUrl": "https://api.vlm.run/v1", "agentBaseUrl": f.srv.URL + "/agent"}}
	const typ = "@vlm-run/n8n-nodes-vlmrun.vlmRun"

	// Default operation: document extraction (upload + generate).
	out := communityRun(t, f, "vlmRun", typ, 1, map[string]interface{}{"domain": "document.invoice"}, creds, "%PDF-1.4", "inv.pdf")
	if out[0].JSON["id"] != "pred1" {
		t.Fatalf("document: %+v", out)
	}
	if up := f.find(t, "POST", "/files"); up.auth != "Bearer vk" || !strings.Contains(up.body, `filename="inv.pdf"`) {
		t.Fatalf("upload: %+v", up)
	}
	if b := jsonBody(t, f.find(t, "POST", "/document/generate")); b["file_id"] != "file1" || b["domain"] != "document.invoice" || b["model"] != "vlm-1" || b["batch"] != false {
		t.Fatalf("generate body: %v", b)
	}

	// Agent execution with a presigned upload.
	out = communityRun(t, f, "vlmRun", typ, 1, map[string]interface{}{"operation": "executeAgent", "agentPrompt": "Summarize", "agentCallbackUrl": "https://hook"}, creds, "%PDF-1.4", "a.pdf")
	if out[0].JSON["id"] != "exec1" || f.find(t, "PUT", "/upload/x").body != "%PDF-1.4" {
		t.Fatalf("agent: %+v", out)
	}
	if b := jsonBody(t, f.find(t, "POST", "/agent/agent/execute")); asMap(b["inputs"])["url"] != "https://cdn/x.pdf" || asMap(b["config"])["prompt"] != "Summarize" || b["callback_url"] != "https://hook" {
		t.Fatalf("agent body: %v", b)
	}

	// Chat completion with image URLs and JSON response format.
	out = communityRun(t, f, "vlmRun", typ, 1, map[string]interface{}{"operation": "chatCompletion", "inputType": "image",
		"prompt":         map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "user", "content": "Describe"}}},
		"imageUrls":      map[string]interface{}{"url": []interface{}{map[string]interface{}{"url": "https://img/1.jpg"}}},
		"responseFormat": `{"schema":{"type":"object"}}`}, creds, "", "")
	choice := asMap(asList(out[0].JSON["choices"])[0])
	if asMap(asMap(choice["message"])["content"])["a"] != 1.0 {
		t.Fatalf("chat: %+v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/agent/openai/chat/completions"))
	parts := asList(asMap(asList(b["messages"])[0])["content"])
	if b["model"] != "vlmrun-orion-1:auto" || len(parts) != 2 || asMap(b["response_format"])["type"] != "json_schema" || asMap(b["response_format"])["strict"] != true {
		t.Fatalf("chat body: %v", b)
	}

	// Artifacts come back as binary data.
	out = communityRun(t, f, "vlmRun", typ, 1, map[string]interface{}{"operation": "artifacts", "objectId": "img_a1b2c3", "sessionId": "s1"}, creds, "", "")
	if bin := asMap(out[0].Binary["data"]); bin["fileName"] != "img_a1b2c3.jpg" || bin["mimeType"] != "image/jpeg" || f.find(t, "GET", "/agent/artifacts").query != "object_id=img_a1b2c3&session_id=s1" {
		t.Fatalf("artifact: %+v", out)
	}
}
