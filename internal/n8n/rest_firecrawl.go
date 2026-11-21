package n8n

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Firecrawl (community node @mendable/n8n-nodes-firecrawl.firecrawl), API v2.
// The node is declarative: every displayed parameter is sent, defaults
// included, and the body always carries "integration": "n8n". Package 1.0
// had a single "Default" resource; operations without a resource resolve to
// their current resource.

// firecrawlPoll is the agent status polling interval (tests shorten it).
var firecrawlPoll = 2 * time.Second

var firecrawlSelfRouted = map[string]bool{"formats": true, "includeTags": true, "excludeTags": true, "actions": true, "location": true, "webhook": true}

func init() {
	ops := map[string]*restOp{
		"Scraping/scrape": firecrawlPost("/scrape", func(a *cmtyArgs, b map[string]interface{}) error {
			b["url"] = a.get("url", "https://firecrawl.dev")
			firecrawlParsers(a, b)
			firecrawlScrapeOptions(a, b, false)
			if s := asMap(asMap(a.get("profile", nil))["settings"]); len(s) > 0 {
				b["profile"] = map[string]interface{}{"name": s["name"], "saveChanges": s["saveChanges"]}
			}
			return nil
		}, ""),
		"Scraping/batchScrape": firecrawlPost("/batch/scrape", func(a *cmtyArgs, b map[string]interface{}) error {
			urls := firecrawlURLs(a.get("urls", "https://firecrawl.dev"))
			if len(urls) == 0 {
				return permanentError{"At least one valid URL is required. Provide URLs as a single URL, comma-separated list, one per line, or a JSON array."}
			}
			b["urls"] = urls
			firecrawlParsers(a, b)
			firecrawlScrapeOptions(a, b, false)
			return nil
		}, "data"),
		"Scraping/batchScrapeStatus": {path: "/batch/scrape/{batchId}", post: firecrawlData},
		"Scraping/batchScrapeErrors": {path: "/batch/scrape/{batchId}/errors"},
		"Scraping/cancelBatchScrape": {method: "DELETE", path: "/batch/scrape/{batchId}"},
		"Crawling/crawl": firecrawlPost("/crawl", func(a *cmtyArgs, b map[string]interface{}) error {
			b["url"] = a.get("url", "https://firecrawl.dev")
			if p := a.str("prompt", ""); p != "" {
				b["prompt"] = p
			}
			b["limit"] = a.get("limit", 50.0)
			b["delay"] = a.get("delay", 0.0)
			b["maxConcurrency"] = a.get("maxConcurrency", 100.0)
			for _, k := range []string{"excludePaths", "includePaths"} {
				var paths []interface{}
				for _, x := range asList(asMap(a.get(k, nil))["items"]) {
					paths = append(paths, asMap(x)["path"])
				}
				if paths != nil {
					b[k] = paths
				}
			}
			for k, v := range asMap(a.get("crawlOptions", nil)) {
				b[k] = v
			}
			firecrawlScrapeOptions(a, b, true)
			return nil
		}, ""),
		"Crawling/getCrawlStatus": {path: "/crawl/{crawlId}", post: firecrawlData},
		"Crawling/cancelCrawl":    {method: "DELETE", path: "/crawl/{crawlId}"},
		"Crawling/getCrawlErrors": {path: "/crawl/{crawlId}/errors"},
		"Crawling/crawlActive":    {path: "/crawl/active"},
		"Crawling/crawlParamsPreview": firecrawlPost("/crawl/params-preview", func(a *cmtyArgs, b map[string]interface{}) error {
			b["prompt"] = a.get("prompt", "")
			return nil
		}, ""),
		"Agent/agent":          {run: firecrawlAgent(true)},
		"Agent/agentAsync":     {run: firecrawlAgent(false)},
		"Agent/getAgentStatus": {path: "/agent/{agentId}", post: firecrawlData},
		"MapSearch/map": firecrawlPost("/map", func(a *cmtyArgs, b map[string]interface{}) error {
			b["url"] = a.get("url", nil)
			if restEmpty(b["url"]) {
				b["url"] = a.get("mapUrl", "https://firecrawl.dev")
			}
			sitemap := a.str("sitemap", "include")
			// package 1.x (API v1) options
			if a.bool("sitemapOnly", false) {
				sitemap = "only"
			} else if a.has("ignoreSitemap") && a.bool("ignoreSitemap", false) && !a.has("sitemap") {
				sitemap = "skip"
			}
			b["sitemap"] = sitemap
			b["includeSubdomains"] = a.bool("includeSubdomains", false)
			b["limit"] = a.get("limit", 50.0)
			b["timeout"] = a.get("timeout", 10000.0)
			return nil
		}, ""),
		"MapSearch/search": firecrawlPost("/search", func(a *cmtyArgs, b map[string]interface{}) error {
			b["query"] = a.get("query", "")
			var src []interface{}
			for _, s := range asList(a.get("sources", []interface{}{"web"})) {
				src = append(src, map[string]interface{}{"type": s})
			}
			b["sources"] = src
			b["timeout"] = a.get("timeout", 60000.0)
			b["limit"] = a.get("limit", 50.0)
			if t := a.str("tbs", ""); t != "" {
				b["tbs"] = t
			}
			return nil
		}, ""),
		"Account/teamCreditUsage":          {path: "/team/credit-usage"},
		"Account/creditUsageHistorical":    {path: "/team/credit-usage/historical"},
		"Account/teamTokenUsage":           {path: "/team/token-usage"},
		"Account/teamTokenUsageHistorical": {path: "/team/token-usage/historical"},
		"Account/teamQueueStatus":          {path: "/team/queue-status"},
		"Interact/interactContext": {path: "/browser", post: func(ec *execCtx, i int, v interface{}) (interface{}, error) {
			ctxText, _ := ec.str(i, "interactContextText", "")
			var sessions []interface{}
			for _, s := range asList(asMap(v)["sessions"]) {
				m := asMap(s)
				if m["status"] == "active" {
					sessions = append(sessions, map[string]interface{}{"id": m["id"], "status": m["status"], "createdAt": m["createdAt"], "lastActivity": m["lastActivity"]})
				}
			}
			if sessions == nil {
				sessions = []interface{}{}
			}
			return map[string]interface{}{"context": ctxText, "activeSessions": len(sessions), "sessions": sessions}, nil
		}},
		"Interact/interact": firecrawlPost("/scrape/{scrapeId}/interact", func(a *cmtyArgs, b map[string]interface{}) error {
			if a.str("interactMode", "prompt") == "code" {
				b["code"] = a.get("code", "")
				b["language"] = a.get("language", "node")
			} else {
				b["prompt"] = a.get("prompt", "")
			}
			b["timeout"] = a.get("timeout", 30.0)
			return nil
		}, ""),
		"Interact/interactStop": {method: "DELETE", path: "/scrape/{scrapeId}/interact"},
		"Extract/extract": firecrawlPost("/extract", func(a *cmtyArgs, b map[string]interface{}) error {
			urls := []interface{}{}
			for _, x := range asList(asMap(a.get("urls", nil))["items"]) {
				urls = append(urls, asMap(x)["url"])
			}
			b["urls"] = urls
			b["prompt"] = a.get("prompt", "")
			s, err := cmtyJSON(a.get("schema", `{"property1": "string"}`), "Schema")
			if err != nil {
				return err
			}
			b["schema"] = s
			b["enableWebSearch"] = a.bool("enableWebSearch", false)
			b["ignoreSitemap"] = a.bool("ignoreSitemap", true)
			b["includeSubdomains"] = a.bool("includeSubdomains", false)
			b["showSources"] = a.bool("showSources", false)
			return nil
		}, ""),
		"Extract/getExtractStatus": {path: "/extract/{extractId}", post: firecrawlData},
	}
	aliases := map[string]string{}
	for k := range ops {
		o := k[strings.Index(k, "/")+1:]
		aliases["Default/"+o] = k
		if !strings.HasPrefix(k, "Scraping/") {
			aliases["Scraping/"+o] = k
		}
	}
	registerREST("@mendable/n8n-nodes-firecrawl.firecrawl", &restService{
		defResource: "Scraping",
		defOps: map[string]string{"Scraping": "scrape", "Crawling": "crawl", "Agent": "agent", "MapSearch": "map", "Account": "teamCreditUsage",
			"Interact": "interactContext", "Extract": "extract", "Default": "map"},
		aliases: aliases,
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "firecrawlApi", nil)
			if err != nil {
				return nil, err
			}
			base := strings.TrimRight(firstNonEmpty(credString(c, "baseUrl"), "https://api.firecrawl.dev/v2"), "/")
			return bearerClient(ec.apiBase("firecrawl", base), c, "apiKey"), nil
		},
		ops: ops,
	})
}

