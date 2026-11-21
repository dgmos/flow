package n8n

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// restRunV is restRun for a given node version, returning full items
// (binary data included).
func restRunItems(t *testing.T, f *fakeAPI, apiKey, nodeType string, version float64, params map[string]interface{}, creds map[string]map[string]interface{}) []Item {
	t.Helper()
	sc := NewStaticCredentials()
	var credType string
	for k, v := range creds {
		sc.Set(k, "*", v)
		credType = k
	}
	h := newHarness(t, &Options{Credentials: sc, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{apiKey: f.srv.URL}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(map[string]interface{}{"name": "Svc", "type": "n8n-nodes-base." + nodeType, "typeVersion": version, "parameters": params}, credType),
	}, "Start->Svc")
	_, steps := h.run(t, data, "")
	if steps["Svc"].Error != "" {
		t.Fatalf("node failed: %s", steps["Svc"].Error)
	}
	return decodeOutput(steps["Svc"].Output)[0]
}

func TestQuickBooksNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer qb-token" {
			t.Errorf("auth %q", r.auth)
		}
		p := strings.TrimPrefix(r.path, "/v3/company/123")
		switch {
		case r.method == "GET" && p == "/query":
			if strings.Contains(r.query, "STARTPOSITION+1001") {
				return 200, "application/json", map[string]interface{}{"QueryResponse": map[string]interface{}{"Customer": []interface{}{map[string]interface{}{"Id": "last"}}}}
			}
			n := 1000
			if strings.Contains(r.query, "MAXRESULTS+2") {
				n = 2
			}
			list := make([]interface{}, n)
			for k := range list {
				list[k] = map[string]interface{}{"Id": k}
			}
			return 200, "application/json", map[string]interface{}{"QueryResponse": map[string]interface{}{"Customer": list}}
		case r.method == "POST" && p == "/invoice":
			return 200, "application/json", map[string]interface{}{"Invoice": map[string]interface{}{"Id": "7"}, "time": "now"}
		case r.method == "POST" && p == "/invoice/7/send":
			return 200, "application/json", map[string]interface{}{"Invoice": map[string]interface{}{"Id": "7", "EmailStatus": "EmailSent"}}
		case r.method == "GET" && p == "/invoice/7/pdf":
			return 200, "application/pdf", "%PDF-1.4"
		case r.method == "GET" && p == "/customer/5":
			return 200, "application/json", map[string]interface{}{"Customer": map[string]interface{}{"Id": "5", "SyncToken": "3"}}
		case r.method == "POST" && p == "/customer":
			return 200, "application/json", map[string]interface{}{"Customer": map[string]interface{}{"Id": "5", "SyncToken": "4"}}
		}
		return 404, "application/json", map[string]interface{}{"Fault": r.path}
	})
	cred := map[string]map[string]interface{}{"quickBooksOAuth2Api": {"environment": "sandbox",
		"oauthTokenData": map[string]interface{}{"access_token": "qb-token", "callbackQueryString": map[string]interface{}{"realmId": "123"}}}}

	// Default resource customer; returnAll pages by STARTPOSITION.
	out := restRunItems(t, f, "quickbooks", "quickbooks", 1, map[string]interface{}{"operation": "getAll", "returnAll": true,
		"filters": map[string]interface{}{"query": "WHERE Active = true"}}, cred)
	if len(out) != 1001 || out[1000].JSON["Id"] != "last" {
		t.Fatalf("customer getAll: %d items", len(out))
	}
	if q := f.find(t, "GET", "/query").query; q != "query=SELECT+%2A+FROM+Customer+WHERE+Active+%3D+true+MAXRESULTS+1000+STARTPOSITION+1" {
		t.Fatalf("getAll query %q", q)
	}
	out = restRunItems(t, f, "quickbooks", "quickbooks", 1, map[string]interface{}{"operation": "getAll", "limit": 2}, cred)
	if len(out) != 2 {
		t.Fatalf("customer getAll limit: %d", len(out))
	}

	out = restRunItems(t, f, "quickbooks", "quickbooks", 1, map[string]interface{}{"resource": "invoice", "operation": "create", "CustomerRef": "42",
		"Line":             []interface{}{map[string]interface{}{"DetailType": "SalesItemLineDetail", "Amount": 10, "Description": "Widget", "itemId": "3", "Qty": 2, "TaxCodeRef": "TAX"}},
		"additionalFields": map[string]interface{}{"BillEmail": "c@d.e", "DocNumber": "A-1"}}, cred)
	if out[0].JSON["Id"] != "7" {
		t.Fatalf("invoice create: %v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/invoice"))
	if mustJSON(b["CustomerRef"]) != `{"value":"42"}` || mustJSON(b["BillEmail"]) != `{"Address":"c@d.e"}` || b["DocNumber"] != "A-1" ||
		mustJSON(b["Line"]) != `[{"Amount":10,"Description":"Widget","DetailType":"SalesItemLineDetail","SalesItemLineDetail":{"ItemRef":{"value":"3"},"Qty":2,"TaxCodeRef":{"value":"TAX"}}}]` {
		t.Fatalf("invoice body: %v", mustJSON(b))
	}

	out = restRunItems(t, f, "quickbooks", "quickbooks", 1, map[string]interface{}{"resource": "invoice", "operation": "send", "invoiceId": "7", "email": "c@d.e"}, cred)
	if out[0].JSON["EmailStatus"] != "EmailSent" {
		t.Fatalf("invoice send: %v", out)
	}
	if r := f.find(t, "POST", "/invoice/7/send"); r.query != "sendTo=c%40d.e" || r.ctype != "application/octet-stream" {
		t.Fatalf("send request: %+v", r)
	}

	// Default operation (get) with download returns the PDF as binary data.
	out = restRunItems(t, f, "quickbooks", "quickbooks", 1, map[string]interface{}{"resource": "invoice", "invoiceId": "7", "download": true,
		"binaryProperty": "pdf", "fileName": "inv.pdf"}, cred)
	bin := asMap(out[0].Binary["pdf"])
	if raw, _ := base64.StdEncoding.DecodeString(toString(bin["data"])); string(raw) != "%PDF-1.4" || bin["fileName"] != "inv.pdf" || bin["mimeType"] != "application/pdf" {
		t.Fatalf("download: %v", out[0].Binary)
	}

	out = restRunItems(t, f, "quickbooks", "quickbooks", 1, map[string]interface{}{"operation": "update", "customerId": "5",
		"updateFields": map[string]interface{}{"PrimaryEmailAddr": "x@y.z"}}, cred)
	if out[0].JSON["SyncToken"] != "4" {
		t.Fatalf("customer update: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "POST", "/customer"))); b != `{"Id":"5","PrimaryEmailAddr":{"Address":"x@y.z"},"SyncToken":"3","sparse":true}` {
		t.Fatalf("update body %s", b)
	}
}
