package n8n

import "strings"

// Snapshot Site (n8n-nodes-snapshot-site): screenshots, AI page analysis
// and visual comparison.

func init() {
	registerREST("n8n-nodes-snapshot-site.snapshotSite", &restService{
		defOps: map[string]string{"": "screenshot"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("snapshotSiteApi")
			if err != nil {
				return nil, err
			}
			base := ec.apiBase("snapshotSite", firstNonEmpty(strings.TrimRight(credString(c, "baseUrl"), "/"), "https://api.prod.ss.snapshot-site.com"))
			return &restClient{base: base, headers: map[string]string{"X-SnapshotSiteAPI-Key": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"/screenshot": {method: "POST", path: "/api/v2/screenshot", body: "url @additionalFields *"},
			"/analyze":    {method: "POST", path: "/api/v3/analyze", body: "url @additionalFields *"},
			"/compare": {method: "POST", path: "/api/v3/compare", build: func(ec *execCtx, i int, r *restRequest) error {
				side := func(urlParam, opts string) map[string]interface{} {
					m := map[string]interface{}{}
					for k, v := range fieldsOf(ec, i, opts) {
						if v != nil && v != "" {
							m[k] = v
						}
					}
					if u, _ := ec.str(i, urlParam, ""); u != "" {
						m["url"] = u
					}
					return m
				}
				th, _ := ec.num(i, "threshold", 0.1)
				r.body = map[string]interface{}{"before": side("beforeUrl", "beforeOptions"), "after": side("afterUrl", "afterOptions"), "threshold": th}
				return nil
			}},
		},
	})
}
