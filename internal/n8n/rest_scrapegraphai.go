package n8n

import (
	"net/url"
	"strconv"
	"strings"
)

// ScrapeGraphAI (community node n8n-nodes-scrapegraphai.scrapegraphAi).
// Package 0.1.x calls API v1 (resources smartscraper, searchscraper,
// smartcrawler, markdownify, scrape, agenticscraper); 1.x calls API v2
// (scrape, extract, search, crawl, monitor, history, credits). Both use node
// version 1, so an unset resource runs Smart Scraper unless the node carries
// only v2 parameters, and scrape/scrape picks the API by its parameters.

func init() {
	v1 := func(path string, body func(a *cmtyArgs) (map[string]interface{}, error)) *restOp {
		return &restOp{run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
			a := &cmtyArgs{ec: ec, i: i}
			b, err := body(a)
			if err != nil {
				return nil, err
			}
			if a.err != nil {
				return nil, a.err
			}
			v, err := cmtyCall(ec, c, "POST", path, nil, b)
			if err != nil {
				return nil, err
			}
			return restItems(v), nil
		}}
	}
	smart := v1("/smartscraper", func(a *cmtyArgs) (map[string]interface{}, error) {
		b := map[string]interface{}{"website_url": a.get("websiteUrl", ""), "user_prompt": a.get("userPrompt", ""), "render_heavy_js": a.bool("renderHeavyJs", false)}
		return b, sgScrollPagesSchema(a, b)
	})
	v1Scrape := v1("/scrape", func(a *cmtyArgs) (map[string]interface{}, error) {
		return map[string]interface{}{"website_url": a.get("websiteUrl", ""), "render_heavy_js": a.bool("renderHeavyJs", false)}, nil
	})
	registerREST("n8n-nodes-scrapegraphai.scrapegraphAi", &restService{
		defResource: "smartscraper",
		defOps: map[string]string{"smartscraper": "scrape", "searchscraper": "search", "smartcrawler": "crawl", "markdownify": "convert", "scrape": "scrape",
			"agenticscraper": "execute", "extract": "extract", "search": "search", "crawl": "start", "monitor": "create", "history": "get", "credits": "get"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "scrapegraphAIApi", nil)
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("scrapegraphAi", "https://api.scrapegraphai.com/v1"), headers: map[string]string{"SGAI-APIKEY": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			// API v1 (package 0.1.x)
			"smartscraper/scrape": {run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				if _, r := ec.raw("resource"); !r && a.has("url") && !a.has("websiteUrl") && !a.has("userPrompt") {
					return sgV2Scrape(ec, i, it, c)
				}
				return smart.run(ec, i, it, c)
			}},
			"searchscraper/search": v1("/searchscraper", func(a *cmtyArgs) (map[string]interface{}, error) {
				b := map[string]interface{}{"user_prompt": a.get("userPrompt", ""), "num_results": a.get("numResults", 3.0)}
				return b, sgScrollPagesSchema(a, b)
			}),
			"smartcrawler/crawl": v1("/crawl", func(a *cmtyArgs) (map[string]interface{}, error) {
				b := map[string]interface{}{"url": a.get("url", ""), "prompt": a.get("prompt", ""), "cache_website": a.bool("cacheWebsite", true),
					"depth": a.get("depth", 2.0), "max_pages": a.get("maxPages", 2.0), "same_domain_only": a.bool("sameDomainOnly", true),
					"render_heavy_js": a.bool("renderHeavyJs", false)}
				return b, sgSchema(a, b)
			}),
			"smartcrawler/getStatus": {path: "/crawl/{taskId}"},
			"markdownify/convert": v1("/markdownify", func(a *cmtyArgs) (map[string]interface{}, error) {
				return map[string]interface{}{"website_url": a.get("websiteUrl", ""), "render_heavy_js": a.bool("renderHeavyJs", false)}, nil
			}),
			"agenticscraper/execute": v1("/agentic-scrapper", func(a *cmtyArgs) (map[string]interface{}, error) {
				var steps []interface{}
				for _, s := range asList(asMap(a.get("steps", nil))["step"]) {
					if act := toString(asMap(s)["action"]); strings.TrimSpace(act) != "" {
						steps = append(steps, act)
					}
				}
				if len(steps) == 0 {
					return nil, permanentError{"At least one browser interaction step is required"}
				}
				ai := a.bool("aiExtraction", true)
				b := map[string]interface{}{"url": a.get("url", ""), "steps": steps, "use_session": a.bool("useSession", false), "ai_extraction": ai}
				if ai {
					if p := a.str("userPrompt", ""); p != "" {
						b["user_prompt"] = p
					}
					return b, sgSchema(a, b)
				}
				return b, nil
			}),
			"scrape/scrape": {run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
				if a := (&cmtyArgs{ec: ec, i: i}); a.has("websiteUrl") {
					return v1Scrape.run(ec, i, it, c)
				}
				return sgV2Scrape(ec, i, it, c)
			}},
			// API v2 (package 1.x)
			"extract/extract": sgV2(func(a *cmtyArgs) (string, string, map[string]interface{}, error) {
				src := a.str("source", "url")
				b := map[string]interface{}{"prompt": a.get("prompt", "")}
				switch src {
				case "url":
					b["url"] = a.get("url", "")
					sgFetchConfig(a, b)
				case "html":
					b["html"] = a.get("html", "")
				default:
					b["markdown"] = a.get("markdown", "")
				}
				if m := a.str("mode", "normal"); (src == "url" || src == "html") && m != "normal" {
					b["mode"] = m
				}
				if a.bool("useSchema", false) {
					s, err := sgJSONObject(a.get("schema", ""), "Schema")
					if err != nil {
						return "", "", nil, err
					}
					b["schema"] = s
				}
				return "POST", "/extract", b, nil
			}, true),
			"search/search": sgV2(func(a *cmtyArgs) (string, string, map[string]interface{}, error) {
				b := map[string]interface{}{"query": a.get("query", ""), "numResults": a.get("numResults", 3.0)}
				if f := a.str("format", "markdown"); f != "markdown" {
					b["format"] = f
				}
				if t := a.str("timeRange", ""); t != "" {
					b["timeRange"] = t
				}
				if l := a.str("locationGeoCode", ""); l != "" {
					b["locationGeoCode"] = l
				}
				if a.bool("useRollup", false) {
					b["prompt"] = a.get("prompt", "")
					if a.bool("useSchema", false) {
						s, err := sgJSONObject(a.get("schema", ""), "Schema")
						if err != nil {
							return "", "", nil, err
						}
						b["schema"] = s
					}
				}
				sgFetchConfig(a, b)
				return "POST", "/search", b, nil
			}, true),
			"crawl/start": sgV2(func(a *cmtyArgs) (string, string, map[string]interface{}, error) {
				f, err := sgFormats(a)
				if err != nil {
					return "", "", nil, err
				}
				b := map[string]interface{}{"url": a.get("url", ""), "formats": f, "maxPages": a.get("maxPages", 50.0), "maxDepth": a.get("maxDepth", 2.0),
					"maxLinksPerPage": a.get("maxLinksPerPage", 10.0)}
				if a.bool("allowExternal", false) {
					b["allowExternal"] = true
				}
				for _, k := range []string{"includePatterns", "excludePatterns", "contentTypes"} {
					if l := asList(a.get(k, nil)); len(l) > 0 {
						b[k] = l
					}
				}
				sgFetchConfig(a, b)
				return "POST", "/crawl", b, nil
			}, false),
			"crawl/getStatus": sgV2Get("crawlId", "/crawl/", "", func(v interface{}) interface{} {
				m := asMap(v)
				var pages []interface{}
				for _, p := range asList(m["pages"]) {
					pm := asMap(p)
					pages = append(pages, map[string]interface{}{"url": pm["url"], "status": pm["status"], "depth": pm["depth"], "title": pm["title"],
						"contentType": pm["contentType"], "links": pm["links"], "scrapeRefId": pm["scrapeRefId"]})
				}
				if pages == nil {
					pages = []interface{}{}
				}
				return map[string]interface{}{"id": m["id"], "status": m["status"], "total": m["total"], "finished": m["finished"], "pages": pages}
			}),
			"crawl/stop":   sgV2Action("crawlId", "id", "POST", "/crawl/", "/stop", "stopped"),
			"crawl/resume": sgV2Action("crawlId", "id", "POST", "/crawl/", "/resume", "resumed"),
			"crawl/delete": sgV2Action("crawlId", "id", "DELETE", "/crawl/", "", "deleted"),
			"monitor/create": sgV2(func(a *cmtyArgs) (string, string, map[string]interface{}, error) {
				f, err := sgFormats(a)
				if err != nil {
					return "", "", nil, err
				}
				b := map[string]interface{}{"url": a.get("url", ""), "name": a.get("name", ""), "interval": a.get("interval", "*/30 * * * *"), "formats": f}
				if w := a.str("webhookUrl", ""); w != "" {
					b["webhookUrl"] = w
				}
				sgFetchConfig(a, b)
				return "POST", "/monitor", b, nil
			}, false),
			"monitor/list": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				simplify := a.bool("simplify", true)
				v, err := cmtyCall(ec, sgV2Client(ec, c), "GET", "/monitor", nil, nil)
				if err != nil {
					return nil, err
				}
				list, ok := v.([]interface{})
				if !ok {
					list = asList(asMap(v)["monitors"])
				}
				out := make([]interface{}, 0, len(list))
				for _, x := range list {
					m := asMap(x)
					if simplify {
						cfg := asMap(m["config"])
						x = map[string]interface{}{"cronId": m["cronId"], "status": m["status"], "interval": m["interval"], "name": cfg["name"], "url": cfg["url"],
							"formats": cfg["formats"], "webhookUrl": cfg["webhookUrl"], "fetchConfig": cfg["fetchConfig"], "createdAt": m["createdAt"], "updatedAt": m["updatedAt"]}
					}
					out = append(out, x)
				}
				return restItems(out), nil
			}},
			"monitor/get": sgV2Get("cronId", "/monitor/", "", nil),
			"monitor/update": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				id := rlValue(a.get("cronId", ""))
				u := fieldsOf(ec, i, "updateFields")
				b := map[string]interface{}{}
				if v := u["interval"]; !restEmpty(v) {
					b["interval"] = v
				}
				if v, ok := u["webhookUrl"]; ok {
					b["webhookUrl"] = v
				}
				if v := u["formats"]; !restEmpty(v) {
					f, err := cmtyJSON(v, "Formats")
					if err != nil {
						return nil, err
					}
					l, ok := f.([]interface{})
					if !ok {
						return nil, permanentError{"The 'Formats' parameter must be a JSON array"}
					}
					if len(l) > 0 {
						b["formats"] = l
					}
				}
				if v := u["fetchConfig"]; !restEmpty(v) {
					fc, err := sgJSONObject(v, "Fetch Config")
					if err != nil {
						return nil, err
					}
					if len(fc) > 0 {
						b["fetchConfig"] = fc
					}
				}
				if len(b) == 0 {
					return nil, permanentError{"Add at least one field to 'Update Fields' before running this operation"}
				}
				v, err := cmtyCall(ec, sgV2Client(ec, c), "PATCH", "/monitor/"+url.PathEscape(id), nil, b)
				if err != nil {
					return nil, err
				}
				return restItems(v), nil
			}},
			"monitor/pause":  sgV2Action("cronId", "cronId", "POST", "/monitor/", "/pause", "paused"),
			"monitor/resume": sgV2Action("cronId", "cronId", "POST", "/monitor/", "/resume", "resumed"),
			"monitor/delete": sgV2Action("cronId", "cronId", "DELETE", "/monitor/", "", "deleted"),
			"monitor/activity": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				id, simplify := rlValue(a.get("cronId", "")), a.bool("simplify", true)
				v, err := cmtyCall(ec, sgV2Client(ec, c), "GET", "/monitor/"+url.PathEscape(id)+"/activity", nil, nil)
				if err != nil {
					return nil, err
				}
				out := []interface{}{}
				for _, t := range asList(asMap(v)["ticks"]) {
					if simplify {
						m := asMap(t)
						t = map[string]interface{}{"id": m["id"], "status": m["status"], "createdAt": m["createdAt"], "elapsedMs": m["elapsedMs"], "changed": m["changed"]}
					}
					out = append(out, t)
				}
				return restItems(out), nil
			}},
			"credits/get": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				v, err := cmtyCall(ec, sgV2Client(ec, c), "GET", "/credits", nil, nil)
				if err != nil {
					return nil, err
				}
				return restItems(v), nil
			}},
			"history/get": sgV2Get("entryId", "/history/", "", func(v interface{}) interface{} {
				m := asMap(v)
				return map[string]interface{}{"id": m["id"], "service": m["service"], "status": m["status"], "elapsedMs": m["elapsedMs"], "createdAt": m["createdAt"],
					"requestParentId": m["requestParentId"], "params": m["params"], "result": m["result"]}
			}),
			"history/list": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				q := url.Values{}
				if s := a.str("service", ""); s != "" {
					q.Set("service", s)
				}
				if p := a.num("page", 1); p > 1 {
					q.Set("page", strconv.Itoa(int(p)))
				}
				if l := a.num("limit", 50); l != 50 {
					q.Set("limit", strconv.Itoa(int(l)))
				}
				simplify := a.bool("simplify", true)
				if a.err != nil {
					return nil, a.err
				}
				v, err := cmtyCall(ec, sgV2Client(ec, c), "GET", "/history", q, nil)
				if err != nil {
					return nil, err
				}
				out := []interface{}{}
				for _, e := range asList(asMap(v)["data"]) {
					if simplify {
						m := asMap(e)
						e = map[string]interface{}{"id": m["id"], "service": m["service"], "status": m["status"], "elapsedMs": m["elapsedMs"], "createdAt": m["createdAt"],
							"requestParentId": m["requestParentId"], "url": asMap(m["params"])["url"]}
					}
					out = append(out, e)
				}
				return restItems(out), nil
			}},
		},
	})
}

