package n8n

import (
	"strings"
	"testing"
)

func TestPipedriveNode(t *testing.T) {
	obj := func(kv ...interface{}) map[string]interface{} {
		m := map[string]interface{}{}
		for k := 0; k+1 < len(kv); k += 2 {
			m[kv[k].(string)] = kv[k+1]
		}
		return m
	}
	ok := func(data interface{}, extra ...interface{}) map[string]interface{} {
		m := obj(extra...)
		m["success"], m["data"] = true, data
		return m
	}
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if !strings.Contains(r.query, "api_token=tok") {
			t.Errorf("%s %s: no api_token in %q", r.method, r.path, r.query)
		}
		switch {
		case r.method == "POST" && r.path == "/v1/deals":
			return 200, "application/json", ok(obj("id", 1, "title", "Big"))
		case r.method == "GET" && r.path == "/v1/persons/search":
			return 200, "application/json", ok(obj("items", []interface{}{obj("result_score", 0.9, "item", obj("id", 7, "name", "Ada"))}))
		case r.method == "GET" && r.path == "/v1/persons":
			if strings.Contains(r.query, "start=2") {
				return 200, "application/json", ok([]interface{}{obj("id", 3)}, "additional_data", obj("pagination", obj("more_items_in_collection", false)))
			}
			return 200, "application/json", ok([]interface{}{obj("id", 1), obj("id", 2)}, "additional_data", obj("pagination", obj("more_items_in_collection", true, "next_start", 2)))
		case r.method == "GET" && r.path == "/v1/organizationFields":
			return 200, "application/json", ok([]interface{}{obj("key", "abc123", "name", "Tier", "field_type", "enum", "options", []interface{}{obj("id", 5, "label", "Gold")})})
		case r.method == "GET" && r.path == "/v1/organizations/9":
			return 200, "application/json", ok(obj("id", 9, "abc123", "5"))
		case r.method == "GET" && r.path == "/api/v2/personFields":
			return 200, "application/json", ok([]interface{}{obj("field_code", "f1", "field_name", "Size", "field_type", "enum", "options", []interface{}{obj("id", 11, "label", "Large")})})
		case r.method == "POST" && r.path == "/api/v2/persons":
			return 200, "application/json", ok(obj("id", 8, "custom_fields", obj("f1", 11)))
		case r.method == "GET" && r.path == "/api/v2/persons":
			if strings.Contains(r.query, "cursor=c2") {
				return 200, "application/json", ok([]interface{}{obj("id", 3)}, "additional_data", obj("next_cursor", nil))
			}
			return 200, "application/json", ok([]interface{}{obj("id", 1), obj("id", 2)}, "additional_data", obj("next_cursor", "c2"))
		case r.method == "DELETE" && r.path == "/api/v2/persons/8":
			return 200, "application/json", ok(obj("id", 8))
		}
		return 404, "application/json", obj("success", false, "error", r.path)
	})
	cred := map[string]map[string]interface{}{"pipedriveApi": {"apiToken": "tok"}}

	// v1, default resource/operation: deal/create.
	out := restRun(t, f, "pipedrive", "pipedrive", map[string]interface{}{"title": "Big", "associateWith": "person", "person_id": 4,
		"additionalFields": map[string]interface{}{"value": 100, "customProperties": map[string]interface{}{"property": []interface{}{map[string]interface{}{"name": "abc", "value": "x"}}}}}, cred)
	if out[0]["title"] != "Big" {
		t.Fatalf("deal create: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/v1/deals"))); b != `{"abc":"x","person_id":4,"title":"Big","value":100}` {
		t.Fatalf("deal body %s", b)
	}

	out = restRun(t, f, "pipedrive", "pipedrive", map[string]interface{}{"resource": "person", "operation": "search", "term": "Ada", "limit": 5,
		"additionalFields": map[string]interface{}{"fields": "name", "exactMatch": true}}, cred)
	if len(out) != 1 || out[0]["name"] != "Ada" || out[0]["result_score"] != 0.9 {
		t.Fatalf("person search: %v", out)
	}
	if q := f.find(t, "GET", "/v1/persons/search").query; !strings.Contains(q, "term=Ada") || !strings.Contains(q, "exact_match=true") || !strings.Contains(q, "limit=5") || !strings.Contains(q, "fields=name") {
		t.Fatalf("search query %q", q)
	}

	out = restRun(t, f, "pipedrive", "pipedrive", map[string]interface{}{"resource": "person", "operation": "getAll", "returnAll": true}, cred)
	if len(out) != 3 {
		t.Fatalf("person getAll: %v", out)
	}
	out = restRun(t, f, "pipedrive", "pipedrive", map[string]interface{}{"resource": "person", "operation": "getAll", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("person getAll limit: %v", out)
	}

	out = restRun(t, f, "pipedrive", "pipedrive", map[string]interface{}{"resource": "organization", "operation": "get", "organizationId": 9, "resolveProperties": true}, cred)
	if out[0]["Tier"] != "Gold" || out[0]["abc123"] != nil {
		t.Fatalf("organization get resolved: %v", out)
	}

	// v2: custom fields are encoded and resolved by default.
	v2 := func(params map[string]interface{}) []map[string]interface{} {
		var res []map[string]interface{}
		for _, it := range restRunItems(t, f, "pipedrive", "pipedrive", 2, params, cred) {
			res = append(res, it.JSON)
		}
		return res
	}
	out = v2(map[string]interface{}{"resource": "person", "name": "Ada", "additionalFields": map[string]interface{}{
		"emails":       map[string]interface{}{"emailProperties": []interface{}{map[string]interface{}{"value": "a@b.c", "primary": true}}},
		"customFields": map[string]interface{}{"property": []interface{}{map[string]interface{}{"name": "Size", "value": "Large"}}}}})
	if mustJSON(out[0]["custom_fields"]) != `{"Size":"Large"}` {
		t.Fatalf("v2 person create: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/api/v2/persons"))); b != `{"custom_fields":{"f1":11},"emails":[{"primary":true,"value":"a@b.c"}],"name":"Ada"}` {
		t.Fatalf("v2 person body %s", b)
	}

	out = v2(map[string]interface{}{"resource": "person", "operation": "getAll", "returnAll": true, "rawCustomFieldOutput": true})
	if len(out) != 3 {
		t.Fatalf("v2 person getAll: %v", out)
	}
	if q := f.find(t, "GET", "/api/v2/persons").query; !strings.Contains(q, "limit=500") {
		t.Fatalf("v2 getAll query %q", q)
	}

	out = v2(map[string]interface{}{"resource": "person", "operation": "delete", "personId": 8})
	if out[0]["success"] != true {
		t.Fatalf("v2 person delete: %v", out)
	}
}
