package n8n

import (
	"strings"
	"testing"
)

func TestSendInBlueNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.method == "POST" && r.path == "/v3/smtp/email":
			return 201, "application/json", map[string]interface{}{"messageId": "<m1>"}
		case r.method == "POST" && r.path == "/v3/contacts":
			return 204, "application/json", ""
		case r.method == "GET" && r.path == "/v3/contacts/a@b.co":
			return 200, "application/json", map[string]interface{}{"id": 7, "email": "a@b.co"}
		case r.method == "GET" && r.path == "/v3/contacts":
			if strings.Contains(r.query, "offset=1000") {
				return 200, "application/json", map[string]interface{}{"contacts": []interface{}{map[string]interface{}{"id": "last"}}}
			}
			page := make([]interface{}, 1000)
			for k := range page {
				page[k] = map[string]interface{}{"id": k}
			}
			return 200, "application/json", map[string]interface{}{"contacts": page}
		case r.method == "GET" && r.path == "/v3/senders":
			s := make([]interface{}, 12)
			for k := range s {
				s[k] = map[string]interface{}{"id": k}
			}
			return 200, "application/json", map[string]interface{}{"senders": s}
		}
		return 404, "application/json", map[string]interface{}{"message": r.path}
	})
	key := headerSpy(f, "api-key")
	creds := map[string]map[string]interface{}{"sendInBlueApi": {"apiKey": "xkeysib-1"}}
	typ := "n8n-nodes-base.sendInBlue"

	// v1 default resource/operation: email/send with the legacy "receipients" spelling.
	out := communityRun(t, f, "sendInBlue", typ, 1, map[string]interface{}{"sender": "Shop <shop@x.io>", "subject": "Hi", "sendHTML": true,
		"htmlContent": "<b>Hi</b>", "textContent": "ignored", "receipients": "a@b.co, Bob <bob@b.co>",
		"additionalFields": map[string]interface{}{"receipientsCC": map[string]interface{}{"receipientCc": map[string]interface{}{"cc": "c@b.co"}},
			"emailTags": map[string]interface{}{"tags": map[string]interface{}{"tag": "x, y"}}}}, creds, "", "")
	if len(out) != 1 || out[0].JSON["messageId"] != "<m1>" || key("/v3/smtp/email") != "xkeysib-1" {
		t.Fatalf("send: %v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/v3/smtp/email"))
	if mustJSON(b["sender"]) != `{"email":"shop@x.io","name":"Shop"}` || mustJSON(b["to"]) != `[{"email":"a@b.co"},{"email":"bob@b.co","name":"Bob"}]` ||
		b["htmlContent"] != "<b>Hi</b>" || b["textContent"] != nil || mustJSON(b["cc"]) != `[{"email":"c@b.co"}]` || mustJSON(b["tags"]) != `["x","y"]` {
		t.Fatalf("send body: %v", b)
	}

	f.reqs = nil
	communityRun(t, f, "sendInBlue", typ, 1.1, map[string]interface{}{"operation": "sendTemplate", "templateId": 12, "recipients": "a@b.co",
		"additionalFields": map[string]interface{}{"templateParameters": map[string]interface{}{"parameterValues": map[string]interface{}{"parameters": "name=Ann,bad,code=X1"}}}}, creds, "", "")
	b = jsonBody(t, f.find(t, "POST", "/v3/smtp/email"))
	if b["templateId"] != float64(12) || mustJSON(b["params"]) != `{"code":"X1","name":"Ann"}` || mustJSON(b["to"]) != `[{"email":"a@b.co"}]` || b["sender"] != nil {
		t.Fatalf("sendTemplate body: %v", b)
	}

	out = communityRun(t, f, "sendInBlue", typ, 1, map[string]interface{}{"resource": "contact", "operation": "upsert", "email": "a@b.co",
		"upsertAttributes": map[string]interface{}{"upsertAttributesValues": []interface{}{map[string]interface{}{"fieldName": "FIRSTNAME", "fieldValue": "Ann"}}}}, creds, "", "")
	b = jsonBody(t, f.find(t, "POST", "/v3/contacts"))
	if out[0].JSON["success"] != true || b["email"] != "a@b.co" || b["updateEnabled"] != true || mustJSON(b["attributes"]) != `{"FIRSTNAME":"Ann"}` {
		t.Fatalf("upsert: %v %v", out, b)
	}

	out = communityRun(t, f, "sendInBlue", typ, 1, map[string]interface{}{"resource": "contact", "operation": "get", "identifier": "a@b.co"}, creds, "", "")
	if out[0].JSON["email"] != "a@b.co" {
		t.Fatalf("get: %v", out)
	}

	f.reqs = nil
	out = communityRun(t, f, "sendInBlue", typ, 1, map[string]interface{}{"resource": "contact", "operation": "getAll", "returnAll": true}, creds, "", "")
	if len(out) != 1001 || out[1000].JSON["id"] != "last" || !strings.Contains(f.reqs[0].query, "limit=1000") {
		t.Fatalf("getAll: %d %+v", len(out), f.reqs[0])
	}
	f.reqs = nil
	out = communityRun(t, f, "sendInBlue", typ, 1, map[string]interface{}{"resource": "contact", "operation": "getAll", "limit": 5}, creds, "", "")
	if len(out) != 5 || len(f.reqs) != 1 || !strings.Contains(f.reqs[0].query, "limit=5") {
		t.Fatalf("getAll limit: %d %+v", len(out), f.reqs)
	}
	out = communityRun(t, f, "sendInBlue", typ, 1, map[string]interface{}{"resource": "sender", "operation": "getAll"}, creds, "", "")
	if len(out) != 10 {
		t.Fatalf("senders default limit: %d", len(out))
	}
}
