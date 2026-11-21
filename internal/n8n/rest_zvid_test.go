package n8n

import (
	"strings"
	"testing"
)

func TestZvidNode(t *testing.T) {
	const typ = "@zvid/n8n-nodes-zvid.zvid"
	polls := 0
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.header.Get("X-Api-Key") != "zk" {
			t.Errorf("api key %q", r.header.Get("X-Api-Key"))
		}
		switch r.method + " " + r.path {
		case "POST /api/render/api-key", "POST /api/render/image/api-key":
			return 200, "", map[string]interface{}{"jobId": "J1", "state": "queued"}
		case "GET /api/jobs/J1":
			if polls++; polls < 2 {
				return 200, "", map[string]interface{}{"id": "J1", "state": "active"}
			}
			return 200, "", map[string]interface{}{"id": "J1", "state": "completed", "url": "https://cdn/v.mp4"}
		case "GET /api/jobs":
			return 200, "", map[string]interface{}{"jobs": []interface{}{map[string]interface{}{"id": "J1"}, map[string]interface{}{"id": "J2"}}}
		case "POST /api/render/validate/api-key":
			if strings.Contains(r.body, "bad") {
				return 422, "", map[string]interface{}{"error": "Invalid", "details": []interface{}{"width missing"}}
			}
			return 200, "", map[string]interface{}{"warnings": []interface{}{}}
		case "GET /api/stock/search":
			return 200, "", map[string]interface{}{"results": []interface{}{}}
		case "GET /api/templates":
			return 200, "", map[string]interface{}{"templates": []interface{}{map[string]interface{}{"id": "T1"}}}
		}
		return 404, "", map[string]interface{}{}
	})
	bases := map[string]string{"zvid": ""}
	cred := map[string]map[string]interface{}{"zvidApi": {"apiKey": "zk", "baseUrl": "https://api.zvid.io/"}}

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"source": "json", "projectJson": `{"width":1280}`, "waitForCompletion": true,
		"waitOptions": map[string]interface{}{"pollInterval": 0}, "additionalFields": map[string]interface{}{"webhookUrl": "https://hook"}}, cred, nil)
	if b := f.find(t, "POST", "/api/render/api-key").json(t); mustJSON(b["payload"]) != `{"width":1280}` || b["webhookUrl"] != "https://hook" || b["template"] != nil {
		t.Fatalf("render body %v", b)
	}
	if out[0].JSON["state"] != "completed" || polls != 2 {
		t.Fatalf("render should wait for completion: %v", out[0].JSON)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"renderType": "image", "templateId": "T1", "variables": `{"name":"Ada"}`}, cred, nil)
	if b := f.find(t, "POST", "/api/render/image/api-key").json(t); b["template"] != "T1" || mustJSON(b["variables"]) != `{"name":"Ada"}` {
		t.Fatalf("template render body %v", b)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "getAll", "filterType": "video"}, cred, nil)
	if q := f.find(t, "GET", "/api/jobs").query; q.Get("limit") != "50" || q.Get("page") != "1" || q.Get("type") != "video" || len(out) != 2 {
		t.Fatalf("getAll %v %v", q, out)
	}
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "validate", "source": "json", "projectJson": `{"bad":true}`}, cred, nil)
	if j := out[0].JSON; j["valid"] != false || j["error"] != "Invalid" || mustJSON(j["errors"]) != `["width missing"]` {
		t.Fatalf("validate rejected %v", j)
	}
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "validate", "source": "json", "projectJson": `{}`}, cred, nil)
	if out[0].JSON["valid"] != true {
		t.Fatalf("validate ok %v", out[0].JSON)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "stockMedia", "stockQuery": "sea", "stockType": "video"}, cred, nil)
	if q := f.find(t, "GET", "/api/stock/search").query; q.Get("query") != "sea" || q.Get("type") != "video" || q.Get("provider") != "all" || q.Get("perPage") != "12" {
		t.Fatalf("stock query %v", q)
	}
	if out = cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "template", "operation": "getAll"}, cred, nil); len(out) != 1 || out[0].JSON["id"] != "T1" {
		t.Fatalf("templates %v", out)
	}
	if cmtyRunnable(t, typ, map[string]interface{}{"resource": "project", "operation": "create"}) {
		t.Fatal("project/create is not a published operation")
	}
}
