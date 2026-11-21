package n8n

import (
	"strings"
	"testing"
)

func TestGotoHumanNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.path == "/requestReview" {
			return 200, "application/json", map[string]interface{}{"reviewId": "r1", "gthLink": "https://app.gotohuman.com/r1"}
		}
		return 200, "application/json", ""
	})
	key := headerSpy(f, "x-api-key")
	creds := map[string]map[string]interface{}{"gotoHumanApi": {"apiKey": "gth-key"}}
	const typ = "@gotohuman/n8n-nodes-gotohuman.gotoHuman"
	tmpl := map[string]interface{}{"__rl": true, "mode": "list", "value": "form1"}

	// v2: raw review data.
	out := communityRun(t, f, "gotoHuman", typ, 2, map[string]interface{}{"operation": "send", "reviewTemplateID": tmpl,
		"agentID": map[string]interface{}{"__rl": true, "mode": "id", "value": "ag1"}, "reviewData": `{"draft":"Hello"}`,
		"metaSelect": "keyValue", "metaKeyValues": map[string]interface{}{"metaArray": []interface{}{map[string]interface{}{"key": "row", "value": "7"}}},
		"additionalFields": map[string]interface{}{"title": "Check"}}, creds, "", "")
	if out[0].JSON["statusCode"] != 200.0 || asMap(out[0].JSON["body"])["reviewId"] != "r1" {
		t.Fatalf("send: %+v", out)
	}
	r := f.find(t, "POST", "/requestReview")
	b := jsonBody(t, r)
	if key("/requestReview") != "gth-key" || b["formId"] != "form1" || b["agentId"] != "ag1" || asMap(b["fields"])["draft"] != "Hello" ||
		asMap(b["meta"])["row"] != "7" || b["title"] != "Check" || b["originV"] != 2.0 || b["webhookUrl"] != nil {
		t.Fatalf("send body: %v", b)
	}

	// v1: resource mapper fields, JSON strings parsed for array fields.
	communityRun(t, f, "gotoHuman", typ, 1, map[string]interface{}{"operation": "send", "reviewTemplateID": tmpl,
		"fields": map[string]interface{}{"mappingMode": "defineBelow", "value": map[string]interface{}{"links": `["a"]`, "text": "x"},
			"schema": []interface{}{map[string]interface{}{"id": "links", "type": "array"}, map[string]interface{}{"id": "text", "type": "string"}}}}, creds, "", "")
	var v1 map[string]interface{}
	for _, rr := range f.reqs {
		if rr.path == "/requestReview" {
			v1 = jsonBody(t, rr)
		}
	}
	if fl := asMap(v1["fields"]); len(asList(fl["links"])) != 1 || fl["text"] != "x" {
		t.Fatalf("v1 body: %v", v1)
	}

	out = communityRun(t, f, "gotoHuman", typ, 2, map[string]interface{}{"operation": "delete", "reviewId": "r1"}, creds, "", "")
	if out[0].JSON["success"] != true || !strings.Contains(f.find(t, "DELETE", "/deleteReview").body, `"reviewId":"r1"`) {
		t.Fatalf("delete: %+v", out)
	}

	// The default operation waits for the review: blocked at import.
	if b := importBlocked(t, typ, 1, map[string]interface{}{"reviewTemplateID": tmpl}); len(b) == 0 || !strings.Contains(b[0], "sendAndWait") {
		t.Fatalf("sendAndWait not blocked: %v", b)
	}
}
