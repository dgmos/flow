package n8n

import (
	"net/url"
	"strings"
	"testing"
)

func TestZohoCrmNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer z-tok" {
			t.Errorf("auth %q", r.auth)
		}
		details := map[string]interface{}{"data": []interface{}{map[string]interface{}{"code": "SUCCESS", "status": "success", "details": map[string]interface{}{"id": "77"}}}}
		switch {
		case r.method == "POST" && (r.path == "/crm/v2/contacts" || r.path == "/crm/v2/sales_orders/upsert"):
			return 201, "application/json", details
		case r.method == "PUT" && r.path == "/crm/v2/leads/L1":
			return 200, "application/json", map[string]interface{}{"data": []interface{}{map[string]interface{}{"status": "error", "message": "invalid data"}}}
		case r.method == "GET" && r.path == "/crm/v2/deals":
			if q, _ := url.ParseQuery(r.query); q.Get("page") == "2" {
				return 200, "application/json", map[string]interface{}{"data": []interface{}{map[string]interface{}{"id": "d3"}}, "info": map[string]interface{}{"more_records": false}}
			}
			return 200, "application/json", map[string]interface{}{"data": []interface{}{map[string]interface{}{"id": "d1"}, map[string]interface{}{"id": "d2"}}, "info": map[string]interface{}{"more_records": true}}
		case r.method == "GET" && r.path == "/crm/v2/contacts/C1":
			return 200, "application/json", map[string]interface{}{"data": []interface{}{map[string]interface{}{"id": "C1", "Last_Name": "Doe"}}}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"zohoOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "z-tok", "api_domain": "https://www.zohoapis.eu"}}}

	out := restRun(t, f, "zohoCrm", "zohoCrm", map[string]interface{}{"resource": "contact", "lastName": "Doe", "additionalFields": map[string]interface{}{
		"Date_of_Birth": "1990-05-01T00:00:00", "Mailing_Address": map[string]interface{}{"address_fields": map[string]interface{}{"Mailing_City": "Berlin"}},
		"customFields": map[string]interface{}{"customFields": []interface{}{map[string]interface{}{"fieldId": "Lead_Score", "value": 5}}}}}, cred)
	if out[0]["id"] != "77" {
		t.Fatalf("contact create: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/crm/v2/contacts"))); b != `{"data":[{"Date_of_Birth":"1990-05-01","Last_Name":"Doe","Lead_Score":5,"Mailing_City":"Berlin"}]}` {
		t.Fatalf("contact body %s", b)
	}

	out = restRun(t, f, "zohoCrm", "zohoCrm", map[string]interface{}{"resource": "contact", "operation": "get", "contactId": "C1"}, cred)
	if out[0]["Last_Name"] != "Doe" {
		t.Fatalf("contact get: %v", out)
	}

	// getAll: limit defaults to 5 and pages while more_records.
	out = restRun(t, f, "zohoCrm", "zohoCrm", map[string]interface{}{"resource": "deal", "operation": "getAll", "options": map[string]interface{}{"fields": []interface{}{"Deal_Name", "Stage"}}}, cred)
	if len(out) != 3 || out[2]["id"] != "d3" {
		t.Fatalf("deal getAll: %v", out)
	}
	if q := f.find(t, "GET", "/crm/v2/deals").query; !strings.Contains(q, "fields=Deal_Name%2CStage") || !strings.Contains(q, "per_page=200") {
		t.Fatalf("deal getAll query %q", q)
	}
	out = restRun(t, f, "zohoCrm", "zohoCrm", map[string]interface{}{"resource": "deal", "operation": "getAll", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("deal getAll limit: %v", out)
	}

	restRun(t, f, "zohoCrm", "zohoCrm", map[string]interface{}{"resource": "salesOrder", "operation": "upsert", "accountId": "A1", "subject": "SO",
		"Product_Details": []interface{}{map[string]interface{}{"id": "P1", "list_price": 3}}, "additionalFields": map[string]interface{}{"dealId": "D9"}}, cred)
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/sales_orders/upsert"))); b != `{"data":[{"Account_Name":{"id":"A1"},"Deal_Name":{"id":"D9"},"Product_Details":[{"list_price":3,"product":{"id":"P1"},"quantity":1}],"Subject":"SO"}]}` {
		t.Fatalf("sales order body %s", b)
	}
}
