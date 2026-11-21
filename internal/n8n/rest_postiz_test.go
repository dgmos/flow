package n8n

import (
	"strings"
	"testing"
)

func TestPostizNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.path == "/public/v1/posts" && r.method == "POST":
			return 200, "application/json", []interface{}{map[string]interface{}{"postId": "p1", "integration": "i1"}}
		case r.path == "/public/v1/integrations":
			return 200, "application/json", []interface{}{map[string]interface{}{"id": "i1"}, map[string]interface{}{"id": "i2"}}
		case r.path == "/public/v1/upload":
			return 200, "application/json", map[string]interface{}{"id": "u1", "path": "https://cdn/x.png"}
		}
		return 200, "application/json", map[string]interface{}{"posts": []interface{}{}}
	})
	creds := map[string]map[string]interface{}{"postizApi": {"apiKey": "pz-key", "host": "https://api.postiz.com"}}
	const typ = "n8n-nodes-postiz.postiz"

	out := communityRun(t, f, "postiz", typ, 1, map[string]interface{}{"type": "schedule", "date": "2025-01-02T10:00:00", "shortLink": true,
		"posts": map[string]interface{}{"post": []interface{}{map[string]interface{}{"integrationId": "i1",
			"value":    map[string]interface{}{"contentItem": []interface{}{map[string]interface{}{"content": "Hello", "image": map[string]interface{}{"imageItem": []interface{}{map[string]interface{}{"id": "u1", "path": "https://cdn/x.png"}}}}}},
			"settings": map[string]interface{}{"setting": []interface{}{map[string]interface{}{"key": "title", "valueType": "string", "stringValue": "T"}, map[string]interface{}{"key": "n", "valueType": "json", "jsonValue": "[1]"}}},
		}}}}, creds, "", "")
	if len(out) != 1 || out[0].JSON["postId"] != "p1" {
		t.Fatalf("create: %+v", out)
	}
	r := f.find(t, "POST", "/public/v1/posts")
	b := jsonBody(t, r)
	post := asMap(asList(b["posts"])[0])
	val := asMap(asList(post["value"])[0])
	if r.auth != "pz-key" || b["type"] != "schedule" || b["shortLink"] != true || len(asList(b["tags"])) != 0 || asMap(post["integration"])["id"] != "i1" ||
		val["content"] != "Hello" || val["id"] != "" || len(asList(val["image"])) != 1 || asMap(post["settings"])["title"] != "T" || len(asList(asMap(post["settings"])["n"])) != 1 || post["group"] != "" {
		t.Fatalf("create request: %v", b)
	}

	out = communityRun(t, f, "postiz", typ, 1, map[string]interface{}{"operation": "getIntegrations"}, creds, "", "")
	if len(out) != 2 {
		t.Fatalf("integrations: %+v", out)
	}

	out = communityRun(t, f, "postiz", typ, 1, map[string]interface{}{"operation": "uploadFile", "binaryProperty": "data"}, creds, "PNGDATA", "x.png")
	if out[0].JSON["path"] != "https://cdn/x.png" || !strings.Contains(f.find(t, "POST", "/public/v1/upload").body, `filename="x.png"`) {
		t.Fatalf("upload: %+v", out)
	}

	communityRun(t, f, "postiz", typ, 1, map[string]interface{}{"operation": "getPosts", "startDate": "2025-01-01", "endDate": "2025-01-31"}, creds, "", "")
	if q := f.find(t, "GET", "/public/v1/posts").query; q != "endDate=2025-01-31&startDate=2025-01-01" {
		t.Fatalf("getPosts query %q", q)
	}
}