// sgSchema adds output_schema when useOutputSchema is on (API v1).
func sgSchema(a *cmtyArgs, b map[string]interface{}) error {
	if !a.bool("useOutputSchema", false) {
		return nil
	}
	s, err := cmtyJSON(a.get("outputSchema", ""), "Output Schema")
	if err != nil {
		return permanentError{"Invalid JSON in Output Schema"}
	}
	b["output_schema"] = s
	return nil
}

func sgScrollPagesSchema(a *cmtyArgs, b map[string]interface{}) error {
	if a.bool("enableScrolling", false) {
		if n := a.num("numberOfScrolls", 0); n > 0 {
			b["number_of_scrolls"] = n
		}
	}
	if a.bool("enablePagination", false) {
		if n := a.num("totalPages", 0); n > 1 {
			b["total_pages"] = n
		}
	}
	return sgSchema(a, b)
}

// sgV2Client points a client at API v2.
func sgV2Client(ec *execCtx, c *restClient) *restClient {
	return &restClient{base: ec.apiBase("scrapegraphAiV2", "https://v2-api.scrapegraphai.com/api"), headers: c.headers}
}

// sgV2 runs a v2 call built by fn; output applies the outputMode setting.
func sgV2(fn func(a *cmtyArgs) (string, string, map[string]interface{}, error), output bool) *restOp {
	return &restOp{run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		a := &cmtyArgs{ec: ec, i: i}
		method, path, b, err := fn(a)
		if err != nil {
			return nil, err
		}
		if a.err != nil {
			return nil, a.err
		}
		v, err := cmtyCall(ec, sgV2Client(ec, c), method, path, nil, b)
		if err != nil {
			return nil, err
		}
		if output {
			v = sgOutput(a.str("outputMode", "simplified"), a.str("outputFields", ""), v)
		}
		return restItems(v), nil
	}}
}