// firecrawlData wraps a response as {"data": response} (setKeyValue).
func firecrawlData(_ *execCtx, _ int, v interface{}) (interface{}, error) {
	return map[string]interface{}{"data": v}, nil
}

// firecrawlPost is a POST whose body is built by fn, with the node's custom
// body and custom properties merged in; out wraps the response in a key.
func firecrawlPost(path string, fn func(a *cmtyArgs, b map[string]interface{}) error, out string) *restOp {
	op := &restOp{method: "POST", path: path, build: func(ec *execCtx, i int, r *restRequest) error {
		a := &cmtyArgs{ec: ec, i: i}
		b := r.body
		custom := a.bool("useCustomBody", false)
		if custom {
			// Only the URL is shown next to a custom body.
			if a.has("url") {
				b["url"] = a.get("url", "")
			}
		} else if err := fn(a, b); err != nil {
			return err
		}
		if a.err != nil {
			return a.err
		}
		if custom {
			cb, err := cmtyJSON(a.get("customBody", ""), "Custom Body")
			if err != nil {
				return err
			}
			for k, v := range asMap(cb) {
				b[k] = v
			}
		}
		if cp := toString(fieldsOf(ec, i, "additionalFields")["customProperties"]); cp != "" {
			var m map[string]interface{}
			if json.Unmarshal([]byte(cp), &m) == nil {
				for k, v := range m {
					b[k] = v
				}
			}
		}
		b["integration"] = "n8n"
		return nil
	}}
	if out != "" {
		op.post = firecrawlData
	}
	return op
}

