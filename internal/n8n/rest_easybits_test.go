package n8n

import (
	"strings"
	"testing"
)

func TestEasybitsExtractorNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"invoice_number": "42"}}
	})
	creds := map[string]map[string]interface{}{"easybitsExtractorApi": {"pipelineId": "pipe1", "apiKey": "eb-key"}}
	for _, v := range []float64{1, 2} {
		out := communityRun(t, f, "easybits", "@easybits/n8n-nodes-extractor.easybitsExtractor", v, map[string]interface{}{}, creds, "\x89PNG\r\n\x1a\nxx", "scan.png")
		if len(out) != 1 || asMap(out[0].JSON["data"])["invoice_number"] != "42" {
			t.Fatalf("v%v: %+v", v, out)
		}
	}
	r := f.find(t, "POST", "/api/pipelines/pipe1")
	files, _ := jsonBody(t, r)["files"].([]interface{})
	if r.auth != "Bearer eb-key" || len(files) != 1 || !strings.HasPrefix(files[0].(string), "data:image/png;base64,") {
		t.Fatalf("request: %+v", r)
	}
}
