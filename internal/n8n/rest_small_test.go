package n8n

import (
	"net/http"
	"strings"
	"testing"
)

func TestSmallRESTServices(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.path == "/mail/send":
			return 202, "application/json", ""
		case r.path == "/marketing/lists" && r.method == "GET":
			if strings.Contains(r.query, "page=2") {
				return 200, "application/json", map[string]interface{}{"result": []interface{}{map[string]interface{}{"id": "l3"}}, "_metadata": map[string]interface{}{}}
			}
			return 200, "application/json", map[string]interface{}{"result": []interface{}{map[string]interface{}{"id": "l1"}, map[string]interface{}{"id": "l2"}}, "_metadata": map[string]interface{}{"next": "URL/marketing/lists?page=2"}}
		case r.path == "/v3/mg.example.com/messages":
			return 200, "application/json", map[string]interface{}{"id": "<m1>", "message": "Queued. Thank you."}
		case r.path == "/translate":
			return 200, "application/json", map[string]interface{}{"translations": []interface{}{map[string]interface{}{"detected_source_language": "EN", "text": "Hallo"}}}
		case r.path == "/language/translate/v2":
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"translations": []interface{}{map[string]interface{}{"translatedText": "Merhaba"}}}}
		case r.path == "/v2/companies/find":
			return 200, "application/json", map[string]interface{}{"name": "Acme"}
		case strings.HasPrefix(r.path, "/https:"):
			return 200, "application/json", map[string]interface{}{"code": 200, "data": map[string]interface{}{"title": "Example", "content": "# Hi"}}
		}
		return 404, "application/json", map[string]interface{}{"path": r.path}
	})
	// The fake's next-page URL needs the server address.
	f.srv.Config.Handler = rewriteNext(f.srv.Config.Handler, f.srv.URL)

	out := restRun(t, f, "sendGrid", "sendGrid", map[string]interface{}{"resource": "mail", "fromEmail": "a@x.com", "fromName": "A", "toEmail": "b@x.com, c@x.com",
		"subject": "Hi", "contentValue": "Body", "additionalFields": map[string]interface{}{"ccEmail": "d@x.com"}}, map[string]map[string]interface{}{"sendGridApi": {"apiKey": "SG.k"}})
	if len(out) != 1 {
		t.Fatalf("sendgrid: %v", out)
	}
	sg := f.find(t, "POST", "/mail/send")
	b := jsonBody(t, sg)
	if sg.auth != "Bearer SG.k" || mustJSON(jpath(b, "personalizations.0.to")) != `[{"email":"b@x.com"},{"email":"c@x.com"}]` || jpath(b, "personalizations.0.subject") != "Hi" || jpath(b, "content.0.type") != "text/plain" || jpath(b, "personalizations.0.cc.0.email") != "d@x.com" {
		t.Fatalf("sendgrid body: %s", sg.body)
	}
	out = restRun(t, f, "sendGrid", "sendGrid", map[string]interface{}{"resource": "list", "operation": "getAll", "returnAll": true}, map[string]map[string]interface{}{"sendGridApi": {"apiKey": "SG.k"}})
	if len(out) != 3 || out[2]["id"] != "l3" {
		t.Fatalf("sendgrid lists: %v", out)
	}

	out = restRun(t, f, "mailgun", "mailgun", map[string]interface{}{"fromEmail": "a@x.com", "toEmail": "b@x.com", "subject": "S", "text": "T", "html": ""},
		map[string]map[string]interface{}{"mailgunApi": {"apiDomain": "api.eu.mailgun.net", "emailDomain": "mg.example.com", "apiKey": "key-1"}})
	if out[0]["message"] != "Queued. Thank you." {
		t.Fatalf("mailgun: %v", out)
	}
	if mg := f.find(t, "POST", "/messages"); !strings.HasPrefix(mg.auth, "Basic ") || !strings.Contains(mg.body, `name="subject"`) {
		t.Fatalf("mailgun request: %+v", mg)
	}

	out = restRun(t, f, "deepL", "deepL", map[string]interface{}{"text": "Hello", "translateTo": "DE", "additionalFields": map[string]interface{}{"sourceLang": "EN-GB"}},
		map[string]map[string]interface{}{"deepLApi": {"apiKey": "dk", "apiPlan": "free"}})
	if out[0]["text"] != "Hallo" {
		t.Fatalf("deepl: %v", out)
	}
	if d := f.find(t, "POST", "/translate"); d.auth != "DeepL-Auth-Key dk" || !strings.Contains(d.body, "source_lang=EN&") && !strings.HasSuffix(d.body, "source_lang=EN") || !strings.Contains(d.body, "target_lang=DE") {
		t.Fatalf("deepl request: %+v", d)
	}

	out = restRun(t, f, "translate", "googleTranslate", map[string]interface{}{"text": "Hello", "translateTo": "tr"},
		map[string]map[string]interface{}{"googleTranslateOAuth2Api": {"accessToken": "g"}})
	if out[0]["translatedText"] != "Merhaba" {
		t.Fatalf("google translate: %v", out)
	}

	out = restRun(t, f, "clearbit", "clearbit", map[string]interface{}{"domain": "acme.com"}, map[string]map[string]interface{}{"clearbitApi": {"apiKey": "cb"}})
	if out[0]["name"] != "Acme" || f.find(t, "GET", "/v2/companies/find").query != "domain=acme.com" {
		t.Fatalf("clearbit: %v", out)
	}

	out = restRun(t, f, "jinaAi", "jinaAi", map[string]interface{}{"url": "https://example.com", "options": map[string]interface{}{"outputFormat": "markdown"}},
		map[string]map[string]interface{}{"jinaAiApi": {"apiKey": "jk"}})
	if out[0]["title"] != "Example" {
		t.Fatalf("jina: %v", out)
	}
}

// rewriteNext replaces "URL" in responses with the server's address.
func rewriteNext(h http.Handler, base string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &replaceWriter{ResponseWriter: w, base: base}
		h.ServeHTTP(rw, r)
	})
}

type replaceWriter struct {
	http.ResponseWriter
	base string
}

func (w *replaceWriter) Write(b []byte) (int, error) {
	_, err := w.ResponseWriter.Write([]byte(strings.ReplaceAll(string(b), `"URL/`, `"`+w.base+`/`)))
	return len(b), err
}

func TestMistralOCR(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		return 200, "application/json", map[string]interface{}{"pages": []interface{}{map[string]interface{}{"markdown": "# A"}, map[string]interface{}{"markdown": "B"}}}
	})
	out := restRun(t, f, "mistral", "mistralAi", map[string]interface{}{"inputType": "url", "url": "https://x/doc.pdf"}, map[string]map[string]interface{}{"mistralCloudApi": {"apiKey": "mk"}})
	if out[0]["extractedText"] != "# A\n\nB" || out[0]["pageCount"] != 2.0 {
		t.Fatalf("ocr: %v", out)
	}
	if b := f.find(t, "POST", "/v1/ocr").body; !strings.Contains(b, `"document_url":"https://x/doc.pdf"`) || !strings.Contains(b, `"model":"mistral-ocr-latest"`) {
		t.Fatalf("body %s", b)
	}
}