func firecrawlParsers(a *cmtyArgs, b map[string]interface{}) {
	var flat func(v interface{}) []string
	flat = func(v interface{}) []string {
		switch t := v.(type) {
		case string:
			var x interface{}
			if json.Unmarshal([]byte(t), &x) == nil {
				return flat(x)
			}
			if s := strings.ToLower(strings.TrimSpace(t)); s != "" {
				return []string{s}
			}
		case []interface{}:
			var out []string
			for _, x := range t {
				out = append(out, flat(x)...)
			}
			return out
		case map[string]interface{}:
			if s, ok := t["type"].(string); ok {
				return []string{strings.ToLower(s)}
			}
		}
		return nil
	}
	var parsers []interface{}
	for _, p := range flat(a.get("parsers", nil)) {
		if p == "pdf" && len(parsers) == 0 {
			parsers = append(parsers, map[string]interface{}{"type": "pdf"})
		}
	}
	if parsers != nil {
		b["parsers"] = parsers
	}
}

// firecrawlScrapeOptions applies the Scrape Options collection, at the top
// level or nested under "scrapeOptions".
func firecrawlScrapeOptions(a *cmtyArgs, b map[string]interface{}, nested bool) {
	opts := asMap(asMap(a.get("scrapeOptions", nil))["options"])
	dst := b
	if nested {
		dst = map[string]interface{}{}
	}
	for k, v := range opts {
		if !firecrawlSelfRouted[k] {
			dst[k] = v
		}
	}
	if f := asList(asMap(opts["formats"])["format"]); len(f) > 0 {
		var formats []interface{}
		for _, x := range f {
			m := asMap(x)
			t := toString(m["type"])
			switch t {
			case "json", "changeTracking":
				out := map[string]interface{}{"type": t}
				if p := toString(m["prompt"]); p != "" {
					out["prompt"] = p
				}
				if s, err := cmtyJSON(m["schema"], "Schema"); err == nil && len(asMap(s)) > 0 {
					out["schema"] = s
				}
				if !restEmpty(m["modes"]) {
					out["modes"] = m["modes"]
				}
				if tag := toString(m["tag"]); tag != "" {
					out["tag"] = tag
				}
				formats = append(formats, out)
			case "screenshot":
				out := map[string]interface{}{"type": t}
				if fp, ok := m["fullPage"]; ok {
					out["fullPage"] = fp
				}
				if q := m["quality"]; !restEmpty(q) {
					out["quality"] = q
				}
				if !restEmpty(m["viewportWidth"]) && !restEmpty(m["viewportHeight"]) {
					out["viewport"] = map[string]interface{}{"width": m["viewportWidth"], "height": m["viewportHeight"]}
				}
				formats = append(formats, out)
			default:
				formats = append(formats, t)
			}
		}
		dst["formats"] = formats
	}
	for _, k := range []string{"includeTags", "excludeTags"} {
		var tags []interface{}
		for _, x := range asList(asMap(opts[k])["items"]) {
			if t := toString(asMap(x)["tag"]); t != "" {
				tags = append(tags, t)
			}
		}
		if tags != nil {
			dst[k] = tags
		}
	}
	if acts := asList(asMap(opts["actions"])["items"]); len(acts) > 0 {
		dst["actions"] = acts
	}
	if s := asMap(asMap(opts["location"])["settings"]); len(s) > 0 {
		langs := []interface{}{}
		for _, l := range asList(asMap(s["languages"])["language"]) {
			langs = append(langs, asMap(l)["code"])
		}
		dst["location"] = map[string]interface{}{"country": s["country"], "languages": langs}
	}
	if s := asMap(asMap(opts["webhook"])["settings"]); len(s) > 0 {
		hdr := map[string]interface{}{}
		for _, h := range asList(asMap(s["headers"])["header"]) {
			hdr[toString(asMap(h)["key"])] = asMap(h)["value"]
		}
		meta, _ := cmtyJSON(s["metadata"], "Metadata")
		if meta == nil {
			meta = map[string]interface{}{}
		}
		ev := s["events"]
		if ev == nil {
			ev = []interface{}{}
		}
		dst["webhook"] = map[string]interface{}{"url": s["url"], "headers": hdr, "metadata": meta, "events": ev}
	}
	if nested && len(dst) > 0 {
		b["scrapeOptions"] = dst
	}
}

