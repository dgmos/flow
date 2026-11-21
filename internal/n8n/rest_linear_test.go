package n8n

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLinearNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.method != "POST" || r.path != "/graphql" {
			return 404, "application/json", map[string]interface{}{"err": r.path}
		}
		if r.auth != "lin_api_1" {
			t.Errorf("auth %q", r.auth)
		}
		var b map[string]interface{}
		_ = json.Unmarshal([]byte(r.body), &b)
		q, v := toString(b["query"]), asMap(b["variables"])
		switch {
		case strings.Contains(q, "issueCreate"):
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"issueCreate": map[string]interface{}{"success": true, "issue": map[string]interface{}{"id": "i1", "title": v["title"]}}}}
		case strings.Contains(q, "issueUpdate"):
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"issueUpdate": map[string]interface{}{"issue": map[string]interface{}{"id": v["issueId"], "state": map[string]interface{}{"id": v["stateId"]}}}}}
		case strings.Contains(q, "issues("):
			if v["after"] == "cur1" {
				return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"issues": map[string]interface{}{"nodes": []interface{}{map[string]interface{}{"id": "c"}}, "pageInfo": map[string]interface{}{"hasNextPage": false}}}}
			}
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"issues": map[string]interface{}{"nodes": []interface{}{map[string]interface{}{"id": "a"}, map[string]interface{}{"id": "b"}},
				"pageInfo": map[string]interface{}{"hasNextPage": true, "endCursor": "cur1"}}}}
		case strings.Contains(q, "commentCreate"):
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"commentCreate": map[string]interface{}{"success": true, "comment": map[string]interface{}{"id": "cm1"}}}}
		}
		return 400, "application/json", map[string]interface{}{"err": q}
	})
	cred := map[string]map[string]interface{}{"linearApi": {"apiKey": "lin_api_1"}}

	out := restRun(t, f, "linear", "linear", map[string]interface{}{"teamId": "T1", "title": "Bug", "additionalFields": map[string]interface{}{"priorityId": 2, "description": "d"}}, cred)
	if len(out) != 1 || out[0]["id"] != "i1" || out[0]["title"] != "Bug" {
		t.Fatalf("create: %v", out)
	}
	if v := asMap(jsonBody(t, f.find(t, "POST", "/graphql"))["variables"]); v["teamId"] != "T1" || v["priorityId"] != float64(2) || v["description"] != "d" {
		t.Fatalf("create vars: %v", v)
	}

	out = restRun(t, f, "linear", "linear", map[string]interface{}{"operation": "update", "issueId": "i1", "updateFields": map[string]interface{}{"stateId": "s2"}}, cred)
	if asMap(out[0]["state"])["id"] != "s2" {
		t.Fatalf("update: %v", out)
	}

	out = restRun(t, f, "linear", "linear", map[string]interface{}{"operation": "getAll", "returnAll": true}, cred)
	if len(out) != 3 || out[2]["id"] != "c" {
		t.Fatalf("getAll: %v", out)
	}
	out = restRun(t, f, "linear", "linear", map[string]interface{}{"operation": "getAll", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("getAll limit: %v", out)
	}

	out = restRun(t, f, "linear", "linear", map[string]interface{}{"resource": "comment", "issueId": "i1", "comment": "hi", "additionalFields": map[string]interface{}{"parentId": " "}}, cred)
	if out[0]["success"] != true || asMap(out[0]["comment"])["id"] != "cm1" {
		t.Fatalf("comment: %v", out)
	}
}
