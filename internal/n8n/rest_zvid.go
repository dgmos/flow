package n8n

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Zvid (community node @zvid/n8n-nodes-zvid.zvid): JSON-to-video renders,
// templates, stock media and authoring helpers.

func init() {
	registerREST("@zvid/n8n-nodes-zvid.zvid", &restService{
		defResource: "render",
		defOps: map[string]string{"render": "create", "template": "render", "stockMedia": "search", "credit": "getBalance",
			"authoring": "getSchema", "creativeLibrary": "search"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "zvidApi", nil)
			if err != nil {
				return nil, err
			}
			base := strings.TrimRight(firstNonEmpty(credString(c, "baseUrl"), "https://api.zvid.io"), "/")
			return &restClient{base: ec.apiBase("zvid", base), headers: map[string]string{"X-Api-Key": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"render/create":     {run: zvidRender(false)},
			"render/createBulk": {run: zvidRender(true)},
			"render/get": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				id, err := ec.str(i, "jobId", "")
				if err != nil {
					return nil, err
				}
				v, err := cmtyCall(ec, c, "GET", "/api/jobs/"+url.PathEscape(id), nil, nil)
				if err != nil {
					return nil, err
				}
				return zvidWait(ec, i, c, v)
			}},
			"render/getAll": {path: "/api/jobs", out: "jobs", build: func(ec *execCtx, i int, r *restRequest) error {
				a := &cmtyArgs{ec: ec, i: i}
				r.query.Set("limit", toString(a.get("limit", 50.0)))
				r.query.Set("page", toString(a.get("page", 1.0)))
				if t := a.str("filterType", "all"); t != "all" {
					r.query.Set("type", t)
				}
				return a.err
			}},
			"render/validate": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				b, err := zvidRenderBody(ec, i)
				if err != nil {
					return nil, err
				}
				res, err := cmtyRaw(ec, c, "POST", "/api/render/validate/api-key", nil, []byte(mustJSON(b)), "application/json")
				body := asMap(decodeJSONBody(res.body))
				if err != nil {
					if res.status < 400 || res.status >= 500 {
						return nil, err
					}
					errs := body["details"]
					if errs == nil {
						errs = []interface{}{}
					}
					return restItems(map[string]interface{}{"valid": false, "error": firstNonEmpty(toString(body["error"]), "Validation failed"),
						"message": firstNonEmpty(toString(body["message"]), "The payload was rejected by the Zvid API"), "errors": errs, "planLimits": body["planLimits"]}), nil
				}
				return restItems(cmtyMerge(map[string]interface{}{"valid": true}, body)), nil
			}},
			"template/create": {method: "POST", path: "/api/templates", build: func(ec *execCtx, i int, r *restRequest) error {
				a := &cmtyArgs{ec: ec, i: i}
				name := strings.TrimSpace(a.str("templateName", ""))
				if name == "" {
					return permanentError{"Template Name is required"}
				}
				r.body["name"] = name
				if d := strings.TrimSpace(a.str("templateDescription", "")); d != "" {
					r.body["description"] = d
				}
				p, err := zvidObject(a.get("templateProjectJson", "{}"), "Template Project JSON")
				if err != nil {
					return err
				}
				r.body["payload"] = p
				return a.err
			}},
			"template/delete":    {method: "DELETE", path: "/api/templates/{templateId}"},
			"template/duplicate": {method: "POST", path: "/api/templates/{templateId}/duplicate"},
			"template/get":       {path: "/api/templates/{templateId}"},
			"template/getAll": {path: "/api/templates", out: "templates", build: func(ec *execCtx, i int, r *restRequest) error {
				v, err := ec.param(i, "limit", 50.0)
				r.query.Set("limit", toString(v))
				return err
			}},
			"template/preview": {method: "POST", path: "/api/templates/{templateId}/preview", build: func(ec *execCtx, i int, r *restRequest) error {
				v, err := zvidObject(mustParam(ec, "variables", "{}"), "Variables")
				if err != nil {
					return err
				}
				if len(asMap(v)) > 0 {
					r.body["variables"] = v
				}
				return nil
			}},
			"template/render": {run: zvidRender(false)},
			"template/update": {method: "PUT", path: "/api/templates/{templateId}", build: func(ec *execCtx, i int, r *restRequest) error {
				v, _ := ec.param(i, "templateChanges", "{}")
				ch, err := zvidObject(v, "Template Changes")
				if err != nil {
					return err
				}
				for _, k := range []string{"name", "description", "payload"} {
					if x, ok := asMap(ch)[k]; ok {
						r.body[k] = x
					}
				}
				if len(r.body) == 0 {
					return permanentError{"Template Changes must include name, description, or payload"}
				}
				return nil
			}},
			"stockMedia/listProviders": {path: "/api/stock/providers"},
			"stockMedia/search": {path: "/api/stock/search", build: func(ec *execCtx, i int, r *restRequest) error {
				a := &cmtyArgs{ec: ec, i: i}
				r.query.Set("type", a.str("stockType", "image"))
				r.query.Set("provider", "all")
				r.query.Set("query", a.str("stockQuery", ""))
				r.query.Set("page", toString(a.get("stockPage", 1.0)))
				r.query.Set("perPage", toString(a.get("stockPerPage", 12.0)))
				return a.err
			}},
			"credit/getBalance":        {path: "/api/credits/balance"},
			"authoring/listElements":   {path: "/api/render/elements/api-key"},
			"authoring/getSchema":      {path: "/api/render/schema/api-key", build: zvidDefaultQuery("target", "schemaTarget", "project")},
			"authoring/getElementDocs": {path: "/api/render/elements", build: zvidDefaultPath("elementType", "TEXT", "/api/render/elements/%s/api-key")},
			"authoring/getExample": {path: "/api/render/examples/api-key", build: func(ec *execCtx, i int, r *restRequest) error {
				n, err := ec.str(i, "exampleName", "promo-video")
				if n != "all" {
					r.path = "/api/render/examples/" + url.PathEscape(n) + "/api-key"
				}
				return err
			}},
			"authoring/repair": {method: "POST", path: "/api/render/repair/api-key", build: func(ec *execCtx, i int, r *restRequest) error {
				v, _ := ec.param(i, "projectJson", "{}")
				p, err := zvidObject(v, "Project JSON")
				r.body["payload"] = p
				return err
			}},
			"authoring/planCreativeVideo": {method: "POST", path: "/api/render/creative-plan/api-key", build: zvidCreativePlan},
			"creativeLibrary/search": {path: "/api/library", build: func(ec *execCtx, i int, r *restRequest) error {
				a := &cmtyArgs{ec: ec, i: i}
				r.path = "/api/library/" + url.PathEscape(a.str("libraryKind", "examples"))
				r.query.Set("q", a.str("libraryQuery", ""))
				r.query.Set("limit", toString(a.get("libraryLimit", 12.0)))
				r.query.Set("offset", toString(a.get("libraryOffset", 0.0)))
				return a.err
			}},
			"creativeLibrary/getMetadata": {path: "/api/library", build: zvidLibraryPath("")},
			"creativeLibrary/getContent":  {path: "/api/library", build: zvidLibraryPath("/content")},
		},
	})
}

