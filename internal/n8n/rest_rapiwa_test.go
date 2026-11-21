package n8n

import (
	"strings"
	"testing"
)

func TestRapiwaNode(t *testing.T) {
	const typ = "n8n-nodes-rapiwa.rapiwa"
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.header.Get("Authorization") != "Bearer rk" {
			t.Errorf("auth %q", r.header.Get("Authorization"))
		}
		switch r.path {
		case "/send-message":
			if strings.Contains(r.body, "fail") {
				return 200, "", map[string]interface{}{"success": false, "error": "no device"}
			}
			return 200, "", map[string]interface{}{"success": true, "id": "m1"}
		case "/verify-whatsapp":
			return 200, "", map[string]interface{}{"success": true, "data": map[string]interface{}{"exists": true, "jid": "8801@s.whatsapp.net"}}
		}
		return 404, "", map[string]interface{}{}
	})
	bases := map[string]string{"rapiwa": ""}
	cred := map[string]map[string]interface{}{"rapiwaApi": {"apiKey": "rk"}}

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"number": "+880 17-1", "message": "Hi"}, cred, nil)
	b := f.find(t, "POST", "/send-message").json(t)
	if b["number"] != "880171" || b["message"] != "Hi" || b["message_type"] != "text" || b["productOperation"] != "welcome" {
		t.Fatalf("send body %v", b)
	}
	if out[0].JSON["id"] != "m1" {
		t.Fatalf("send out %v", out[0].JSON)
	}

	cmtyRun(t, f, bases, typ, map[string]interface{}{"number": "1", "messageType": "document", "mediaUrl": "https://x.io/a/report.pdf"}, cred, nil)
	b = f.find(t, "POST", "/send-message").json(t)
	if b["file_name"] != "report.pdf" || b["mimetype"] != "application/pdf" || b["message"] != nil {
		t.Fatalf("document body %v", b)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "verifyWhatsAppNumber", "number": "8801"}, cred, nil)
	if j := out[0].JSON; j["exists"] != true || j["number"] != "8801" || j["jid"] != "8801@s.whatsapp.net" || j["success"] != true {
		t.Fatalf("verify out %v", j)
	}
	n := f.count("POST", "/verify-whatsapp")
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "verifyWhatsAppNumber", "number": "123@lid"}, cred, nil)
	if f.count("POST", "/verify-whatsapp") != n || out[0].JSON["jid"] != "123@lid" {
		t.Fatalf("@lid numbers skip verification: %v", out[0].JSON)
	}

	if e := cmtyErr(t, f, bases, typ, map[string]interface{}{"number": "1", "message": "fail"}, cred); !strings.Contains(e, "no device") {
		t.Fatalf("success:false should fail: %q", e)
	}
}
