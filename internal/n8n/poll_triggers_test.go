package n8n

import (
	"strings"
	"testing"
	"time"

	"github.com/dgmos/flow/internal/node"
)

func TestServicePollTriggers(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case strings.HasPrefix(r.path, "/v1/databases/"):
			return 200, "application/json", map[string]interface{}{"results": []interface{}{map[string]interface{}{"id": "p1", "url": "https://notion.so/p1", "created_time": now, "properties": map[string]interface{}{}}}}
		case strings.HasSuffix(r.path, "/events"):
			return 200, "application/json", map[string]interface{}{"items": []interface{}{
				map[string]interface{}{"id": "e1", "created": "2024-01-01T00:00:00Z", "updated": now},
				map[string]interface{}{"id": "e2", "created": now, "updated": now}}}
		case strings.HasPrefix(r.path, "/appABCDEFGHIJKLMN/"):
			return 200, "application/json", map[string]interface{}{"records": []interface{}{map[string]interface{}{"id": "rec1", "fields": map[string]interface{}{"Name": "a"}}}}
		}
		return 404, "application/json", map[string]interface{}{}
	})
	creds := NewStaticCredentials()
	creds.Set("notionApi", "*", map[string]interface{}{"apiKey": "nk"})
	creds.Set("googleCalendarOAuth2Api", "*", map[string]interface{}{"oauthTokenData": map[string]interface{}{"access_token": "gt"}})
	creds.Set("airtableTokenApi", "*", map[string]interface{}{"apiKey": "at"})
	h := newHarness(t, &Options{Credentials: creds, PollState: &memPollState{m: map[string]string{}}, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}},
		APIBaseURLs: map[string]string{"notion": f.srv.URL, "calendar": f.srv.URL, "airtable": f.srv.URL}})
	every := map[string]interface{}{"item": []interface{}{map[string]interface{}{"mode": "everyMinute"}}}
	cases := []struct {
		name string
		node map[string]interface{}
		want string
	}{
		{"notion", credNode(map[string]interface{}{"name": "T", "type": "n8n-nodes-base.notionTrigger", "typeVersion": 1, "parameters": map[string]interface{}{
			"pollTimes": every, "event": "pageAddedToDatabase", "databaseId": map[string]interface{}{"__rl": true, "mode": "id", "value": "0123456789abcdef0123456789abcdef"}}}, "notionApi"), "p1"},
		{"calendar", credNode(map[string]interface{}{"name": "T", "type": "n8n-nodes-base.googleCalendarTrigger", "typeVersion": 1, "parameters": map[string]interface{}{
			"pollTimes": every, "triggerOn": "eventUpdated", "calendarId": map[string]interface{}{"__rl": true, "mode": "list", "value": "primary"}}}, "googleCalendarOAuth2Api"), "e1"},
		{"airtable", credNode(map[string]interface{}{"name": "T", "type": "n8n-nodes-base.airtableTrigger", "typeVersion": 1, "parameters": map[string]interface{}{
			"pollTimes": every, "authentication": "airtableTokenApi", "triggerField": "Modified",
			"baseId":  map[string]interface{}{"__rl": true, "mode": "url", "value": "https://airtable.com/appABCDEFGHIJKLMN/tblABCDEFGHIJKLMN/viw1"},
			"tableId": map[string]interface{}{"__rl": true, "mode": "url", "value": "https://airtable.com/appABCDEFGHIJKLMN/tblABCDEFGHIJKLMN/viw1"}}}, "airtableTokenApi"), "rec1"},
	}
	for _, c := range cases {
		data := wfJSON(t, []map[string]interface{}{c.node, nd("Out", "noOp", 1, nil)}, "T->Out")
		_, rep, _ := Import(data, ImportOptions{})
		if !rep.Runnable || len(rep.Triggers) == 0 || rep.Triggers[0].Kind != "poll" {
			t.Fatalf("%s: %+v %+v", c.name, rep.Blocking, rep.Triggers)
		}
		h.fixedID = "poll-" + c.name
		if _, steps := h.run(t, data, `{"__trigger":"T"}`); steps["Out"].Status != "SKIPPED" {
			t.Fatalf("%s: first poll should only record the time: %+v", c.name, steps["T"])
		}
		_, steps := h.run(t, data, `{"__trigger":"T"}`)
		if got := itemsOf(t, steps["Out"], 0); len(got) != 1 || got[0]["id"] != c.want {
			t.Fatalf("%s second poll: %v (%s)", c.name, got, steps["T"].Error)
		}
	}
	if q := f.find(t, "GET", "/appABCDEFGHIJKLMN/tblABCDEFGHIJKLMN").query; !strings.Contains(q, "IS_AFTER%28%7BModified%7D") {
		t.Fatalf("airtable formula: %s", q)
	}
	if b := f.find(t, "POST", "/v1/databases/").body; !strings.Contains(b, `"timestamp":"created_time"`) {
		t.Fatalf("notion query: %s", b)
	}
}
