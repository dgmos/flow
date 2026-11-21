package n8n

import (
	"testing"
	"time"
)

func TestFirecrawlNode(t *testing.T) {
	const typ = "@mendable/n8n-nodes-firecrawl.firecrawl"
	old := firecrawlPoll
	firecrawlPoll = time.Millisecond
	t.Cleanup(func() { firecrawlPoll = old })
	polls := 0
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.header.Get("Authorization") != "Bearer fc" {
			t.Errorf("auth %q", r.header.Get("Authorization"))
		}
		switch r.method + " " + r.path {
		case "POST /scrape":
			return 200, "", map[string]interface{}{"success": true, "data": map[string]interface{}{"markdown": "# A"}}
		case "POST /map", "POST /crawl":
			return 200, "", map[string]interface{}{"success": true, "id": "c1"}
		case "GET /crawl/c1":
			return 200, "", map[string]interface{}{"status": "completed", "data": []interface{}{}}
		case "GET /team/credit-usage":
			return 200, "", map[string]interface{}{"data": map[string]interface{}{"remaining_credits": 5}}
		case "POST /agent":
			return 200, "", map[string]interface{}{"success": true, "id": "a1", "status": "processing"}
		case "GET /agent/a1":
			if polls++; polls < 2 {
				return 200, "", map[string]interface{}{"status": "processing"}
			}
			return 200, "", map[string]interface{}{"status": "completed", "data": map[string]interface{}{"founders": []interface{}{}}}
		}
		return 404, "", map[string]interface{}{}
	})
	bases := map[string]string{"firecrawl": ""}
	cred := map[string]map[string]interface{}{"firecrawlApi": {"apiKey": "fc"}}

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"url": "https://a.io", "requestOptions": map[string]interface{}{}, "scrapeOptions": map[string]interface{}{"options": map[string]interface{}{
		"formats":         map[string]interface{}{"format": []interface{}{map[string]interface{}{"type": "markdown"}, map[string]interface{}{"type": "json", "prompt": "p", "schema": "{}"}}},
		"includeTags":     map[string]interface{}{"items": []interface{}{map[string]interface{}{"tag": "main"}}},
		"onlyMainContent": false, "waitFor": 1000}}}, cred, nil)
	b := f.find(t, "POST", "/scrape").json(t)
	if b["url"] != "https://a.io" || b["integration"] != "n8n" || mustJSON(b["formats"]) != `["markdown",{"prompt":"p","type":"json"}]` ||
		mustJSON(b["includeTags"]) != `["main"]` || b["onlyMainContent"] != false || b["waitFor"] != 1000.0 || b["scrapeOptions"] != nil {
		t.Fatalf("scrape body %v", b)
	}
	if asMap(out[0].JSON["data"])["markdown"] != "# A" {
		t.Fatalf("scrape out %v", out[0].JSON)
	}

	// package 1.x: map without a resource, API v1 options
	cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "map", "url": "https://a.io", "sitemapOnly": true, "includeSubdomains": true}, cred, nil)
	if b := f.find(t, "POST", "/map").json(t); b["sitemap"] != "only" || b["includeSubdomains"] != true || b["limit"] != 50.0 || b["timeout"] != 10000.0 {
		t.Fatalf("map body %v", b)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "Crawling", "url": "https://a.io", "limit": 5, "crawlOptions": map[string]interface{}{"allowSubdomains": true},
		"excludePaths":  map[string]interface{}{"items": []interface{}{map[string]interface{}{"path": "blog/.*"}}},
		"scrapeOptions": map[string]interface{}{"options": map[string]interface{}{"formats": map[string]interface{}{"format": []interface{}{map[string]interface{}{"type": "html"}}}}}}, cred, nil)
	if b := f.find(t, "POST", "/crawl").json(t); b["limit"] != 5.0 || b["allowSubdomains"] != true || mustJSON(b["excludePaths"]) != `["blog/.*"]` ||
		mustJSON(b["scrapeOptions"]) != `{"formats":["html"]}` || b["maxConcurrency"] != 100.0 {
		t.Fatalf("crawl body %v", b)
	}
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "Crawling", "operation": "getCrawlStatus", "crawlId": "c1"}, cred, nil)
	if asMap(out[0].JSON["data"])["status"] != "completed" {
		t.Fatalf("crawl status is wrapped in data: %v", out[0].JSON)
	}
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "teamCreditUsage"}, cred, nil)
	if asMap(out[0].JSON["data"])["remaining_credits"] != 5.0 {
		t.Fatalf("credit usage %v", out[0].JSON)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "Agent", "prompt": "founders", "schemaType": "fromExample", "jsonExample": `{"founders":[{"name":"x"}]}`}, cred, nil)
	b = f.find(t, "POST", "/agent").json(t)
	if mustJSON(b["schema"]) != `{"properties":{"founders":{"items":{"properties":{"name":{"type":"string"}},"type":"object"},"type":"array"}},"type":"object"}` {
		t.Fatalf("agent body %v", b)
	}
	if out[0].JSON["status"] != "completed" || f.count("GET", "/agent/a1") != 2 {
		t.Fatalf("agent should poll until completed: %v", out[0].JSON)
	}

	cmtyRun(t, f, bases, typ, map[string]interface{}{"url": "https://b.io", "useCustomBody": true, "customBody": `{"formats":["links"],"integration":"x"}`,
		"scrapeOptions": map[string]interface{}{"options": map[string]interface{}{"waitFor": 5}}}, cred, nil)
	if b := f.find(t, "POST", "/scrape").json(t); b["url"] != "https://b.io" || mustJSON(b["formats"]) != `["links"]` || b["integration"] != "n8n" || b["waitFor"] != nil {
		t.Fatalf("custom body %v", b)
	}
	if !aiSubNodeSupported("@mendable/n8n-nodes-firecrawl.firecrawlTool") {
		t.Fatal("firecrawlTool should be a supported AI tool")
	}
}
