package n8n

import (
	"net/url"
	"strings"
	"testing"
)

func TestXeroNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer x-tok" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/Invoices":
			return 200, "application/json", map[string]interface{}{"Invoices": []interface{}{map[string]interface{}{"InvoiceID": "I1"}}}
		case r.method == "GET" && r.path == "/Contacts":
			q, _ := url.ParseQuery(r.query)
			switch q.Get("page") {
			case "", "1":
				return 200, "application/json", map[string]interface{}{"Contacts": []interface{}{map[string]interface{}{"ContactID": "c1"}, map[string]interface{}{"ContactID": "c2"}}}
			case "2":
				return 200, "application/json", map[string]interface{}{"Contacts": []interface{}{map[string]interface{}{"ContactID": "c3"}}}
			}
			return 200, "application/json", map[string]interface{}{"Contacts": []interface{}{}}
		case r.method == "POST" && r.path == "/Contacts/c1":
			return 200, "application/json", map[string]interface{}{"Contacts": []interface{}{map[string]interface{}{"ContactID": "c1", "Name": "New"}}}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"xeroOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "x-tok"}}}

	out := restRun(t, f, "xero", "xero", map[string]interface{}{"organizationId": "T1", "type": "ACCREC", "contactId": "C9",
		"lineItemsUi":      map[string]interface{}{"lineItemsValues": []interface{}{map[string]interface{}{"description": "Work", "quantity": 2, "unitAmount": "50"}}},
		"additionalFields": map[string]interface{}{"reference": "R1", "dueDate": "2024-02-01"}}, cred)
	if out[0]["InvoiceID"] != "I1" {
		t.Fatalf("invoice create: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/Invoices"))); b != `{"Contact":{"ContactID":"C9"},"DueDate":"2024-02-01","LineItems":[{"Description":"Work","Quantity":"2","Tracking":[],"UnitAmount":"50"}],"Reference":"R1","Type":"ACCREC"}` {
		t.Fatalf("invoice body %s", b)
	}

	out = restRun(t, f, "xero", "xero", map[string]interface{}{"resource": "contact", "operation": "getAll", "organizationId": "T1", "returnAll": true,
		"options": map[string]interface{}{"orderBy": "Name", "where": `Name!=null`}}, cred)
	if len(out) != 3 {
		t.Fatalf("contact getAll: %v", out)
	}
	if q := f.find(t, "GET", "/Contacts").query; !strings.Contains(q, "order=Name+DESC") || !strings.Contains(q, "page=1") {
		t.Fatalf("contact getAll query %q", q)
	}
	out = restRun(t, f, "xero", "xero", map[string]interface{}{"resource": "contact", "operation": "getAll", "organizationId": "T1", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("contact getAll limit: %v", out)
	}

	out = restRun(t, f, "xero", "xero", map[string]interface{}{"resource": "contact", "operation": "update", "organizationId": "T1", "contactId": "c1",
		"updateFields": map[string]interface{}{"name": "New", "emailAddress": "a@b.c"}}, cred)
	if out[0]["Name"] != "New" {
		t.Fatalf("contact update: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/Contacts/c1"))); b != `{"Contacts":[{"EmailAddress":"a@b.c","Name":"New"}]}` {
		t.Fatalf("contact update body %s", b)
	}
}