var firecrawlURLLike = regexp.MustCompile(`^\S*\.\S*$`)

// firecrawlURLs extracts URLs from a string (one per line, comma separated
// or a JSON array) or a list, adding https:// when missing.
func firecrawlURLs(v interface{}) []interface{} {
	var out []interface{}
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && !firecrawlURLLike.MatchString(s) {
			return
		}
		if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			s = "https://" + s
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch t := v.(type) {
		case []interface{}:
			for _, x := range t {
				walk(x)
			}
		case map[string]interface{}:
			if u, ok := t["url"].(string); ok {
				add(u)
			}
		case string:
			s := strings.TrimSpace(t)
			if strings.HasPrefix(s, "[") {
				var x interface{}
				if json.Unmarshal([]byte(s), &x) == nil {
					walk(x)
					return
				}
			}
			switch {
			case strings.Contains(s, "\n"):
				for _, p := range strings.Split(s, "\n") {
					add(p)
				}
			case strings.Contains(s, ","):
				for _, p := range strings.Split(s, ",") {
					add(p)
				}
			case strings.Contains(s, " http"):
				for _, p := range strings.Fields(s) {
					add(p)
				}
			default:
				add(s)
			}
		}
	}
	walk(v)
	return out
}

// firecrawlSchema infers a JSON schema from an example value.
func firecrawlSchema(v interface{}) map[string]interface{} {
	switch t := v.(type) {
	case nil:
		return map[string]interface{}{"type": "null"}
	case []interface{}:
		if len(t) == 0 {
			return map[string]interface{}{"type": "array", "items": map[string]interface{}{}}
		}
		return map[string]interface{}{"type": "array", "items": firecrawlSchema(t[0])}
	case string:
		return map[string]interface{}{"type": "string"}
	case float64:
		if t == math.Trunc(t) {
			return map[string]interface{}{"type": "integer"}
		}
		return map[string]interface{}{"type": "number"}
	case bool:
		return map[string]interface{}{"type": "boolean"}
	case map[string]interface{}:
		props := map[string]interface{}{}
		for k, x := range t {
			props[k] = firecrawlSchema(x)
		}
		return map[string]interface{}{"type": "object", "properties": props}
	}
	return map[string]interface{}{}
}

