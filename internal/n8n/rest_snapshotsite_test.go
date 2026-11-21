package n8n

import (
	"testing"
)

func TestSnapshotSiteNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		return 200, "application/json", map[string]interface{}{"ok": true, "path": r.path}
	})
	key := headerSpy(f, "X-SnapshotSiteAPI-Key")
	creds := map[string]map[string]interface{}{"snapshotSiteApi": {"apiKey": "ss-key", "baseUrl": "https://api.prod.ss.snapshot-site.com"}}
	const typ = "n8n-nodes-snapshot-site.snapshotSite"

	out := communityRun(t, f, "snapshotSite", typ, 1, map[string]interface{}{"url": "https://example.com",
		"additionalFields": map[string]interface{}{"format": "jpeg", "fullSize": true, "hideCookie": false, "width": 1280}}, creds, "", "")
	if out[0].JSON["path"] != "/api/v2/screenshot" || key("/api/v2/screenshot") != "ss-key" {
		t.Fatalf("screenshot: %+v", out)
	}
	if b := jsonBody(t, f.find(t, "POST", "/api/v2/screenshot")); b["url"] != "https://example.com" || b["format"] != "jpeg" || b["fullSize"] != true || b["hideCookie"] != false || b["width"] != 1280.0 {
		t.Fatalf("screenshot body: %v", b)
	}

	communityRun(t, f, "snapshotSite", typ, 1, map[string]interface{}{"operation": "analyze", "url": "https://a.com",
		"additionalFields": map[string]interface{}{"enableSummary": true}}, creds, "", "")
	if b := jsonBody(t, f.find(t, "POST", "/api/v3/analyze")); b["enableSummary"] != true || b["url"] != "https://a.com" {
		t.Fatalf("analyze body: %v", b)
	}

	communityRun(t, f, "snapshotSite", typ, 1, map[string]interface{}{"operation": "compare", "beforeUrl": "https://a.com", "afterUrl": "https://b.com",
		"beforeOptions": map[string]interface{}{"width": 800, "hide": ""}}, creds, "", "")
	b := jsonBody(t, f.find(t, "POST", "/api/v3/compare"))
	if before := asMap(b["before"]); before["url"] != "https://a.com" || before["width"] != 800.0 || before["hide"] != nil ||
		asMap(b["after"])["url"] != "https://b.com" || b["threshold"] != 0.1 {
		t.Fatalf("compare body: %v", b)
	}
}