func sgV2Scrape(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return sgV2(func(a *cmtyArgs) (string, string, map[string]interface{}, error) {
		f, err := sgFormats(a)
		if err != nil {
			return "", "", nil, err
		}
		b := map[string]interface{}{"url": a.get("url", ""), "formats": f}
		if ct := a.str("contentType", ""); ct != "" {
			b["contentType"] = ct
		}
		sgFetchConfig(a, b)
		return "POST", "/scrape", b, nil
	}, true).run(ec, i, it, c)
}

// sgV2Get fetches prefix+<id>; simplify (when set and the node's simplify
// parameter is on) shapes the result.
func sgV2Get(idParam, prefix, suffix string, simplify func(interface{}) interface{}) *restOp {
	return &restOp{run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		a := &cmtyArgs{ec: ec, i: i}
		id := rlValue(a.get(idParam, ""))
		v, err := cmtyCall(ec, sgV2Client(ec, c), "GET", prefix+url.PathEscape(id)+suffix, nil, nil)
		if err != nil {
			return nil, err
		}
		if simplify != nil && a.bool("simplify", true) {
			v = simplify(v)
		}
		return restItems(v), nil
	}}
}

// sgV2Action calls an id endpoint and outputs {<idKey>: id, <flag>: true}.
func sgV2Action(idParam, idKey, method, prefix, suffix, flag string) *restOp {
	return &restOp{run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		id := rlValue((&cmtyArgs{ec: ec, i: i}).get(idParam, ""))
		if _, err := cmtyCall(ec, sgV2Client(ec, c), method, prefix+url.PathEscape(id)+suffix, nil, nil); err != nil {
			return nil, err
		}
		return restItems(map[string]interface{}{idKey: id, flag: true}), nil
	}}
}

