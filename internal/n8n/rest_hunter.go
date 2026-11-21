package n8n

import (
	"net/url"
	"strconv"
	"strings"
)

// Hunter (n8n-nodes-base.hunter), API v2. The node has no resource.

func init() {
	registerREST("hunter", &restService{
		defOps: map[string]string{"": "domainSearch"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("hunterApi")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("hunter", "https://api.hunter.io/v2"), query: url.Values{"api_key": {credString(c, "apiKey")}}}, nil
		},
		ops: map[string]*restOp{
			"/domainSearch":  {run: hunterDomainSearch},
			"/emailFinder":   {path: "/email-finder", query: "firstname:first_name lastname:last_name domain", out: "data"},
			"/emailVerifier": {path: "/email-verifier", query: "email", out: "data"},
		},
	})
}

// hunterDomainSearch returns the domain's data (or only its emails, the
// default), merging the emails of every page when returnAll is set.
func hunterDomainSearch(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	req := &restRequest{method: "GET", path: "/domain-search", query: url.Values{}}
	q := map[string]interface{}{}
	if err := ec.restFields(i, "domain @filters type seniority department", q); err != nil {
		return nil, err
	}
	for k, v := range q {
		req.query.Set(k, strings.Join(queryValues(v), ","))
	}
	onlyEmails, _ := ec.boolean(i, "onlyEmails", true)
	all, _ := ec.boolean(i, "returnAll", false)
	var data map[string]interface{}
	if !all {
		n, _ := ec.num(i, "limit", 100)
		req.query.Set("limit", strconv.Itoa(int(n)))
		v, err := c.do(ec, req)
		if err != nil {
			return nil, err
		}
		data = asMap(asMap(v)["data"])
	} else {
		req.query.Set("limit", "100")
		for off := 0; off < 100000; off += 100 {
			req.query.Set("offset", strconv.Itoa(off))
			v, err := c.do(ec, req)
			if err != nil {
				return nil, err
			}
			page := asMap(asMap(v)["data"])
			if data == nil {
				data = page
			} else {
				data["emails"] = append(asList(data["emails"]), asList(page["emails"])...)
			}
			meta := asMap(asMap(v)["meta"])
			total, ok := toFloat(meta["results"])
			if !ok || float64(off+100) >= total {
				break
			}
		}
	}
	if onlyEmails {
		return restItems(asList(data["emails"])), nil
	}
	return restItems(data), nil
}
