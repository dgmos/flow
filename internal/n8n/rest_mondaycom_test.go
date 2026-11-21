package n8n

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMondayComNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.method != "POST" || r.auth != "Bearer mt" {
			t.Errorf("request %s auth %q", r.method, r.auth)
		}
		var b map[string]interface{}
		_ = json.Unmarshal([]byte(r.body), &b)
		q, v := toString(b["query"]), asMap(b["variables"])
		data := func(d map[string]interface{}) (int, string, interface{}) {
			return 200, "application/json", map[string]interface{}{"data": d}
		}
		switch {
		case strings.Contains(q, "create_item"):
			return data(map[string]interface{}{"create_item": map[string]interface{}{"id": "it1"}})
		case strings.Contains(q, "change_column_value"):
			return data(map[string]interface{}{"change_column_value": map[string]interface{}{"id": v["itemId"]}})
		case strings.Contains(q, "next_items_page"):
			return data(map[string]interface{}{"next_items_page": map[string]interface{}{"cursor": nil, "items": []interface{}{map[string]interface{}{"id": "c"}}}})
		case strings.Contains(q, "items_page"):
			items := []interface{}{map[string]interface{}{"id": "a"}, map[string]interface{}{"id": "b"}}
			return data(map[string]interface{}{"boards": []interface{}{map[string]interface{}{"groups": []interface{}{map[string]interface{}{"id": "g1", "items_page": map[string]interface{}{"cursor": "CUR", "items": items}}}}}})
		case strings.Contains(q, "items (ids"):
			return data(map[string]interface{}{"items": []interface{}{map[string]interface{}{"id": "1"}, map[string]interface{}{"id": "2"}}})
		}
		return 400, "application/json", map[string]interface{}{"err": q}
	})
	cred := map[string]map[string]interface{}{"mondayComApi": {"apiToken": "mt"}}
	last := func() map[string]interface{} {
		f.mu.Lock()
		defer f.mu.Unlock()
		var b map[string]interface{}
		_ = json.Unmarshal([]byte(f.reqs[len(f.reqs)-1].body), &b)
		return asMap(b["variables"])
	}

	out := restRun(t, f, "mondayCom", "mondayCom", map[string]interface{}{"resource": "boardItem", "boardId": "123", "groupId": "topics", "name": "Lead",
		"additionalFields": map[string]interface{}{"columnValues": `{"status": {"label": "Done"}}`}}, cred)
	if len(out) != 1 || out[0]["id"] != "it1" {
		t.Fatalf("create: %v", out)
	}
	if v := last(); v["itemName"] != "Lead" || v["boardId"] != "123" || v["columnValues"] != `{"status":{"label":"Done"}}` {
		t.Fatalf("create vars: %v", v)
	}

	restRun(t, f, "mondayCom", "mondayCom", map[string]interface{}{"resource": "boardItem", "operation": "changeColumnValue", "boardId": 1, "itemId": 2, "columnId": "status", "value": `{"label":"Stuck"}`}, cred)
	if v := last(); v["value"] != `{"label":"Stuck"}` || v["columnId"] != "status" || v["itemId"] != float64(2) {
		t.Fatalf("changeColumnValue vars: %v", v)
	}

	out = restRun(t, f, "mondayCom", "mondayCom", map[string]interface{}{"resource": "boardItem", "operation": "getAll", "boardId": "123", "groupId": "g1", "returnAll": true}, cred)
	if len(out) != 3 || out[2]["id"] != "c" {
		t.Fatalf("getAll: %v", out)
	}
	if v := last(); v["cursor"] != "CUR" {
		t.Fatalf("next page vars: %v", v)
	}
	restRun(t, f, "mondayCom", "mondayCom", map[string]interface{}{"resource": "boardItem", "operation": "getAll", "boardId": "123", "groupId": "g1", "limit": 7}, cred)
	if v := last(); v["limit"] != float64(7) {
		t.Fatalf("getAll limit vars: %v", v)
	}

	out = restRun(t, f, "mondayCom", "mondayCom", map[string]interface{}{"resource": "boardItem", "operation": "get", "itemId": "1,2"}, cred)
	if len(out) != 2 || mustJSON(last()["itemId"]) != `["1","2"]` {
		t.Fatalf("get: %v %v", out, last())
	}
}