// firecrawlAgent starts an agent job; wait polls it until it finishes or
// maxWaitTime passes.
func firecrawlAgent(wait bool) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	start := firecrawlPost("/agent", func(a *cmtyArgs, b map[string]interface{}) error {
		b["prompt"] = a.get("prompt", "")
		if a.bool("specifyUrls", false) {
			if urls := firecrawlURLs(a.get("urls", "")); len(urls) > 0 {
				b["urls"] = urls
			}
		}
		switch a.str("schemaType", "none") {
		case "fromExample":
			ex, err := cmtyJSON(a.get("jsonExample", `{"founders": [{"name": "John Doe", "role": "CEO"}]}`), "JSON Example")
			if err != nil {
				return permanentError{"Invalid JSON example. Please provide valid JSON."}
			}
			if ex != nil {
				b["schema"] = firecrawlSchema(ex)
			}
		case "manual":
			s, err := cmtyJSON(a.get("schema", ""), "Schema")
			if err != nil {
				return err
			}
			b["schema"] = s
		}
		return nil
	}, "")
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		req, err := start.request(ec, i)
		if err != nil {
			return nil, err
		}
		v, err := c.do(ec, req)
		if err != nil {
			return nil, err
		}
		job := asMap(v)
		id := toString(job["id"])
		if !wait || id == "" {
			return restItems(v), nil
		}
		maxWait, _ := ec.num(i, "maxWaitTime", 300)
		deadline := time.Now().Add(time.Duration(maxWait * float64(time.Second)))
		status := firstNonEmpty(toString(job["status"]), "processing")
		for status == "processing" || status == "pending" {
			if time.Now().After(deadline) {
				return restItems(map[string]interface{}{"success": false, "status": "timeout", "error": "Agent job timed out after " + toString(maxWait) + " seconds",
					"jobId": id, "message": "You can check the status manually using the \"Get Agent Status\" operation."}), nil
			}
			if err := cmtySleep(ec, firecrawlPoll); err != nil {
				return nil, err
			}
			var st interface{}
			var lastErr error
			for attempt := 0; attempt < 3; attempt++ {
				if st, lastErr = cmtyCall(ec, c, "GET", "/agent/"+url.PathEscape(id), nil, nil); lastErr == nil {
					break
				}
				if attempt < 2 {
					if err := cmtySleep(ec, firecrawlPoll*time.Duration(attempt+1)); err != nil {
						return nil, err
					}
				}
			}
			if lastErr != nil {
				return restItems(map[string]interface{}{"success": false, "status": "poll_failed", "error": "Failed to poll agent status after 3 attempts",
					"jobId": id, "lastError": lastErr.Error(), "message": "You can check the status manually using the \"Get Agent Status\" operation."}), nil
			}
			status = toString(asMap(st)["status"])
			if status == "completed" || status == "failed" {
				return restItems(st), nil
			}
		}
		return restItems(v), nil
	}
}
