package n8n

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestUploadToURLNode(t *testing.T) {
	const typ = "n8n-nodes-uploadtourl.uploadToUrl"
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if strings.HasPrefix(r.path, "/files/") {
			if r.header.Get("x-api-key") != "" {
				t.Errorf("downloads must not carry the API key")
			}
			return 200, "application/pdf; charset=binary", "PDFDATA"
		}
		if r.header.Get("x-api-key") != "uk" {
			t.Errorf("x-api-key %q", r.header.Get("x-api-key"))
		}
		switch {
		case r.method == "POST" && r.path == "/api/upload":
			return 200, "", map[string]interface{}{"url": "https://uploadtourl.com/f/1", "id": "f1"}
		case r.method == "GET" && r.path == "/api/file/f1":
			return 200, "", map[string]interface{}{"id": "f1", "name": "a.pdf"}
		case r.method == "DELETE" && r.path == "/api/file/f1":
			return 200, "", map[string]interface{}{"deleted": true}
		}
		return 404, "", map[string]interface{}{}
	})
	bases := map[string]string{"uploadToUrl": "/api"}
	cred := map[string]map[string]interface{}{"uploadToUrlApi": {"apiKey": "uk"}}
	file := &cmtyInput{"a.pdf", "application/pdf", "HELLO"}

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"expiryType": "30"}, cred, file)
	r := f.find(t, "POST", "/api/upload")
	if !strings.HasPrefix(r.header.Get("Content-Type"), "multipart/form-data") || !strings.Contains(r.body, `filename="a.pdf"`) ||
		!strings.Contains(r.body, "HELLO") || !strings.Contains(r.body, "name=\"expiry_days\"\r\n\r\n30") || !strings.Contains(r.body, "name=\"source\"\r\n\r\nn8n") {
		t.Fatalf("upload body %q", r.body)
	}
	if out[0].JSON["id"] != "f1" {
		t.Fatalf("upload out %v", out[0].JSON)
	}
	// older versions: operation uploadFile, single property, never expires
	cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "uploadFile", "uploadAllBinary": false, "binaryPropertyName": "data", "expiryType": "never"}, cred, file)
	if r := f.find(t, "POST", "/api/upload"); !strings.Contains(r.body, "\r\n\r\nnever") {
		t.Fatalf("uploadFile body %q", r.body)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"inputType": "base64", "base64Data": "data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("B64")), "fileName": "n.csv"}, cred, nil)
	if r := f.find(t, "POST", "/api/upload"); !strings.Contains(r.body, "Content-Type: text/csv") || !strings.Contains(r.body, "B64") {
		t.Fatalf("base64 body %q", r.body)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "download", "fileUrl": f.srv.URL + "/files/report.pdf", "dataPropertyName": "doc"}, cred, nil)
	data, mt, name, err := binaryOf(out[0], "doc")
	if err != nil || string(data) != "PDFDATA" || mt != "application/pdf" || name != "report.pdf" {
		t.Fatalf("download: %q %s %s %v", data, mt, name, err)
	}
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "uploadFileFromUrl", "fileUrl": f.srv.URL + "/files/report.pdf"}, cred, nil)
	if r := f.find(t, "POST", "/api/upload"); !strings.Contains(r.body, "PDFDATA") || !strings.Contains(r.body, `filename="report.pdf"`) || out[0].JSON["id"] != "f1" {
		t.Fatalf("from url %q", r.body)
	}

	if out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "retrieve", "fileId": "f1"}, cred, nil); out[0].JSON["name"] != "a.pdf" {
		t.Fatalf("retrieve %v", out[0].JSON)
	}
	if out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "delete", "fileId": "f1"}, cred, nil); out[0].JSON["deleted"] != true {
		t.Fatalf("delete %v", out[0].JSON)
	}
	if e := cmtyErr(t, f, bases, typ, map[string]interface{}{}, cred); !strings.Contains(e, "No binary data") {
		t.Fatalf("missing binary: %q", e)
	}
}
