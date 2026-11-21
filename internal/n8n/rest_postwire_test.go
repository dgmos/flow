package n8n

import (
	"testing"
)

func TestPostWireNode(t *testing.T) {
	var base string
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch r.path {
		case "/api/media/upload-url":
			return 200, "application/json", map[string]interface{}{"upload_url": base + "/storage/put", "path": "m/1.png"}
		case "/storage/put":
			return 200, "text/plain", ""
		case "/api/media/finalize":
			return 200, "application/json", map[string]interface{}{"media_url": "https://cdn.postwire.io/m/1.png", "content_type": "image/png"}
		case "/api/generate":
			return 200, "application/json", map[string]interface{}{"drafts": map[string]interface{}{"linkedin": map[string]interface{}{"text": "LI post"}, "x": map[string]interface{}{"text": "X post"}}}
		case "/api/post":
			return 200, "application/json", map[string]interface{}{"ok": true, "results": []interface{}{
				map[string]interface{}{"platform": "linkedin", "ok": true, "url": "https://li/1"}, map[string]interface{}{"platform": "x", "ok": true, "url": "https://x/1"}}}
		case "/api/schedule":
			return 200, "application/json", map[string]interface{}{"scheduled": map[string]interface{}{"id": "s1", "status": "queued"}}
		case "/api/me":
			return 200, "application/json", map[string]interface{}{"email": "me@x.com", "plan": "free"}
		}
		return 404, "application/json", map[string]interface{}{"error": "nope"}
	})
	base = f.srv.URL
	creds := map[string]map[string]interface{}{"postWireApi": {"apiKey": "pw_live_1"}}
	const typ = "n8n-nodes-postwire.postWire"

	// Publish (smart drafts) with an uploaded image.
	out := communityRun(t, f, "postWire", typ, 2, map[string]interface{}{"resource": "post", "operation": "publish", "platforms": []interface{}{"linkedin", "x"},
		"prompt": "Launch day", "mediaSource": "binary", "binaryPropertyName": "data", "options": map[string]interface{}{"firstComment": "Link in bio"}}, creds, "\x89PNG\r\n", "a.png")
	if len(out) != 2 || out[1].JSON["url"] != "https://x/1" {
		t.Fatalf("publish: %+v", out)
	}
	if put := f.find(t, "PUT", "/storage/put"); put.body != "\x89PNG\r\n" || put.auth != "" {
		t.Fatalf("storage put: %+v", put)
	}
	post := f.find(t, "POST", "/api/post")
	b := jsonBody(t, post)
	if post.auth != "Bearer pw_live_1" || b["photo_url"] != "https://cdn.postwire.io/m/1.png" || asMap(asMap(b["per_platform"])["x"])["text"] != "X post" ||
		asMap(asMap(b["options"])["linkedin"])["first_comment"] != "Link in bio" {
		t.Fatalf("post body: %v", b)
	}
	if g := jsonBody(t, f.find(t, "POST", "/api/generate")); g["prompt"] != "Launch day" || g["media_url"] != "https://cdn.postwire.io/m/1.png" {
		t.Fatalf("generate body: %v", g)
	}

	// Schedule from explicit drafts.
	out = communityRun(t, f, "postWire", typ, 2, map[string]interface{}{"resource": "post", "operation": "schedule", "platforms": []interface{}{"x"},
		"contentMode": "drafts", "drafts": `{"drafts":{"x":{"text":"hi"}}}`, "when": "time", "runAt": "2025-03-01T09:00:00Z", "mediaSource": "none"}, creds, "", "")
	if out[0].JSON["id"] != "s1" || asMap(asMap(out[0].JSON["drafts"])["x"])["text"] != "hi" {
		t.Fatalf("schedule: %+v", out)
	}
	if b := jsonBody(t, f.find(t, "POST", "/api/schedule")); b["run_at"] != "2025-03-01T09:00:00Z" {
		t.Fatalf("schedule body: %v", b)
	}

	// Generate only, and the v1 "me" operation.
	out = communityRun(t, f, "postWire", typ, 2, map[string]interface{}{"resource": "post", "operation": "generate", "platforms": []interface{}{"x"}, "prompt": "p"}, creds, "", "")
	if out[0].JSON["preview"] != "*x*\nX post" {
		t.Fatalf("generate: %+v", out)
	}
	out = communityRun(t, f, "postWire", typ, 1, map[string]interface{}{"operation": "me"}, creds, "", "")
	if out[0].JSON["email"] != "me@x.com" {
		t.Fatalf("me: %+v", out)
	}
}
