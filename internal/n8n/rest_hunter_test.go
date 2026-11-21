package n8n

import (
	"strings"
	"testing"
)

func TestHunterNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if !strings.Contains(r.query, "api_key=K1") {
			t.Errorf("no api key: %q", r.query)
		}
		switch r.path {
		case "/domain-search":
			email := "a@x.io"
			if strings.Contains(r.query, "offset=100") {
				email = "c@x.io"
			}
			return 200, "application/json", map[string]interface{}{
				"data": map[string]interface{}{"domain": "x.io", "emails": []interface{}{map[string]interface{}{"value": email}}},
				"meta": map[string]interface{}{"results": 150, "offset": 0},
			}
		case "/email-verifier":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"status": "valid"}}
		case "/email-finder":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"email": "jo@x.io"}}
		}
		return 404, "application/json", map[string]interface{}{}
	})
	cred := map[string]map[string]interface{}{"hunterApi": {"apiKey": "K1"}}

	out := restRun(t, f, "hunter", "hunter", map[string]interface{}{"domain": "x.io", "filters": map[string]interface{}{"seniority": []interface{}{"senior", "executive"}}}, cred)
	if len(out) != 1 || out[0]["value"] != "a@x.io" {
		t.Fatalf("domainSearch: %v", out)
	}
	if q := f.find(t, "GET", "/domain-search").query; !strings.Contains(q, "limit=100") || !strings.Contains(q, "domain=x.io") || !strings.Contains(q, "seniority=senior%2Cexecutive") {
		t.Fatalf("domainSearch query %q", q)
	}

	out = restRun(t, f, "hunter", "hunter", map[string]interface{}{"domain": "x.io", "returnAll": true, "onlyEmails": false}, cred)
	if len(out) != 1 || out[0]["domain"] != "x.io" || len(asList(out[0]["emails"])) != 2 {
		t.Fatalf("domainSearch returnAll: %v", out)
	}

	out = restRun(t, f, "hunter", "hunter", map[string]interface{}{"operation": "emailVerifier", "email": "a@x.io"}, cred)
	if len(out) != 1 || out[0]["status"] != "valid" || !strings.Contains(f.find(t, "GET", "/email-verifier").query, "email=a%40x.io") {
		t.Fatalf("emailVerifier: %v", out)
	}
	out = restRun(t, f, "hunter", "hunter", map[string]interface{}{"operation": "emailFinder", "domain": "x.io", "firstname": "Jo", "lastname": "Do"}, cred)
	if q := f.find(t, "GET", "/email-finder").query; out[0]["email"] != "jo@x.io" || !strings.Contains(q, "first_name=Jo") || !strings.Contains(q, "last_name=Do") {
		t.Fatalf("emailFinder: %v %q", out, q)
	}
}