// zvidLibraryPath applies the default library kind (examples).
func zvidLibraryPath(suffix string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		a := &cmtyArgs{ec: ec, i: i}
		r.path = "/api/library/" + url.PathEscape(a.str("libraryKind", "examples")) + "/" + url.PathEscape(a.str("librarySlug", "")) + suffix
		return a.err
	}
}

func zvidDefaultQuery(key, param, def string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		v, err := ec.str(i, param, def)
		r.query.Set(key, v)
		return err
	}
}

func zvidDefaultPath(param, def, format string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		v, err := ec.str(i, param, def)
		r.path = fmt.Sprintf(format, url.PathEscape(v))
		return err
	}
}

// zvidObject parses a JSON object parameter.
func zvidObject(v interface{}, name string) (interface{}, error) {
	x, err := cmtyJSON(v, name)
	if err != nil {
		return nil, permanentError{fmt.Sprintf("Parameter %q must be valid JSON (an object)", name)}
	}
	if x == nil {
		return nil, nil
	}
	if _, ok := x.(map[string]interface{}); !ok {
		if _, ok := x.([]interface{}); !ok {
			return nil, permanentError{fmt.Sprintf("Parameter %q must be valid JSON (an object)", name)}
		}
	}
	return x, nil
}

// zvidRenderBody builds a render request from a project JSON or a template.
func zvidRenderBody(ec *execCtx, i int) (map[string]interface{}, error) {
	a := &cmtyArgs{ec: ec, i: i}
	b := map[string]interface{}{}
	src := a.str("source", "template")
	if src == "json" || (src != "template" && a.has("projectJson")) {
		p, err := zvidObject(a.get("projectJson", "{}"), "Project JSON")
		if err != nil {
			return nil, err
		}
		b["payload"] = p
	} else {
		b["template"] = rlValue(a.get("templateId", ""))
		v, err := zvidObject(a.get("variables", "{}"), "Variables")
		if err != nil {
			return nil, err
		}
		if len(asMap(v)) > 0 {
			b["variables"] = v
		}
	}
	f := fieldsOf(ec, i, "additionalFields")
	if !restEmpty(f["overrides"]) {
		o, err := zvidObject(f["overrides"], "Overrides")
		if err != nil {
			return nil, err
		}
		b["overrides"] = o
	}
	if !restEmpty(f["webhookUrl"]) {
		b["webhookUrl"] = f["webhookUrl"]
	}
	return b, a.err
}

