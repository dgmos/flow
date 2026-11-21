package n8n

import (
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

func TestHubspotNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer pat-1" {
			t.Errorf("auth %q on %s", r.auth, r.path)
		}
		switch {
		case r.method == "POST" && strings.HasPrefix(r.path, "/contacts/v1/contact/createOrUpdate/email/"):
			return 200, "application/json", map[string]interface{}{"vid": 42, "isNew": true}
		case r.path == "/contacts/v1/contact/vid/42/profile":
			return 200, "application/json", map[string]interface{}{"vid": 42, "properties": map[string]interface{}{"email": map[string]interface{}{"value": "a@x.io"}}}
		case r.path == "/crm/v3/objects/companies/search":
			return 200, "application/json", map[string]interface{}{"results": []interface{}{map[string]interface{}{"id": "c1"}}}
		case r.path == "/deals/v1/deal/paged":
			if strings.Contains(r.query, "offset=2") {
				return 200, "application/json", map[string]interface{}{"deals": []interface{}{map[string]interface{}{"dealId": 3}}, "hasMore": false}
			}
			return 200, "application/json", map[string]interface{}{"deals": []interface{}{map[string]interface{}{"dealId": 1}, map[string]interface{}{"dealId": 2}}, "hasMore": true, "offset": 2}
		}
		return 404, "application/json", map[string]interface{}{"message": "nope"}
	})
	creds := NewStaticCredentials()
	creds.Set("hubspotAppToken", "*", map[string]interface{}{"appToken": "pat-1"})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{"hubspot": f.srv.URL}})
	hs := func(name string, p map[string]interface{}) map[string]interface{} {
		p["authentication"] = "appToken"
		return credNode(map[string]interface{}{"name": name, "type": "n8n-nodes-base.hubspot", "typeVersion": 2.1, "parameters": p}, "hubspotAppToken")
	}
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		hs("Upsert", map[string]interface{}{"email": "a@x.io", "additionalFields": map[string]interface{}{"firstName": "Ada", "closeDate": "2024-01-02T00:00:00Z",
			"customPropertiesUi": map[string]interface{}{"customPropertiesValues": []interface{}{map[string]interface{}{"property": "tier", "value": "gold"}}}}, "options": map[string]interface{}{}}),
		hs("Search", map[string]interface{}{"resource": "company", "operation": "search", "returnAll": false, "limit": 5,
			"filterGroupsUi": map[string]interface{}{"filterGroupsValues": []interface{}{map[string]interface{}{"filtersUi": map[string]interface{}{"filterValues": []interface{}{
				map[string]interface{}{"propertyName": "domain|string", "operator": "EQ", "value": "x.io", "type": "string"}}}}}}}),
		hs("Deals", map[string]interface{}{"resource": "deal", "operation": "getAll", "returnAll": true, "filters": map[string]interface{}{}}),
	}, "Start->Upsert", "Start->Search", "Start->Deals")
	_, rep, _ := Import(data, ImportOptions{})
	if !rep.Runnable {
		t.Fatalf("not runnable: %+v", rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	if got := itemsOf(t, steps["Upsert"], 0); len(got) != 1 || got[0]["isNew"] != true || got[0]["vid"] != 42.0 {
		t.Fatalf("upsert: %v (%s)", got, steps["Upsert"].Error)
	}
	if b := f.find(t, "POST", "/createOrUpdate/email/a@x.io").body; !strings.Contains(b, `{"property":"firstname","value":"Ada"}`) ||
		!strings.Contains(b, `{"property":"closedate","value":1704153600000}`) || !strings.Contains(b, `{"property":"tier","value":"gold"}`) {
		t.Fatalf("upsert body: %s", b)
	}
	if b := f.find(t, "POST", "/companies/search").body; !strings.Contains(b, `"propertyName":"domain"`) || strings.Contains(b, `"type"`) || !strings.Contains(b, `"limit":5`) {
		t.Fatalf("search body: %s", b)
	}
	if got := itemsOf(t, steps["Deals"], 0); len(got) != 3 || got[2]["dealId"] != 3.0 {
		t.Fatalf("deals pagination: %v (%s)", got, steps["Deals"].Error)
	}
}
