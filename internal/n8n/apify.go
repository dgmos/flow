package n8n

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Apify (community node @apify/n8n-nodes-apify).

type apifyAPI struct {
	ec   *execCtx
	base string
	auth map[string]string
}

func (ec *execCtx) apify(i int) (*apifyAPI, error) {
	credType, _ := ec.str(i, "authentication", "apifyApi")
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	tok := credString(c, "apiToken", "apiKey", "token")
	if credType == "apifyOAuth2Api" || tok == "" {
		tok = accessToken(c)
	}
	return &apifyAPI{ec: ec, base: ec.apiBase("apify", "https://api.apify.com"),
		auth: map[string]string{"Authorization": "Bearer " + tok, "x-apify-integration-platform": "n8n"}}, nil
}

func (a *apifyAPI) call(method, path string, qs url.Values, body interface{}) (map[string]interface{}, []byte, error) {
	u := a.base + path
	if len(qs) > 0 {
		u += "?" + qs.Encode()
	}
	return a.ec.apiCall(method, u, a.auth, body, "")
}

// apifyID turns "user/actor" into the API's "user~actor" form.
func apifyID(id string) string { return strings.ReplaceAll(strings.TrimSpace(id), "/", "~") }

var apifyTerminal = map[string]bool{"SUCCEEDED": true, "FAILED": true, "TIMED-OUT": true, "ABORTED": true}

// wait polls a run until it reaches a terminal status.
func (a *apifyAPI) wait(run map[string]interface{}) (map[string]interface{}, error) {
	id := toString(run["id"])
	deadline := time.Now().Add(24 * time.Hour)
	if s := toFloatDefault(asMap(run["options"])["timeoutSecs"], 0); s > 0 {
		deadline = time.Now().Add(time.Duration(s)*time.Second + 5*time.Minute)
	}
	delay := time.Second
	for !apifyTerminal[toString(run["status"])] {
		if time.Now().After(deadline) {
			return nil, permanentError{fmt.Sprintf("Apify: timed out waiting for run %s (last status %s)", id, run["status"])}
		}
		select {
		case <-a.ec.ctx.Done():
			return nil, a.ec.ctx.Err()
		case <-time.After(delay):
		}
		if delay < 5*time.Second {
			delay += time.Second
		}
		out, _, err := a.call("GET", "/v2/actor-runs/"+url.PathEscape(id), nil, nil)
		if err != nil {
			return nil, err
		}
		run = asMap(out["data"])
	}
	return run, nil
}

func (a *apifyAPI) datasetItems(id string, qs url.Values) ([]Item, error) {
	if qs == nil {
		qs = url.Values{}
	}
	qs.Set("format", "json")
	_, raw, err := a.call("GET", "/v2/datasets/"+url.PathEscape(id)+"/items", qs, nil)
	if err != nil {
		return nil, err
	}
	var list []interface{}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, permanentError{"Apify: unexpected dataset response"}
	}
	var out []Item
	for _, x := range list {
		if m, ok := x.(map[string]interface{}); ok {
			out = append(out, Item{JSON: m})
		} else {
			out = append(out, Item{JSON: map[string]interface{}{"value": x}})
		}
	}
	return out, nil
}

// apifyInput reads the actor input (JSON text, object or schema fields).
func apifyInput(ec *execCtx, i int) (interface{}, error) {
	if mode, _ := ec.str(i, "actorInputMode", "json"); mode == "schema" {
		v, err := ec.param(i, "actorInput.value", map[string]interface{}{})
		if err != nil {
			return nil, err
		}
		out := map[string]interface{}{}
		for k, x := range asMap(v) {
			if x != nil {
				out[k] = x
			}
		}
		return out, nil
	}
	v, err := ec.param(i, "customBody", "{}")
	if err != nil {
		return nil, err
	}
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return map[string]interface{}{}, nil
		}
		var parsed interface{}
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return nil, permanentError{"Apify: could not parse custom body: " + truncate(s, 200)}
		}
		return parsed, nil
	}
	if v == nil {
		return map[string]interface{}{}, nil
	}
	return v, nil
}

