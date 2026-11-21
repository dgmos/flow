package n8n

import (
	"net/http"
	"strings"
	"testing"
)

func TestServiceNowNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.method == "POST" && r.path == "/now/table/incident":
			return 201, "application/json", map[string]interface{}{"result": map[string]interface{}{"sys_id": "i1", "number": "INC1"}}
		case r.method == "GET" && r.path == "/now/table/incident":
			recs := []interface{}{map[string]interface{}{"sys_id": "a"}, map[string]interface{}{"sys_id": "b"}}
			if strings.Contains(r.query, "sysparm_offset=2") {
				recs = []interface{}{map[string]interface{}{"sys_id": "c"}}
			}
			return 200, "application/json", map[string]interface{}{"result": recs}
		case r.method == "PATCH" && r.path == "/now/table/incident/i1":
			return 200, "application/json", map[string]interface{}{"result": map[string]interface{}{"sys_id": "i1", "state": "2"}}
		case r.method == "POST" && r.path == "/now/table/u_tickets":
			return 201, "application/json", map[string]interface{}{"result": map[string]interface{}{"sys_id": "t1"}}
		case r.method == "GET" && r.path == "/now/table/sys_user":
			return 200, "application/json", map[string]interface{}{"result": []interface{}{map[string]interface{}{"user_name": "jdoe"}}}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	inner := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Total-Count", "3")
		inner.ServeHTTP(w, r)
	})
	basic := map[string]map[string]interface{}{"serviceNowBasicApi": {"user": "admin", "password": "pw", "subdomain": "dev1"}}
	wantAuth := basicAuth("admin", "pw")["Authorization"]

	out := communityRun(t, f, "serviceNow", "n8n-nodes-base.serviceNow", 1, map[string]interface{}{"authentication": "basicAuth", "resource": "incident", "operation": "create",
		"short_description": "Printer down", "additionalFields": map[string]interface{}{"urgency": 1, "caller_id": "u9"}}, basic, "", "")
	if len(out) != 1 || out[0].JSON["number"] != "INC1" {
		t.Fatalf("create: %v", out)
	}
	r := f.find(t, "POST", "/now/table/incident")
	if b := jsonBody(t, r); b["short_description"] != "Printer down" || b["urgency"] != float64(1) || b["caller_id"] != "u9" || r.auth != wantAuth {
		t.Fatalf("create request: %v %q", b, r.auth)
	}

	out = communityRun(t, f, "serviceNow", "n8n-nodes-base.serviceNow", 1, map[string]interface{}{"authentication": "basicAuth", "resource": "incident", "operation": "getAll",
		"returnAll": true, "options": map[string]interface{}{"sysparm_fields": []interface{}{"sys_id", "number"}, "sysparm_display_value": "true"}}, basic, "", "")
	if len(out) != 3 || out[2].JSON["sys_id"] != "c" {
		t.Fatalf("getAll: %v", out)
	}
	if q := f.find(t, "GET", "/now/table/incident").query; !strings.Contains(q, "sysparm_limit=100") || !strings.Contains(q, "sysparm_fields=sys_id%2Cnumber") || !strings.Contains(q, "sysparm_display_value=true") {
		t.Fatalf("getAll query %q", q)
	}
	f.reqs = nil
	out = communityRun(t, f, "serviceNow", "n8n-nodes-base.serviceNow", 1, map[string]interface{}{"authentication": "basicAuth", "resource": "incident", "operation": "getAll", "limit": 2}, basic, "", "")
	if len(out) != 2 || len(f.reqs) != 1 || !strings.Contains(f.reqs[0].query, "sysparm_limit=2") {
		t.Fatalf("getAll limit: %v %+v", out, f.reqs)
	}

	out = communityRun(t, f, "serviceNow", "n8n-nodes-base.serviceNow", 1, map[string]interface{}{"authentication": "basicAuth", "resource": "incident", "operation": "update",
		"id": "i1", "updateFields": map[string]interface{}{"state": "2", "work_notes": "on it"}}, basic, "", "")
	if out[0].JSON["state"] != "2" || jsonBody(t, f.find(t, "PATCH", "/now/table/incident/i1"))["work_notes"] != "on it" {
		t.Fatalf("update: %v", out)
	}

	oauth := map[string]map[string]interface{}{"serviceNowOAuth2Api": {"subdomain": "dev1", "oauthTokenData": map[string]interface{}{"access_token": "sn-tok"}}}
	out = communityRun(t, f, "serviceNow", "n8n-nodes-base.serviceNow", 1, map[string]interface{}{"resource": "tableRecord", "operation": "create", "tableName": "u_tickets",
		"fieldsToSend": map[string]interface{}{"field": []interface{}{map[string]interface{}{"column": "u_title", "value": "Hi"}}}}, oauth, "", "")
	r = f.find(t, "POST", "/now/table/u_tickets")
	if out[0].JSON["sys_id"] != "t1" || jsonBody(t, r)["u_title"] != "Hi" || r.auth != "Bearer sn-tok" {
		t.Fatalf("tableRecord create: %v %s %q", out, r.body, r.auth)
	}

	out = communityRun(t, f, "serviceNow", "n8n-nodes-base.serviceNow", 1, map[string]interface{}{"getOption": "user_name", "user_name": "jdoe"}, oauth, "", "")
	if out[0].JSON["user_name"] != "jdoe" || !strings.Contains(f.find(t, "GET", "/now/table/sys_user").query, "sysparm_query=user_name%3Djdoe") {
		t.Fatalf("user get: %v", out)
	}
	if b := importBlocked(t, "n8n-nodes-base.serviceNow", 1, map[string]interface{}{"resource": "incident", "operation": "nope"}); len(b) == 0 {
		t.Fatal("unknown operation not blocked")
	}
}