// zvidRender creates a render (or a bulk render) and optionally waits.
func zvidRender(bulk bool) func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		b, err := zvidRenderBody(ec, i)
		if err != nil {
			return nil, err
		}
		rt, err := ec.str(i, "renderType", "video")
		if err != nil {
			return nil, err
		}
		image := rt == "image"
		path := "/api/render/api-key"
		if bulk {
			path = "/api/render/bulk/api-key"
			if image {
				path = "/api/render/image/bulk/api-key"
			}
			raw, _ := ec.param(i, "items", "[]")
			items, err := cmtyJSON(raw, "Items")
			if l, ok := items.([]interface{}); err != nil || !ok || len(l) == 0 {
				return nil, permanentError{`Items must be a JSON array with one entry per render, e.g. [{"variables": {"name": "Alice"}}]`}
			}
			b["items"] = items
			if n := fieldsOf(ec, i, "additionalFields")["batchName"]; !restEmpty(n) {
				b["name"] = n
			}
		} else if image {
			path = "/api/render/image/api-key"
		}
		v, err := cmtyCall(ec, c, "POST", path, nil, b)
		if err != nil {
			return nil, err
		}
		if bulk {
			return restItems(v), nil
		}
		return zvidWait(ec, i, c, v)
	}
}

// zvidWait polls a render job until it completes when waitForCompletion is on.
func zvidWait(ec *execCtx, i int, c *restClient, v interface{}) ([]Item, error) {
	a := &cmtyArgs{ec: ec, i: i}
	if !a.bool("waitForCompletion", false) {
		return restItems(v), a.err
	}
	opts := fieldsOf(ec, i, "waitOptions")
	interval := 5.0
	if f, ok := toFloat(opts["pollInterval"]); ok {
		interval = f
	}
	maxWait := 600.0
	if f, ok := toFloat(opts["maxWaitTime"]); ok {
		maxWait = f
	}
	deadline := time.Now().Add(time.Duration(maxWait * float64(time.Second)))
	job := asMap(v)
	id := toString(firstNonNil(job["jobId"], job["id"]))
	if id == "" {
		return restItems(v), nil
	}
	state := toString(firstNonNil(job["state"], job["status"]))
	for state != "completed" && state != "failed" {
		if time.Now().After(deadline) {
			return nil, permanentError{fmt.Sprintf("Render %s did not finish within %v seconds (last state: %q). Increase Max Wait Time or use the Zvid Trigger node instead.", id, maxWait, state)}
		}
		if err := cmtySleep(ec, time.Duration(interval*float64(time.Second))); err != nil {
			return nil, err
		}
		x, err := cmtyCall(ec, c, "GET", "/api/jobs/"+url.PathEscape(id), nil, nil)
		if err != nil {
			return nil, err
		}
		job = asMap(x)
		state = toString(firstNonNil(job["state"], job["status"]))
	}
	if state == "failed" {
		return nil, permanentError{fmt.Sprintf("Render %s failed: %s", id, firstNonEmpty(toString(job["failedReason"]), "unknown reason"))}
	}
	return restItems(job), nil
}

func zvidCreativePlan(ec *execCtx, i int, r *restRequest) error {
	a := &cmtyArgs{ec: ec, i: i}
	brief := strings.TrimSpace(a.str("creativeBrief", ""))
	if brief == "" {
		return permanentError{"Creative Brief is required"}
	}
	slugs, err := cmtyJSON(a.get("recentAssetSlugs", "[]"), "Recent Asset Slugs")
	l, ok := slugs.([]interface{})
	if err == nil && ok {
		for _, s := range l {
			if _, isStr := s.(string); !isStr {
				ok = false
			}
		}
	}
	if err != nil || !ok {
		return permanentError{"Recent Asset Slugs must be a JSON array of strings"}
	}
	brand, err := zvidObject(a.get("brandKit", "{}"), "Brand Kit")
	if err != nil {
		return err
	}
	mode := a.str("variationMode", "fresh")
	b := r.body
	b["brief"], b["variationMode"] = brief, mode
	if s := strings.TrimSpace(a.str("variationSeed", "")); s != "" {
		b["variationSeed"] = s
	}
	if mode == "explore" {
		b["exploreCount"] = a.get("exploreCount", 3.0)
	}
	b["aspectRatio"] = a.get("creativeAspectRatio", "16:9")
	b["duration"] = a.get("creativeDuration", 15.0)
	b["style"] = a.get("creativeStyle", "auto")
	if m := a.str("motionIntensity", "auto"); m != "auto" {
		b["motionIntensity"] = m
	}
	b["preferredMedia"] = a.get("preferredMedia", "mixed")
	b["recentAssetSlugs"] = l
	if len(asMap(brand)) > 0 {
		b["brand"] = brand
	}
	return a.err
}
