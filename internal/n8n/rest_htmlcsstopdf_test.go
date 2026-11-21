package n8n

import (
	"strings"
	"testing"
)

func TestHTMLCSSToPDFNode(t *testing.T) {
	const typ = "n8n-nodes-htmlcsstopdf.htmlcsstopdf"
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.header.Get("CLIENT-API-KEY") != "pk" {
			t.Errorf("api key %q", r.header.Get("CLIENT-API-KEY"))
		}
		file := strings.Contains(r.body, `"output_format":"file"`) || strings.Contains(r.body, `"output":"file"`) || strings.Contains(r.body, "name=\"output\"\r\n\r\nfile")
		switch {
		case file:
			return 200, "application/pdf", "%PDF-1"
		case r.path == "/generatePdf":
			return 200, "", map[string]interface{}{"pdf_url": "https://pdfmunk.com/x.pdf"}
		case r.path == "/pdf/parse":
			return 200, "", map[string]interface{}{"text": "hello"}
		}
		return 200, "", map[string]interface{}{"url": "https://pdfmunk.com/out.pdf"}
	})
	bases := map[string]string{"htmlcsstopdf": ""}
	cred := map[string]map[string]interface{}{"htmlcsstopdfApi": {"apiKey": "pk"}}

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"html_content": "<h1>Hi</h1>", "dynamic_params": map[string]interface{}{"params": []interface{}{map[string]interface{}{"key": "name", "value": "Ada"}}}}, cred, nil)
	b := f.find(t, "POST", "/generatePdf").json(t)
	if b["html_content"] != "<h1>Hi</h1>" || b["output_format"] != "url" || b["output_filename"] != "document" || b["viewPortWidth"] != 1080.0 ||
		b["css_content"] != "" || mustJSON(b["dynamic_params"]) != `[{"name":"Ada"}]` || b["output"] != nil {
		t.Fatalf("html body %v", b)
	}
	if out[0].JSON["pdf_url"] == nil {
		t.Fatalf("html out %v", out[0].JSON)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "urlToPdf", "url": "https://a.io", "output_format": "file", "output_filename": "page"}, cred, nil)
	data, mt, name, err := binaryOf(out[0], "data")
	if err != nil || string(data) != "%PDF-1" || mt != "application/pdf" || name != "page.pdf" || out[0].JSON["success"] != true {
		t.Fatalf("url file: %q %s %s %v", data, mt, name, err)
	}

	// compress a binary input; the output defaults to a file
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "pdfManipulation", "operation": "compressPdf", "compress_input_type": "file"}, cred, &cmtyInput{"in.pdf", "application/pdf", "RAWPDF"})
	r := f.find(t, "POST", "/compressPdf")
	if !strings.Contains(r.body, "RAWPDF") || !strings.Contains(r.body, `filename="in.pdf"`) || !strings.Contains(r.body, "name=\"compression\"\r\n\r\nhigh") {
		t.Fatalf("compress body %q", r.body)
	}
	if _, _, name, _ := binaryOf(out[0], "data"); name != "compressed.pdf" {
		t.Fatalf("compress name %q", name)
	}

	cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "pdfManipulation", "pdf_urls": "https://a/1.pdf, https://a/2.pdf"}, cred, nil)
	if b := f.find(t, "POST", "/pdf/merge").json(t); mustJSON(b["urls"]) != `["https://a/1.pdf","https://a/2.pdf"]` || b["output"] != "url" {
		t.Fatalf("merge body %v", b)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "pdfSecurity", "lock_url": "https://a/1.pdf", "lock_password": "s", "lock_output": "url"}, cred, nil)
	if b := f.find(t, "POST", "/lockPdf").json(t); b["password"] != "s" || b["url"] != "https://a/1.pdf" || b["output_name"] != "locked.pdf" {
		t.Fatalf("lock body %v", b)
	}
	// operation name used by some templates
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "pdfParsing", "operation": "parsePdf", "parse_url": "https://a/1.pdf"}, cred, nil)
	if b := f.find(t, "POST", "/pdf/parse").json(t); b["mode"] != "full" || b["pages"] != "all" || out[0].JSON["text"] != "hello" {
		t.Fatalf("parse body %v", b)
	}
}
