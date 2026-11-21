package n8n

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodoNode(t *testing.T) {
	const typ = "@decodo/n8n-nodes-decodo.decodo"
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.method != "POST" || r.path != "/scrape" || r.header.Get("x-integration") != "n8n" {
			t.Errorf("request %s %s %v", r.method, r.path, r.header)
		}
		if strings.Contains(r.body, "bad.io") {
			return 200, "", map[string]interface{}{"status": "failed", "status_code": 613, "message": "blocked"}
		}
		return 200, "", map[string]interface{}{"results": []interface{}{map[string]interface{}{"content": "x"}}}
	})
	bases := map[string]string{"decodo": ""}
	key := map[string]map[string]interface{}{"decodoApi": {"token": "VK-123"}}

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"url": "https://a.io", "geo": "US", "markdown": true}, key, nil)
	r := f.find(t, "POST", "/scrape")
	if b := r.json(t); b["target"] != "universal" || b["headless"] != "html" || b["geo"] != "US" || b["markdown"] != true || b["parse"] != nil {
		t.Fatalf("universal body %v", b)
	}
	if r.header.Get("Authorization") != "Bearer VK-123" || out[0].JSON["results"] == nil {
		t.Fatalf("api key auth %q", r.header.Get("Authorization"))
	}

	tok := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "google_search", "query": "go", "markdown": true}, map[string]map[string]interface{}{"decodoApi": {"token": tok}}, nil)
	r = f.find(t, "POST", "/scrape")
	// parse defaults to true for Google
	if b := r.json(t); b["target"] != "google_search" || b["query"] != "go" || b["parse"] != true || b["limit"] != 10.0 || b["headless"] != "html" || b["markdown"] != true {
		t.Fatalf("google body %v", b)
	}
	if r.header.Get("Authorization") != "Basic "+tok {
		t.Fatalf("token auth %q", r.header.Get("Authorization"))
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "amazon", "url": "https://amazon.com/dp/1"}, key, nil)
	if b := f.find(t, "POST", "/scrape").json(t); b["headless"] != nil || b["parse"] != true {
		t.Fatalf("amazon body %v", b)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "youtube_transcript", "video_id": "abc", "language_code": "en"}, key, nil)
	if b := f.find(t, "POST", "/scrape").json(t); b["query"] != "abc" || b["language_code"] != "en" {
		t.Fatalf("youtube body %v", b)
	}
	if e := cmtyErr(t, f, bases, typ, map[string]interface{}{"url": "https://bad.io"}, key); !strings.Contains(e, "(613): blocked") {
		t.Fatalf("failed status: %q", e)
	}
	if !aiSubNodeSupported("@decodo/n8n-nodes-decodo.decodoTool") {
		t.Fatal("decodoTool should be a supported AI tool")
	}
}
