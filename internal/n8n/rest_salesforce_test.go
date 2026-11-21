package n8n

import (
	"net/url"
	"strings"
	"testing"
)

func TestSalesforceNode(t *testing.T) {
	var queries []string
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer sf" {
			t.Errorf("auth %q", r.auth)
		}
		p := strings.TrimPrefix(r.path, "/services/data/v59.0")
		switch {
		case r.method == "POST" && p == "/sobjects/lead":
			return 201, "application/json", map[string]interface{}{"id": "00Q1", "success": true, "errors": []interface{}{}}
		case r.method == "PATCH":
			return 204, "application/json", ""
		case p == "/query":
			q, _ := url.ParseQuery(r.query)
			queries = append(queries, q.Get("q"))
			return 200, "application/json", map[string]interface{}{"records": []interface{}{map[string]interface{}{"Id": "a1"}, map[string]interface{}{"Id": "a2"}},
				"nextRecordsUrl": "/services/data/v59.0/query/01gX-2000"}
		case p == "/query/01gX-2000":
			return 200, "application/json", map[string]interface{}{"records": []interface{}{map[string]interface{}{"Id": "a3"}}}
		}
		return 404, "application/json", []interface{}{map[string]interface{}{"errorCode": "NOT_FOUND", "message": p}}
	})
	cred := map[string]map[string]interface{}{"salesforceOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "sf", "instance_url": "https://x.my.salesforce.com"}}}

	out := restRun(t, f, "salesforce", "salesforce", map[string]interface{}{"company": "Acme", "lastname": "Doe", "additionalFields": map[string]interface{}{
		"email": "d@acme.io", "firstname": "Jo", "owner": map[string]interface{}{"__rl": true, "value": "005A", "mode": "id"},
		"customFieldsUi": map[string]interface{}{"customFieldsValues": []interface{}{map[string]interface{}{"fieldId": "Score__c", "value": 7}}}}}, cred)
	if len(out) != 1 || out[0]["id"] != "00Q1" {
		t.Fatalf("lead create: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/sobjects/lead"))); b != `{"Company":"Acme","Email":"d@acme.io","FirstName":"Jo","LastName":"Doe","OwnerId":"005A","Score__c":7}` {
		t.Fatalf("lead body %s", b)
	}

	out = restRun(t, f, "salesforce", "salesforce", map[string]interface{}{"resource": "opportunity", "operation": "update", "opportunityId": "006X",
		"updateFields": map[string]interface{}{"stageName": "Closed Won", "amount": 10}}, cred)
	if out[0]["success"] != true || mustJSON(out[0]["errors"]) != "[]" {
		t.Fatalf("update: %v", out)
	}
	if r := f.find(t, "PATCH", "/sobjects/opportunity/006X"); mustJSON(jsonBody(t, r)) != `{"Amount":10,"StageName":"Closed Won"}` {
		t.Fatalf("update body %s", r.body)
	}

	out = restRun(t, f, "salesforce", "salesforce", map[string]interface{}{"resource": "contact", "operation": "upsert", "lastname": "Doe", "externalId": "Ext__c", "externalIdValue": "e 1",
		"additionalFields": map[string]interface{}{"customFieldsUi": map[string]interface{}{"customFieldsValues": []interface{}{map[string]interface{}{"fieldId": "Ext__c", "value": "e 1"}}}}}, cred)
	if r := f.find(t, "PATCH", "/sobjects/contact/Ext__c/e 1"); out[0]["success"] != true || mustJSON(jsonBody(t, r)) != `{"LastName":"Doe"}` {
		t.Fatalf("upsert: %v %s", out, r.body)
	}

	out = restRun(t, f, "salesforce", "salesforce", map[string]interface{}{"resource": "account", "operation": "getAll", "returnAll": true}, cred)
	if len(out) != 3 || out[2]["Id"] != "a3" || queries[0] != "SELECT id,name,type,LastModifiedDate FROM Account " {
		t.Fatalf("account getAll: %v %q", out, queries)
	}
	restRun(t, f, "salesforce", "salesforce", map[string]interface{}{"resource": "contact", "operation": "getAll", "limit": 5, "options": map[string]interface{}{
		"fields": "Id,Email", "conditionsUi": map[string]interface{}{"conditionValues": []interface{}{
			map[string]interface{}{"field": "Email", "operation": "equal", "value": "o'k@x.io"},
			map[string]interface{}{"field": "NumberOfEmployees", "operation": ">", "value": "10"}}}}}, cred)
	if q := queries[1]; q != `SELECT Id,Email FROM Contact WHERE Email = 'o\'k@x.io' AND NumberOfEmployees > 10 LIMIT 5` {
		t.Fatalf("contact getAll query %q", q)
	}
	out = restRun(t, f, "salesforce", "salesforce", map[string]interface{}{"resource": "search", "query": "SELECT Id FROM Lead"}, cred)
	if len(out) != 3 || queries[2] != "SELECT Id FROM Lead" {
		t.Fatalf("search: %v %q", out, queries)
	}
}
