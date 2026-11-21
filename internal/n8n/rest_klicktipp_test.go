package n8n

import (
	"net/url"
	"testing"
)

func TestKlickTippNode(t *testing.T) {
	logins := 0
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.path == "/account/login":
			logins++
			return 200, "application/json", map[string]interface{}{"sessid": "S1", "session_name": "SESS"}
		case r.path == "/subscriber/search":
			return 200, "application/json", []interface{}{"777"}
		case r.path == "/subscriber" && r.method == "POST":
			return 200, "application/json", map[string]interface{}{"id": "777", "email": "a@b.de"}
		case r.path == "/subscriber/777" && r.method == "GET":
			return 200, "application/json", map[string]interface{}{"id": "777", "email": "a@b.de"}
		case r.path == "/tag" && r.method == "GET":
			return 200, "application/json", map[string]interface{}{"10": "VIP", "11": "Lead"}
		case r.path == "/tag" && r.method == "POST":
			return 200, "application/json", []interface{}{"12"}
		}
		return 200, "application/json", []interface{}{true}
	})
	cookie := headerSpy(f, "Cookie")
	creds := map[string]map[string]interface{}{"klickTippApi": {"username": "u-kt-test", "password": "p-kt-test"}}
	const typ = "n8n-nodes-klicktipp.klicktipp"
	ktSessions.Range(func(k, _ interface{}) bool { ktSessions.Delete(k); return true })

	// v3 subscribe with a data field.
	out := communityRun(t, f, "klicktipp", typ, 3, map[string]interface{}{"resource": "subscriber", "operation": "subscribe", "email": "a@b.de", "listId": "5", "tagId": "10",
		"fields": map[string]interface{}{"dataFields": []interface{}{map[string]interface{}{"fieldId": "fieldFirstName", "fieldValue": "Ann"}}}}, creds, "", "")
	if out[0].JSON["id"] != "777" {
		t.Fatalf("subscribe: %+v", out)
	}
	sub := f.find(t, "POST", "/subscriber")
	q, _ := url.ParseQuery(sub.body)
	if sub.ctype != "application/x-www-form-urlencoded" || q.Get("email") != "a@b.de" || q.Get("listid") != "5" || q.Get("tagid") != "10" || q.Get("fields[fieldFirstName]") != "Ann" || cookie("/subscriber") != "SESS=S1" {
		t.Fatalf("subscribe request: %+v", sub)
	}

	// v3 update looks the contact up by email by default.
	out = communityRun(t, f, "klicktipp", typ, 3, map[string]interface{}{"resource": "subscriber", "operation": "update", "lookupEmail": "a@b.de", "email": "new@b.de"}, creds, "", "")
	if out[0].JSON["success"] != true {
		t.Fatalf("update: %+v", out)
	}
	if q, _ := url.ParseQuery(f.find(t, "PUT", "/subscriber/777").body); q.Get("newemail") != "new@b.de" {
		t.Fatalf("update body: %v", q)
	}

	// v2 get uses the ID by default.
	out = communityRun(t, f, "klicktipp", typ, 2, map[string]interface{}{"resource": "subscriber", "operation": "get", "subscriberId": "777"}, creds, "", "")
	if out[0].JSON["email"] != "a@b.de" {
		t.Fatalf("get: %+v", out)
	}

	// Contact tagging with several tags.
	communityRun(t, f, "klicktipp", typ, 3, map[string]interface{}{"resource": "contact-tagging", "email": "a@b.de", "tagId": []interface{}{"10", "11"}}, creds, "", "")
	if q, _ := url.ParseQuery(f.find(t, "POST", "/subscriber/tag").body); q.Get("tagids[0]") != "10" || q.Get("tagids[1]") != "11" {
		t.Fatalf("tag body: %v", q)
	}

	// Default resource/op: tag/index; tag/create.
	out = communityRun(t, f, "klicktipp", typ, 3, map[string]interface{}{}, creds, "", "")
	if len(out) != 2 || out[0].JSON["id"] != "10" || out[0].JSON["value"] != "VIP" {
		t.Fatalf("tags: %+v", out)
	}
	out = communityRun(t, f, "klicktipp", typ, 2, map[string]interface{}{"operation": "create", "name": "New"}, creds, "", "")
	if out[0].JSON["id"] != "12" {
		t.Fatalf("create tag: %+v", out)
	}
	if logins != 1 {
		t.Fatalf("expected one cached login, got %d", logins)
	}
}