var sgSimplifyKeys = []string{"id", "json", "results", "raw", "data", "usage", "metadata", "remaining", "used", "plan", "jobs", "cronId", "status"}

// sgOutput applies the v2 node's output mode.
func sgOutput(mode, fields string, v interface{}) interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return map[string]interface{}{"value": v}
	}
	out := map[string]interface{}{}
	switch mode {
	case "raw":
		return m
	case "selected":
		for _, f := range strings.Split(fields, ",") {
			if f = strings.TrimSpace(f); f != "" {
				if x, ok := m[f]; ok {
					out[f] = x
				}
			}
		}
		return out
	}
	for _, k := range sgSimplifyKeys {
		if x, ok := m[k]; ok {
			out[k] = x
		}
	}
	if len(out) == 0 {
		return m
	}
	return out
}

func sgJSONObject(v interface{}, what string) (map[string]interface{}, error) {
	x, err := cmtyJSON(v, what)
	if err != nil {
		return nil, err
	}
	if x == nil {
		return map[string]interface{}{}, nil
	}
	m, ok := x.(map[string]interface{})
	if !ok {
		return nil, permanentError{"The '" + what + "' parameter must be a JSON object"}
	}
	return m, nil
}

// sgFormats builds the v2 formats list (markdown when none is set).
func sgFormats(a *cmtyArgs) ([]interface{}, error) {
	entries := asList(asMap(a.get("formats", nil))["format"])
	if len(entries) == 0 {
		return []interface{}{map[string]interface{}{"type": "markdown"}}, nil
	}
	out := make([]interface{}, 0, len(entries))
	for _, e := range entries {
		m := asMap(e)
		t := toString(m["type"])
		f := map[string]interface{}{"type": t}
		switch t {
		case "markdown", "html":
			if mode := toString(m["mode"]); mode != "" && mode != "normal" {
				f["mode"] = mode
			}
		case "json":
			if p := toString(m["prompt"]); p != "" {
				f["prompt"] = p
			}
			if !restEmpty(m["schema"]) {
				s, err := sgJSONObject(m["schema"], "Schema")
				if err != nil {
					return nil, err
				}
				if len(s) > 0 {
					f["schema"] = s
				}
			}
		case "screenshot":
			if toBool(m["fullPage"]) {
				f["fullPage"] = true
			}
			for k, def := range map[string]float64{"width": 1280, "height": 720, "quality": 80} {
				if n, ok := m[k].(float64); ok && n != def {
					f[k] = n
				}
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// sgFetchConfig adds the fetchConfig collection without its defaults.
func sgFetchConfig(a *cmtyArgs, b map[string]interface{}) {
	raw := asMap(a.get("fetchConfig", nil))
	fc := map[string]interface{}{}
	if m := toString(raw["mode"]); m != "" && m != "auto" {
		fc["mode"] = m
	}
	if toBool(raw["stealth"]) {
		fc["stealth"] = true
	}
	if n, ok := raw["scrolls"].(float64); ok && n > 0 {
		fc["scrolls"] = n
	}
	if n, ok := raw["wait"].(float64); ok && n > 0 {
		fc["wait"] = n
	}
	if n, ok := raw["timeout"].(float64); ok && n != 30000 {
		fc["timeout"] = n
	}
	if c := toString(raw["country"]); c != "" {
		fc["country"] = c
	}
	for _, k := range []string{"headers", "cookies"} {
		if !restEmpty(raw[k]) {
			if m, err := sgJSONObject(raw[k], k); err == nil && len(m) > 0 {
				fc[k] = m
			} else if err != nil && a.err == nil {
				a.err = err
			}
		}
	}
	if len(fc) > 0 {
		b["fetchConfig"] = fc
	}
}