func apifyRunQuery(ec *execCtx, i int) url.Values {
	qs := url.Values{"waitForFinish": {"0"}}
	for _, k := range []string{"timeout", "memory", "maxTotalChargeUsd"} {
		if v, _ := ec.param(i, k, nil); v != nil && v != "" {
			if n := toFloatDefault(v, 0); n > 0 || k == "timeout" {
				qs.Set(k, strconv.FormatFloat(n, 'f', -1, 64))
			}
		}
	}
	if b, _ := ec.str(i, "build", ""); b != "" {
		qs.Set("build", b)
	}
	return qs
}

func apifyNode(ec *execCtx) ([][]Item, error) {
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		a, err := ec.apify(i)
		if err != nil {
			return nil, err
		}
		r, o := ec.resourceOpBy(i, "Actors", map[string]string{"Actors": "Run actor", "Actor tasks": "Run task", "Actor runs": "Get user runs list",
			"Datasets": "Get items", "Key-Value Stores": "Get Key-Value Store Record"})
		if alias, ok := map[string]string{"actor": "Actors", "dataset": "Datasets", "task": "Actor tasks"}[r]; ok {
			r = alias
		}
		if alias, ok := map[string]string{"run": "Run actor", "getItems": "Get items"}[o]; ok {
			o = alias
		}
		switch r + "/" + o {
		case "Actors/Run actor", "Actors/Run actor and get dataset", "Actor tasks/Run task", "Actor tasks/Run task and get dataset":
			task := r == "Actor tasks"
			idParam := "actorId"
			if task {
				idParam = "actorTaskId"
			}
			idv, err := ec.param(i, idParam, "")
			if err != nil {
				return nil, err
			}
			id := apifyID(rlValue(idv))
			if id == "" {
				return nil, permanentError{"Apify: actor ID is required"}
			}
			input, err := apifyInput(ec, i)
			if err != nil {
				return nil, err
			}
			path := "/v2/acts/" + url.PathEscape(id) + "/runs"
			if task {
				path = "/v2/actor-tasks/" + url.PathEscape(id) + "/runs"
			}
			out, _, err := a.call("POST", path, apifyRunQuery(ec, i), input)
			if err != nil {
				return nil, err
			}
			run := asMap(out["data"])
			if run == nil || toString(run["id"]) == "" {
				return nil, permanentError{"Apify: run ID not found after starting the run"}
			}
			getDataset := strings.HasSuffix(o, "and get dataset")
			if wf, _ := ec.boolean(i, "waitForFinish", true); wf || getDataset {
				if run, err = a.wait(run); err != nil {
					return nil, err
				}
			}
			if !getDataset {
				return one(run), nil
			}
			if toString(run["status"]) != "SUCCEEDED" {
				return nil, permanentError{fmt.Sprintf("Apify: run %s did not finish with status SUCCEEDED. Run status: %s", run["id"], run["status"])}
			}
			if toString(run["defaultDatasetId"]) == "" {
				return nil, permanentError{fmt.Sprintf("Apify: run %s did not create a dataset", run["id"])}
			}
			return a.datasetItems(toString(run["defaultDatasetId"]), nil)
		case "Actors/Scrape single URL":
			u, err := ec.str(i, "url", "")
			if err != nil {
				return nil, err
			}
			crawler, _ := ec.str(i, "crawlerType", "cheerio")
			format, _ := ec.str(i, "outputFormat", "markdown")
			out, _, err := a.call("POST", "/v2/acts/aYG0l9s7dbB7j3gbS/runs", url.Values{"waitForFinish": {"0"}}, map[string]interface{}{
				"startUrls": []interface{}{map[string]interface{}{"url": u}}, "crawlerType": crawler, "maxCrawlDepth": 0, "maxCrawlPages": 1, "maxResults": 1,
				"proxyConfiguration": map[string]interface{}{"useApifyProxy": true}, "removeCookieWarnings": true, "saveHtml": format == "html", "saveMarkdown": format == "markdown"})
			if err != nil {
				return nil, err
			}
			run, err := a.wait(asMap(out["data"]))
			if err != nil {
				return nil, err
			}
			items, err := a.datasetItems(toString(run["defaultDatasetId"]), nil)
			if err != nil {
				return nil, err
			}
			if len(items) == 0 {
				return nil, permanentError{"Apify: no content was scraped from the provided URL"}
			}
			meta := cloneJSON(items[0].JSON)
			for _, k := range []string{"text", "html", "markdown"} {
				delete(meta, k)
			}
			return one(map[string]interface{}{format: items[0].JSON[format], "metadata": meta}), nil
		case "Actors/Get last run":
			idv, _ := ec.param(i, "userActorId", "")
			status, _ := ec.str(i, "status", "")
			qs := url.Values{}
			if status != "" {
				qs.Set("status", status)
			}
			out, _, err := a.call("GET", "/v2/acts/"+url.PathEscape(apifyID(rlValue(idv)))+"/runs/last", qs, nil)
			if err != nil {
				return nil, err
			}
			return one(asMap(out["data"])), nil
		case "Actors/Get list of actors", "Actor tasks/Get list of tasks", "Actor runs/Get user runs list", "Actor runs/Get runs":
			path := map[string]string{"Actors": "/v2/acts", "Actor tasks": "/v2/actor-tasks", "Actor runs": "/v2/actor-runs"}[r]
			if o == "Get runs" {
				idv, _ := ec.param(i, "actorId", "")
				path = "/v2/acts/" + url.PathEscape(apifyID(rlValue(idv))) + "/runs"
			}
			qs := url.Values{}
			for _, k := range []string{"offset", "limit", "desc", "my", "status"} {
				if v, _ := ec.param(i, k, nil); v != nil && v != "" {
					qs.Set(k, toString(v))
				}
			}
			out, _, err := a.call("GET", path, qs, nil)
			if err != nil {
				return nil, err
			}
			return listItems(asMap(out["data"])["items"]), nil
		case "Actor runs/Get run":
			idv, _ := ec.param(i, "runId", "")
			out, _, err := a.call("GET", "/v2/actor-runs/"+url.PathEscape(rlValue(idv)), nil, nil)
			if err != nil {
				return nil, err
			}
			return one(asMap(out["data"])), nil
		case "Actor runs/Abort run":
			idv, _ := ec.param(i, "runId", "")
			out, _, err := a.call("POST", "/v2/actor-runs/"+url.PathEscape(rlValue(idv))+"/abort", nil, nil)
			if err != nil {
				return nil, err
			}
			return one(asMap(out["data"])), nil
		case "Datasets/Get items":
			idv, err := ec.param(i, "datasetId", "")
			if err != nil {
				return nil, err
			}
			id := rlValue(idv)
			if id == "" {
				return nil, permanentError{"Apify: dataset ID is required"}
			}
			qs := url.Values{}
			off, _ := ec.num(i, "offset", 0)
			lim, _ := ec.num(i, "limit", 50)
			qs.Set("offset", strconv.Itoa(int(off)))
			qs.Set("limit", strconv.Itoa(int(lim)))
			for _, k := range []string{"fields", "omit"} {
				if s, _ := ec.str(i, "options."+k, ""); s != "" {
					qs.Set(k, strings.Join(splitList(s), ","))
				}
			}
			return a.datasetItems(id, qs)
		case "Key-Value Stores/Get Key-Value Store Record":
			sv, _ := ec.param(i, "storeId", "")
			kv, _ := ec.param(i, "recordKey", "")
			store, key := rlValue(sv), rlValue(kv)
			_, resp, err := a.ec.apiCallRaw("GET", a.base+"/v2/key-value-stores/"+url.PathEscape(store)+"/records/"+url.PathEscape(key), a.auth)
			if err != nil {
				return nil, err
			}
			res := map[string]interface{}{"storeId": store, "recordKey": key, "contentType": resp.contentType}
			ct := resp.contentType
			switch {
			case strings.HasPrefix(ct, "application/json"):
				var v interface{}
				if json.Unmarshal(resp.body, &v) == nil {
					res["data"] = v
				} else {
					res["data"] = string(resp.body)
				}
			case strings.HasPrefix(ct, "text/") || ct == "":
				res["data"] = string(resp.body)
			default:
				return []Item{{JSON: res, Binary: map[string]interface{}{"data": binaryData(resp.body, ct, key)}}}, nil
			}
			return one(res), nil
		}
		return nil, unsupportedOp("Apify", r, o)
	})
}
