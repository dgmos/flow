package n8n

import (
	"strings"
	"testing"
)

func TestBrightDataNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch {
		case r.path == "/request":
			return 200, "text/html", "<html>ok</html>"
		case r.path == "/datasets/v3/scrape":
			return 200, "application/json", []interface{}{map[string]interface{}{"name": "A"}, map[string]interface{}{"name": "B"}}
		case r.path == "/datasets/v3/trigger":
			return 200, "application/json", map[string]interface{}{"snapshot_id": "s_1"}
		case strings.HasPrefix(r.path, "/datasets/v3/snapshot/"):
			return 200, "application/json", []interface{}{map[string]interface{}{"row": 1}}
		case r.path == "/datasets/filter":
			return 200, "application/json", map[string]interface{}{"snapshot_id": "s_2"}
		}
		return 200, "application/json", map[string]interface{}{"status": "ready"}
	})
	creds := map[string]map[string]interface{}{"brightdataApi": {"token": "bd-token"}}
	const typ = "@brightdata/n8n-nodes-brightdata.brightData"
	rl := func(v string) map[string]interface{} {
		return map[string]interface{}{"__rl": true, "mode": "list", "value": v}
	}

	// Default: Web Unlocker request, Markdown output.
	out := communityRun(t, f, "brightData", typ, 1, map[string]interface{}{"url": "https://example.com", "zone": rl("web_unlocker1"), "country": rl("de"), "data_format": "markdown"}, creds, "", "")
	if out[0].JSON["data"] != "<html>ok</html>" {
		t.Fatalf("request: %+v", out)
	}
	r := f.find(t, "POST", "/request")
	if b := jsonBody(t, r); r.auth != "Bearer bd-token" || b["zone"] != "web_unlocker1" || b["country"] != "de" || b["format"] != "raw" || b["data_format"] != "markdown" || b["method"] != "GET" {
		t.Fatalf("request body: %v", b)
	}

	out = communityRun(t, f, "brightData", typ, 1, map[string]interface{}{"resource": "webScrapper", "dataset_id": rl("gd_l1"), "urls": `[{"url":"https://x.com/a"}]`}, creds, "", "")
	if len(out) != 2 {
		t.Fatalf("scrape: %+v", out)
	}
	sc := f.find(t, "POST", "/datasets/v3/scrape")
	if sc.query != "dataset_id=gd_l1&format=json&include_errors=false" || !strings.HasPrefix(sc.body, `[{"url":"https://x.com/a"}]`) {
		t.Fatalf("scrape request: %+v", sc)
	}

	communityRun(t, f, "brightData", typ, 1, map[string]interface{}{"resource": "webScrapper", "operation": "downloadSnapshot", "snapshot_id": "s_1", "split_into_parts": true, "batch_size": 500}, creds, "", "")
	if q := f.find(t, "GET", "/datasets/v3/snapshot/s_1").query; q != "batch_size=500&format=json&part=1" {
		t.Fatalf("download query %q", q)
	}

	communityRun(t, f, "brightData", typ, 1, map[string]interface{}{"resource": "marketplaceDataset", "operation": "filterDataset", "dataset_id": rl("gd_x"),
		"filter_type": "filter_single", "field_name": "country", "field_value": "TR"}, creds, "", "")
	if b := jsonBody(t, f.find(t, "POST", "/datasets/filter")); b["dataset_id"] != "gd_x" || b["records_limit"] != 100.0 || asMap(b["filter"])["operator"] != "==" {
		t.Fatalf("filter body: %v", b)
	}

	// The older package wraps downloads in {items}.
	out = communityRun(t, f, "brightData", "n8n-nodes-brightdata.brightData", 1, map[string]interface{}{"resource": "webScrapper", "operation": "downloadSnapshot", "snapshot_id": "s_9"}, creds, "", "")
	if len(out) != 1 || len(asList(out[0].JSON["items"])) != 1 {
		t.Fatalf("legacy download: %+v", out)
	}
	if _, ok := integrationToolBase("n8n-nodes-brightdata.brightDataTool"); !ok {
		t.Fatal("brightDataTool not supported")
	}
}
