package n8n

import (
	"net/url"
	"testing"
)

func TestZoomNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer zm-tok" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/users/me/meetings":
			return 201, "application/json", map[string]interface{}{"id": 85, "topic": "Standup"}
		case r.method == "GET" && r.path == "/users/me/meetings":
			q, _ := url.ParseQuery(r.query)
			if q.Get("page_number") == "1" {
				return 200, "application/json", map[string]interface{}{"page_count": 2, "page_number": 2, "meetings": []interface{}{map[string]interface{}{"id": 3}}}
			}
			return 200, "application/json", map[string]interface{}{"page_count": 2, "page_number": 1, "meetings": []interface{}{map[string]interface{}{"id": 1}, map[string]interface{}{"id": 2}}}
		case r.method == "PATCH" && r.path == "/meetings/85":
			return 204, "application/json", ""
		case r.method == "GET" && r.path == "/meetings/85":
			return 200, "application/json", map[string]interface{}{"id": 85}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"zoomOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "zm-tok"}}}

	out := restRun(t, f, "zoom", "zoom", map[string]interface{}{"authentication": "oAuth2", "topic": "Standup", "additionalFields": map[string]interface{}{
		"startTime": "2024-03-01T09:30:00", "timeZone": "Europe/Berlin", "duration": 15, "settings": map[string]interface{}{"joinBeforeHost": true, "audio": "voip"}}}, cred)
	if out[0]["id"] != float64(85) {
		t.Fatalf("create: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/users/me/meetings"))); b != `{"duration":15,"settings":{"audio":"voip","join_before_host":true},"start_time":"2024-03-01T09:30:00","timezone":"Europe/Berlin","topic":"Standup"}` {
		t.Fatalf("create body %s", b)
	}

	out = restRun(t, f, "zoom", "zoom", map[string]interface{}{"authentication": "oAuth2", "operation": "getAll", "returnAll": true, "filters": map[string]interface{}{"type": "scheduled"}}, cred)
	if len(out) != 3 {
		t.Fatalf("getAll: %v", out)
	}
	out = restRun(t, f, "zoom", "zoom", map[string]interface{}{"authentication": "oAuth2", "operation": "getAll", "limit": 2}, cred)
	if len(out) != 2 {
		t.Fatalf("getAll limit: %v", out)
	}
	q := ""
	for _, r := range f.reqs {
		if r.method == "GET" && r.path == "/users/me/meetings" {
			q = r.query
		}
	}
	if q != "page_size=2" {
		t.Fatalf("getAll limit query %q", q)
	}

	out = restRun(t, f, "zoom", "zoom", map[string]interface{}{"authentication": "oAuth2", "operation": "update", "meetingId": "85", "updateFields": map[string]interface{}{"topic": "New"}}, cred)
	if out[0]["success"] != true || jsonBody(t, f.find(t, "PATCH", "/meetings/85"))["topic"] != "New" {
		t.Fatalf("update: %v", out)
	}
	out = restRun(t, f, "zoom", "zoom", map[string]interface{}{"authentication": "oAuth2", "operation": "get", "meetingId": "85",
		"additionalFields": map[string]interface{}{"occurrenceId": "o1"}}, cred)
	if out[0]["id"] != float64(85) || f.find(t, "GET", "/meetings/85").query != "occurrence_id=o1" {
		t.Fatalf("get: %v", out)
	}
}
