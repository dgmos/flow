package n8n

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGoogleSlidesNode(t *testing.T) {
	var f *fakeAPI
	f = newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.path != "/img.png" && r.auth != "Bearer g-tok" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/presentations":
			return 200, "application/json", map[string]interface{}{"presentationId": "P1"}
		case r.method == "POST" && r.path == "/presentations/P1:batchUpdate":
			return 200, "application/json", map[string]interface{}{"presentationId": "P1", "replies": []interface{}{}}
		case r.method == "GET" && r.path == "/presentations/P1":
			return 200, "application/json", map[string]interface{}{"slides": []interface{}{map[string]interface{}{"objectId": "s1"}, map[string]interface{}{"objectId": "s2"}}}
		case r.method == "GET" && r.path == "/presentations/P1/pages/s1/thumbnail":
			return 200, "application/json", map[string]interface{}{"contentUrl": f.srv.URL + "/img.png", "width": 800}
		case r.path == "/img.png":
			return 200, "image/png", "PNGDATA"
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"googleSlidesOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "g-tok"}}}

	out := restRunV(t, f, "googleSlides", "googleSlides", 2, map[string]interface{}{"title": "Deck"}, cred)
	if out[0]["presentationId"] != "P1" || jsonBody(t, f.find(t, "POST", "/presentations"))["title"] != "Deck" {
		t.Fatalf("create: %v", out)
	}

	restRunV(t, f, "googleSlides", "googleSlides", 2, map[string]interface{}{"operation": "replaceText", "presentationId": "P1",
		"textUi":  map[string]interface{}{"textValues": []interface{}{map[string]interface{}{"text": "{{name}}", "replaceText": "Ada", "matchCase": true}}},
		"options": map[string]interface{}{"revisionId": "r1"}}, cred)
	if b := mustJSON(jsonBody(t, f.find(t, "POST", ":batchUpdate"))); b != `{"requests":[{"replaceAllText":{"containsText":{"matchCase":true,"text":"{{name}}"},"pageObjectIds":[],"replaceText":"Ada"}}]}` {
		t.Fatalf("replaceText body %s", b)
	}

	out = restRunV(t, f, "googleSlides", "googleSlides", 2, map[string]interface{}{"operation": "getSlides", "presentationId": "P1", "limit": 1}, cred)
	if len(out) != 1 || out[0]["objectId"] != "s1" || f.find(t, "GET", "/presentations/P1").query != "fields=slides" {
		t.Fatalf("getSlides: %v", out)
	}

	items := restRunItems(t, f, "googleSlides", "googleSlides", 2, map[string]interface{}{"resource": "page", "operation": "getThumbnail", "presentationId": "P1",
		"pageObjectId": "s1", "download": true}, cred)
	bin := asMap(items[0].Binary["data"])
	raw, _ := base64.StdEncoding.DecodeString(toString(bin["data"]))
	if items[0].JSON["width"] != float64(800) || !strings.Contains(string(raw), "PNGDATA") || bin["fileName"] != "s1.png" {
		t.Fatalf("thumbnail: %v %v", items[0].JSON, bin)
	}
}
