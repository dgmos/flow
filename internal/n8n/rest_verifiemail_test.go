package n8n

import "testing"

func TestVerifiEmailNode(t *testing.T) {
	const typ = "n8n-nodes-verifiemail.verifiEmail"
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.query.Get("token") != "vk" {
			t.Errorf("token %v", r.query)
		}
		switch r.path {
		case "/check":
			return 200, "", map[string]interface{}{"valid": true, "email": "x"}
		case "/v1/bulk/check":
			return 200, "", map[string]interface{}{"results": []interface{}{}}
		case "/v1/domain/check":
			return 200, "", map[string]interface{}{"spf": true}
		}
		return 404, "", map[string]interface{}{}
	})
	bases := map[string]string{"verifiEmail": ""}
	cred := map[string]map[string]interface{}{"verifiEmailApi": {"apiKey": "vk"}}

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"email": "a@b.co"}, cred, nil)
	if r := f.find(t, "GET", "/check"); r.query.Get("email") != "a@b.co" {
		t.Fatalf("validate query %v", r.query)
	}
	// response fields override the echoed email, as n8n spreads them last
	if out[0].JSON["email"] != "x" || out[0].JSON["valid"] != true {
		t.Fatalf("validate: %v", out[0].JSON)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "bulkValidateEmails", "emailInputMethod": "commaSeparated", "emailsCommaSeparated": `a@b.co, "c@d.co"`}, cred, nil)
	if b := f.find(t, "POST", "/v1/bulk/check").json(t); mustJSON(b["emails"]) != `["a@b.co","c@d.co"]` {
		t.Fatalf("bulk body %v", b)
	}
	if mustJSON(out[0].JSON["emails"]) != `["a@b.co","c@d.co"]` {
		t.Fatalf("bulk out %v", out[0].JSON)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "checkDomainHealth", "domain": "b.co"}, cred, nil)
	if r := f.find(t, "GET", "/v1/domain/check"); r.query.Get("domain") != "b.co" || r.query.Get("selector") != "google" {
		t.Fatalf("domain query %v", r.query)
	}
	if out[0].JSON["selector"] != "google" || out[0].JSON["spf"] != true {
		t.Fatalf("domain out %v", out[0].JSON)
	}
	if cmtyRunnable(t, typ, map[string]interface{}{"operation": "teleport"}) {
		t.Fatal("unknown operation should block import")
	}
}
