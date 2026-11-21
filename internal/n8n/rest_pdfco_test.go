package n8n

import (
	"testing"
	"time"
)

func TestPDFcoNode(t *testing.T) {
	old := pdfcoPollInterval
	pdfcoPollInterval = time.Millisecond
	t.Cleanup(func() { pdfcoPollInterval = old })
	var base string
	checks := 0
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch r.path {
		case "/v1/pdf/convert/from/html":
			return 200, "application/json", map[string]interface{}{"jobId": "J1", "url": "https://cdn/out.pdf", "name": "out.pdf", "credits": 2}
		case "/v1/job/check":
			checks++
			if checks == 1 {
				return 200, "application/json", map[string]interface{}{"status": "working"}
			}
			return 200, "application/json", map[string]interface{}{"status": "success", "url": "https://cdn/out.pdf", "credits": 2}
		case "/v1/file/upload/get-presigned-url":
			return 200, "application/json", map[string]interface{}{"presignedUrl": base + "/put/a.pdf", "url": "https://files.pdf.co/a.pdf"}
		case "/put/a.pdf":
			return 200, "text/plain", ""
		case "/v1/pdf/convert/to/text":
			return 200, "application/json", map[string]interface{}{"url": base + "/result.txt", "pageCount": 1, "error": false}
		case "/result.txt":
			return 200, "text/plain", "Hello text"
		case "/v1/pdf/split":
			return 200, "application/json", map[string]interface{}{"urls": []interface{}{"https://cdn/1.pdf"}, "error": false}
		}
		return 404, "application/json", map[string]interface{}{"error": true, "message": "nope"}
	})
	base = f.srv.URL
	key := headerSpy(f, "x-api-key")
	creds := map[string]map[string]interface{}{"pdfcoApi": {"apiKey": "pk-1", "baseUrl": "https://api.pdf.co"}}
	const typ = "n8n-nodes-pdfco.PDFco Api"

	// HTML template to PDF, polled until the job succeeds.
	out := communityRun(t, f, "pdfco", typ, 1.1, map[string]interface{}{"operation": "URL/HTML to PDF", "convertType": "htmlTemplateToPDF", "templateId": "7",
		"templateData": `{"a":1}`, "advancedOptions": map[string]interface{}{"name": "inv.pdf", "custom": "A5", "profiles": `"x": 1`}}, creds, "", "")
	if out[0].JSON["url"] != "https://cdn/out.pdf" || out[0].JSON["name"] != "out.pdf" || out[0].JSON["credits"] != nil || out[0].JSON["status"] != nil || checks != 2 {
		t.Fatalf("html to pdf: %+v (checks %d)", out, checks)
	}
	b := jsonBody(t, f.find(t, "POST", "/v1/pdf/convert/from/html"))
	if b["templateId"] != "7" || b["async"] != true || b["paperSize"] != "A5" || b["profiles"] != `{ "x": 1 }` || key("/v1/pdf/convert/from/html") != "pk-1" {
		t.Fatalf("html body: %v", b)
	}

	// Upload a binary file through a presigned URL.
	out = communityRun(t, f, "pdfco", typ, 1, map[string]interface{}{"operation": "Upload File to PDF.co", "name": "a.pdf", "binaryData": true, "binaryPropertyName": "data"}, creds, "%PDF-1.7", "a.pdf")
	if out[0].JSON["url"] != "https://files.pdf.co/a.pdf" || f.find(t, "PUT", "/put/a.pdf").body != "%PDF-1.7" {
		t.Fatalf("upload: %+v", out)
	}
	if q := f.find(t, "GET", "/v1/file/upload/get-presigned-url").query; q != "name=a.pdf" {
		t.Fatalf("presign query %q", q)
	}

	// Convert from PDF downloads the inline result.
	out = communityRun(t, f, "pdfco", typ, 1, map[string]interface{}{"operation": "Convert from PDF", "url": "https://x/a.pdf"}, creds, "", "")
	if out[0].JSON["body"] != "Hello text" {
		t.Fatalf("convert from: %+v", out)
	}

	communityRun(t, f, "pdfco", typ, 1, map[string]interface{}{"operation": "Split PDF", "url": "https://x/a.pdf", "pages": "1-2"}, creds, "", "")
	if b := jsonBody(t, f.find(t, "POST", "/v1/pdf/split")); b["pages"] != "1-2" || b["inline"] != true {
		t.Fatalf("split body: %v", b)
	}
}
