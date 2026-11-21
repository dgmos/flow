package n8n

import (
	"fmt"
	"strings"
	"testing"
)

func TestTrelloNode(t *testing.T) {
	cards := func(from, n int) []interface{} {
		var l []interface{}
		for k := 0; k < n; k++ {
			l = append(l, map[string]interface{}{"id": fmt.Sprintf("c%d", from+k)})
		}
		return l
	}
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if !strings.Contains(r.query, "key=K") || !strings.Contains(r.query, "token=T") {
			t.Errorf("auth query %q", r.query)
		}
		switch {
		case r.method == "POST" && r.path == "/cards":
			return 200, "application/json", map[string]interface{}{"id": "card1", "name": "Bug"}
		case r.method == "GET" && r.path == "/lists/L1/cards":
			if strings.Contains(r.query, "before=") {
				return 200, "application/json", cards(30, 2)
			}
			if strings.Contains(r.query, "limit=30") {
				return 200, "application/json", cards(0, 30)
			}
			return 200, "application/json", cards(0, 5)
		case r.method == "PUT" && r.path == "/cards/card1":
			return 200, "application/json", map[string]interface{}{"id": "card1", "name": "Done"}
		case r.method == "POST" && r.path == "/checklists/CL/checkItems":
			return 200, "application/json", map[string]interface{}{"id": "ci1"}
		case r.method == "DELETE":
			return 200, "application/json", map[string]interface{}{"_value": nil}
		}
		return 404, "application/json", map[string]interface{}{"err": r.path}
	})
	cred := map[string]map[string]interface{}{"trelloApi": {"apiKey": "K", "apiToken": "T"}}

	out := restRun(t, f, "trello", "trello", map[string]interface{}{"listId": "L1", "name": "Bug", "description": "d",
		"additionalFields": map[string]interface{}{"due": "2024-01-02", "idLabels": "x,y"}}, cred)
	if len(out) != 1 || out[0]["id"] != "card1" {
		t.Fatalf("create: %v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/cards"))
	if b["idList"] != "L1" || b["name"] != "Bug" || b["desc"] != "d" || b["due"] != "2024-01-02" || b["idLabels"] != "x,y" {
		t.Fatalf("create body: %v", b)
	}

	out = restRun(t, f, "trello", "trello", map[string]interface{}{"resource": "list", "operation": "getCards", "id": "L1", "returnAll": true}, cred)
	if len(out) != 32 || out[31]["id"] != "c31" {
		t.Fatalf("getCards all: %d %v", len(out), out[len(out)-1])
	}
	if q := f.find(t, "GET", "/lists/L1/cards").query; !strings.Contains(q, "sort=-id") {
		t.Fatalf("getCards query %q", q)
	}
	out = restRun(t, f, "trello", "trello", map[string]interface{}{"resource": "list", "operation": "getCards", "id": "L1", "limit": 3}, cred)
	if len(out) != 3 {
		t.Fatalf("getCards limit: %v", out)
	}

	restRun(t, f, "trello", "trello", map[string]interface{}{"operation": "update", "id": "card1", "updateFields": map[string]interface{}{"closed": true, "name": "Done"}}, cred)
	if q := f.find(t, "PUT", "/cards/card1").query; !strings.Contains(q, "closed=true") || !strings.Contains(q, "name=Done") {
		t.Fatalf("update query %q", q)
	}

	restRun(t, f, "trello", "trello", map[string]interface{}{"resource": "checklist", "operation": "createCheckItem", "checklistId": "CL", "name": "step", "additionalFields": map[string]interface{}{"checked": true}}, cred)
	if b := jsonBody(t, f.find(t, "POST", "/checklists/CL/checkItems")); b["name"] != "step" || b["checked"] != true {
		t.Fatalf("checkItem body: %v", b)
	}

	out = restRun(t, f, "trello", "trello", map[string]interface{}{"operation": "delete", "id": "card1"}, cred)
	if len(out) != 1 || f.find(t, "DELETE", "/cards/card1").path != "/cards/card1" {
		t.Fatalf("delete: %v", out)
	}
}
