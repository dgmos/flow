package n8n

import (
	"net/url"
	"strings"
	"testing"
)

func TestMailchimpNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "apikey abc-us6" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/3.0/lists/L1/members":
			return 200, "application/json", map[string]interface{}{"id": "h1", "email_address": "a@b.c"}
		case r.method == "GET" && r.path == "/3.0/lists/L1/members":
			q, _ := url.ParseQuery(r.query)
			switch q.Get("offset") {
			case "", "0":
				return 200, "application/json", map[string]interface{}{"members": []interface{}{map[string]interface{}{"id": "m1"}, map[string]interface{}{"id": "m2"}}}
			case "2":
				return 200, "application/json", map[string]interface{}{"members": []interface{}{map[string]interface{}{"id": "m3"}}}
			}
			return 200, "application/json", map[string]interface{}{"members": []interface{}{}}
		case r.method == "POST" && r.path == "/3.0/lists/L1/members/a@b.c/tags":
			return 204, "application/json", ""
		case r.method == "PUT" && r.path == "/3.0/lists/L1/members/a@b.c":
			return 200, "application/json", map[string]interface{}{"id": "h1", "status": "unsubscribed"}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"mailchimpApi": {"apiKey": "abc-us6"}}

	out := restRun(t, f, "mailchimp", "mailchimp", map[string]interface{}{"authentication": "apiKey", "list": "L1", "email": "a@b.c", "status": "subscribed",
		"options":       map[string]interface{}{"tags": "vip,new"},
		"mergeFieldsUi": map[string]interface{}{"mergeFieldsValues": []interface{}{map[string]interface{}{"name": "FNAME", "value": "Ada"}}}}, cred)
	if out[0]["id"] != "h1" {
		t.Fatalf("member create: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/3.0/lists/L1/members"))); b != `{"email_address":"a@b.c","listId":"L1","merge_fields":{"FNAME":"Ada"},"status":"subscribed","tags":["vip","new"]}` {
		t.Fatalf("member create body %s", b)
	}

	out = restRun(t, f, "mailchimp", "mailchimp", map[string]interface{}{"authentication": "apiKey", "operation": "getAll", "list": "L1", "returnAll": true,
		"options": map[string]interface{}{"status": "subscribed"}}, cred)
	if len(out) != 3 {
		t.Fatalf("getAll: %v", out)
	}
	if q := f.find(t, "GET", "/members").query; !strings.Contains(q, "count=500") || !strings.Contains(q, "status=subscribed") {
		t.Fatalf("getAll query %q", q)
	}
	out = restRun(t, f, "mailchimp", "mailchimp", map[string]interface{}{"authentication": "apiKey", "operation": "getAll", "list": "L1", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("getAll limit: %v", out)
	}

	out = restRun(t, f, "mailchimp", "mailchimp", map[string]interface{}{"authentication": "apiKey", "operation": "update", "list": "L1", "email": "a@b.c",
		"updateFields": map[string]interface{}{"status": "unsubscribed"}}, cred)
	if out[0]["status"] != "unsubscribed" || mustJSON(jsonBody(t, f.find(t, "PUT", "/members/a@b.c"))) != `{"email_address":"a@b.c","listId":"L1","status":"unsubscribed"}` {
		t.Fatalf("update: %v", out)
	}

	out = restRun(t, f, "mailchimp", "mailchimp", map[string]interface{}{"authentication": "apiKey", "resource": "memberTag", "operation": "delete", "list": "L1",
		"email": "a@b.c", "tags": []interface{}{"vip"}}, cred)
	if out[0]["success"] != true || mustJSON(jsonBody(t, f.find(t, "POST", "/tags"))) != `{"tags":[{"name":"vip","status":"inactive"}]}` {
		t.Fatalf("memberTag delete: %v", out)
	}
}
