package n8n

import (
	"net/url"
	"strings"
	"testing"
)

func TestMattermostNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer mm" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/posts":
			return 201, "application/json", map[string]interface{}{"id": "p1", "message": "hi"}
		case r.method == "POST" && r.path == "/channels/C1/members":
			return 201, "application/json", map[string]interface{}{"channel_id": "C1", "user_id": "U1"}
		case r.method == "GET" && r.path == "/users":
			q, _ := url.ParseQuery(r.query)
			switch {
			case q.Get("page") == "0":
				return 200, "application/json", []interface{}{map[string]interface{}{"id": "a"}, map[string]interface{}{"id": "b"}}
			case q.Get("page") == "1":
				return 200, "application/json", []interface{}{map[string]interface{}{"id": "c"}}
			case q.Has("page"):
				return 200, "application/json", []interface{}{}
			}
			return 200, "application/json", []interface{}{map[string]interface{}{"id": "a"}}
		case r.method == "POST" && r.path == "/teams/T1/invite/email":
			return 200, "application/json", map[string]interface{}{"status": "OK"}
		}
		return 404, "application/json", map[string]interface{}{"err": r.path}
	})
	cred := map[string]map[string]interface{}{"mattermostApi": {"accessToken": "mm", "baseUrl": "https://chat.example.com/"}}

	out := restRun(t, f, "mattermost", "mattermost", map[string]interface{}{"channelId": "C1", "message": "hi", "otherOptions": map[string]interface{}{"root_id": "r1"},
		"attachments": []interface{}{map[string]interface{}{"title": "T", "color": "#ff0000",
			"fields": map[string]interface{}{"item": []interface{}{map[string]interface{}{"title": "k", "value": "v", "short": true}}},
			"actions": map[string]interface{}{"item": []interface{}{map[string]interface{}{"type": "button", "data_source": "custom", "name": "Go",
				"integration": map[string]interface{}{"item": map[string]interface{}{"url": "https://x", "context": map[string]interface{}{"property": []interface{}{map[string]interface{}{"name": "a", "value": "1"}}}}}}}}}}}, cred)
	if len(out) != 1 || out[0]["id"] != "p1" {
		t.Fatalf("post: %v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/posts"))
	want := `{"attachments":[{"actions":[{"integration":{"context":{"a":"1"},"url":"https://x"},"name":"Go"}],"color":"#ff0000","fields":[{"short":true,"title":"k","value":"v"}],"title":"T"}]}`
	if b["channel_id"] != "C1" || b["message"] != "hi" || b["root_id"] != "r1" || mustJSON(b["props"]) != want {
		t.Fatalf("post body: %v", mustJSON(b))
	}

	restRun(t, f, "mattermost", "mattermost", map[string]interface{}{"resource": "channel", "operation": "addUser", "channelId": "C1", "userId": "U1"}, cred)
	if b := jsonBody(t, f.find(t, "POST", "/channels/C1/members")); b["user_id"] != "U1" {
		t.Fatalf("addUser body: %v", b)
	}

	// returnAll defaults to true for users.
	out = restRun(t, f, "mattermost", "mattermost", map[string]interface{}{"resource": "user", "operation": "getAll", "additionalFields": map[string]interface{}{"inTeam": "T1"}}, cred)
	if len(out) != 3 || out[2]["id"] != "c" {
		t.Fatalf("users all: %v", out)
	}
	if q := f.find(t, "GET", "/users").query; !strings.Contains(q, "in_team=T1") || !strings.Contains(q, "per_page=100") {
		t.Fatalf("users query %q", q)
	}
	out = restRun(t, f, "mattermost", "mattermost", map[string]interface{}{"resource": "user", "operation": "getAll", "returnAll": false, "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("users limit: %v", out)
	}

	restRun(t, f, "mattermost", "mattermost", map[string]interface{}{"resource": "user", "operation": "invite", "teamId": "T1", "emails": "a@x.io,b@x.io"}, cred)
	if r := f.find(t, "POST", "/teams/T1/invite/email"); r.body != `["a@x.io","b@x.io"]` || !strings.HasPrefix(r.ctype, "application/json") {
		t.Fatalf("invite: %+v", r)
	}
}
