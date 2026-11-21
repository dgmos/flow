package n8n

import (
	"fmt"
	"strings"
)

// gotoHuman (@gotohuman/n8n-nodes-gotohuman): human review requests. Only
// "send" is supported; "sendAndWait" (the default) suspends the execution
// until gotoHuman calls the resume webhook, so it is left out of the table
// and blocked at import.

func init() {
	registerREST("@gotohuman/n8n-nodes-gotohuman.gotoHuman", &restService{
		defResource: "reviewRequest",
		defOps:      map[string]string{"reviewRequest": "sendAndWait"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("gotoHumanApi")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("gotoHuman", "https://api.gotohuman.com"), headers: map[string]string{"x-api-key": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"reviewRequest/send": {run: gotoHumanSend},
			"reviewRequest/delete": {method: "DELETE", path: "/deleteReview", body: "reviewId", post: func(_ *execCtx, _ int, v interface{}) (interface{}, error) {
				if v == nil {
					return map[string]interface{}{"success": true}, nil
				}
				return v, nil
			}},
		},
	})
}

func gotoHumanJSON(ec *execCtx, name string, v interface{}) (interface{}, error) {
	s, ok := v.(string)
	if !ok {
		return v, nil
	}
	var x interface{}
	if err := jsonUnmarshal([]byte(s), &x); err != nil {
		return nil, permanentError{fmt.Sprintf("%s: %s is not valid JSON", ec.node.Name, name)}
	}
	return x, nil
}

func gotoHumanSend(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	tmpl, _ := ec.param(i, "reviewTemplateID", "")
	body := map[string]interface{}{"formId": rlValue(tmpl), "origin": "n8n", "originV": ec.version}
	if ec.version >= 2 {
		raw, _ := ec.param(i, "reviewData", "")
		f, err := gotoHumanJSON(ec, "reviewData", raw)
		if err != nil {
			return nil, err
		}
		body["fields"] = f
		if cfg, _ := ec.param(i, "reviewConfig", "{}"); !restEmpty(cfg) {
			if body["config"], err = gotoHumanJSON(ec, "reviewConfig", cfg); err != nil {
				return nil, err
			}
		}
	} else {
		// v1: resource mapper; array/object fields given as strings are parsed.
		fp, _ := ec.param(i, "fields", nil)
		fm := asMap(fp)
		fields := map[string]interface{}{}
		for k, v := range asMap(fm["value"]) {
			fields[k] = v
		}
		for _, x := range asList(fm["schema"]) {
			s := asMap(x)
			id := toString(s["id"])
			if str, ok := fields[id].(string); ok && (s["type"] == "array" || s["type"] == "object") {
				var j interface{}
				if jsonUnmarshal([]byte(str), &j) != nil {
					return nil, permanentError{fmt.Sprintf("%s: could not parse field '%s' as JSON: %s", ec.node.Name, id, str)}
				}
				fields[id] = j
			}
		}
		body["fields"] = fields
	}
	if a, _ := ec.param(i, "agentID", ""); rlValue(a) != "" {
		body["agentId"] = rlValue(a)
	}
	switch sel, _ := ec.str(i, "metaSelect", "no"); sel {
	case "json":
		if s, _ := ec.str(i, "metaJson", ""); strings.TrimSpace(s) != "" {
			m, err := gotoHumanJSON(ec, "metaJson", s)
			if err != nil {
				return nil, err
			}
			body["meta"] = m
		}
	case "keyValue":
		if v, _ := ec.param(i, "metaKeyValues.metaArray", nil); v != nil {
			m := map[string]interface{}{}
			for _, x := range asList(v) {
				p := asMap(x)
				m[toString(p["key"])] = p["value"]
			}
			body["meta"] = m
		}
	}
	if sel, _ := ec.str(i, "assignToSelect", "all"); sel == "selectByEmail" {
		v, _ := ec.param(i, "assignTo.values", nil)
		emails := []interface{}{}
		for _, x := range asList(v) {
			emails = append(emails, asMap(x)["email"])
		}
		body["assignTo"] = emails
	}
	af := fieldsOf(ec, i, "additionalFields")
	if t := toString(af["title"]); t != "" {
		body["title"] = t
	}
	if v, ok := af["autoApprove"]; ok {
		body["autoApprove"] = toBool(v)
	}
	if w := af["workflow"]; !restEmpty(w) {
		x, err := gotoHumanJSON(ec, "workflow field", w)
		if err != nil {
			return nil, err
		}
		body["workflow"] = x
	}
	if u := toString(af["updateForReviewId"]); u != "" {
		body["updateForReviewId"] = u
	}
	if s, _ := ec.str(i, "sessionId", ""); s != "" {
		body["sessionId"] = s
	}
	res, err := cnRaw(ec, c, &restRequest{method: "POST", path: "/requestReview", body: body})
	if err != nil {
		return nil, err
	}
	// n8n returns the full response.
	return []Item{{JSON: cnFullResponse(res)}}, nil
}
