package n8n

import (
	"encoding/base64"
	"testing"
)

func TestHtmlCssToImageNode(t *testing.T) {
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.method != "POST" || r.path != "/api/v1/generateImage" {
			return 404, "", map[string]interface{}{"error": r.path}
		}
		if r.json(t)["response_format"] == "png" {
			return 200, "image/png", "\x89PNGdata"
		}
		return 200, "", map[string]interface{}{"success": true, "imageUrl": "https://img/1.png"}
	})
	bases := map[string]string{"htmlCssToImage": ""}
	creds := map[string]map[string]interface{}{"htmlcsstoimgApi": {"apiKey": "k1"}}
	typ := "n8n-nodes-htmlcsstoimage.htmlCssToImage"

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"html_content": "<p>Hi</p>", "css_content": "p{}"}, creds, nil)
	r := f.find(t, "POST", "/api/v1/generateImage")
	b := r.json(t)
	if out[0].JSON["imageUrl"] != "https://img/1.png" || r.header.Get("CLIENT-API-KEY") != "k1" || b["html_content"] != "<p>Hi</p>" ||
		b["css_content"] != "p{}" || b["response_format"] != "url" || b["viewPortWidth"] != float64(1080) {
		t.Fatalf("htmlToImage: %v %v", out, b)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"html_content": "<p>Hi</p>", "response_format_html": "png"}, creds, nil)
	bin := asMap(out[0].Binary["data"])
	if d, _ := base64.StdEncoding.DecodeString(toString(bin["data"])); string(d) != "\x89PNGdata" || bin["mimeType"] != "image/png" {
		t.Fatalf("png: %v", out)
	}

	cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "urlToImage", "url": "https://example.com", "response_format_url": "url"}, creds, nil)
	if b := f.find(t, "POST", "/api/v1/generateImage").json(t); b["url"] != "https://example.com" || b["full_page"] != true || b["wait_till"] != float64(10000) {
		t.Fatalf("urlToImage body: %v", b)
	}
}
