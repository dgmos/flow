package n8n

import (
	"testing"
)

func TestScrapeGraphAINode(t *testing.T) {
	const typ = "n8n-nodes-scrapegraphai.scrapegraphAi"
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.header.Get("SGAI-APIKEY") != "sk" {
			t.Errorf("api key %q", r.header.Get("SGAI-APIKEY"))
		}
		switch r.path {
		case "/v1/smartscraper":
			return 200, "", map[string]interface{}{"request_id": "r1", "result": map[string]interface{}{"title": "T"}}
		case "/v1/markdownify":
			return 200, "", map[string]interface{}{"result": "# T"}
		case "/v2/scrape":
			return 200, "", map[string]interface{}{"id": "s1", "results": map[string]interface{}{"markdown": "x"}, "debug": "dropped"}
		case "/v2/history":
			return 200, "", map[string]interface{}{"data": []interface{}{
				map[string]interface{}{"id": "h1", "service": "scrape", "status": "done", "params": map[string]interface{}{"url": "https://a.io"}, "result": "big"},
				map[string]interface{}{"id": "h2", "service": "crawl"}}}
		case "/v2/crawl/c1":
			return 200, "", map[string]interface{}{"id": "c1", "status": "done", "pages": []interface{}{map[string]interface{}{"url": "u", "html": "big"}}}
		}
		return 404, "", map[string]interface{}{}
	})
	bases := map[string]string{"scrapegraphAi": "/v1", "scrapegraphAiV2": "/v2"}
	cred := map[string]map[string]interface{}{"scrapegraphAIApi": {"apiKey": "sk"}}

	// package 0.1.x: Smart Scraper is the default
	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"websiteUrl": "https://a.io", "userPrompt": "title", "useOutputSchema": true,
		"outputSchema": `{"type":"object"}`, "enablePagination": true, "totalPages": 3}, cred, nil)
	b := f.find(t, "POST", "/v1/smartscraper").json(t)
	if b["website_url"] != "https://a.io" || b["user_prompt"] != "title" || b["render_heavy_js"] != false || b["total_pages"] != 3.0 || mustJSON(b["output_schema"]) != `{"type":"object"}` {
		t.Fatalf("smartscraper body %v", b)
	}
	if out[0].JSON["request_id"] != "r1" {
		t.Fatalf("smartscraper out %v", out[0].JSON)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "markdownify", "websiteUrl": "https://a.io", "renderHeavyJs": true}, cred, nil)
	if b := f.find(t, "POST", "/v1/markdownify").json(t); b["render_heavy_js"] != true {
		t.Fatalf("markdownify body %v", b)
	}

	// package 1.x: Scrape is the default and calls API v2
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"url": "https://a.io", "formats": map[string]interface{}{"format": []interface{}{
		map[string]interface{}{"type": "json", "prompt": "p", "schema": "{}"}}}, "fetchConfig": map[string]interface{}{"stealth": true, "timeout": 30000}}, cred, nil)
	b = f.find(t, "POST", "/v2/scrape").json(t)
	if mustJSON(b["formats"]) != `[{"prompt":"p","type":"json"}]` || mustJSON(b["fetchConfig"]) != `{"stealth":true}` || b["url"] != "https://a.io" {
		t.Fatalf("v2 scrape body %v", b)
	}
	if out[0].JSON["debug"] != nil || out[0].JSON["id"] != "s1" {
		t.Fatalf("simplified output %v", out[0].JSON)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "history", "operation": "list", "service": "scrape", "limit": 10}, cred, nil)
	if q := f.find(t, "GET", "/v2/history").query; q.Get("service") != "scrape" || q.Get("limit") != "10" || q.Get("page") != "" {
		t.Fatalf("history query %v", q)
	}
	if len(out) != 2 || out[0].JSON["url"] != "https://a.io" || out[0].JSON["result"] != nil {
		t.Fatalf("history out %v", out)
	}
	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "crawl", "operation": "getStatus", "crawlId": map[string]interface{}{"__rl": true, "mode": "id", "value": "c1"}}, cred, nil)
	if p := asMap(asList(out[0].JSON["pages"])[0]); p["url"] != "u" || p["html"] != nil {
		t.Fatalf("crawl status %v", out[0].JSON)
	}
}
