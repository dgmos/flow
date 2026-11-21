package n8n

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"
)

// Phantombuster (n8n-nodes-base.phantombuster): agents at
// https://api.phantombuster.com/api/v2. launchSync reads the NDJSON stream
// of /agents/launch-sync and returns its summary message.

func init() {
	registerREST("phantombuster", &restService{
		defResource: "agent",
		defOps:      map[string]string{"agent": "launch"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("phantombusterApi")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("phantombuster", "https://api.phantombuster.com/api/v2"),
				headers: map[string]string{"X-Phantombuster-Key": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"agent/delete": {method: "POST", path: "/agents/delete", body: "agentId:id", success: true},
			"agent/get":    {path: "/agents/fetch", query: "agentId:id"},
			"agent/getAll": {path: "/agents/fetch-all", list: true, build: func(ec *execCtx, _ int, _ *restRequest) error {
				restDefaultLimit(ec, 25)
				return nil
			}},
			"agent/getOutput":  {run: pbGetOutput},
			"agent/launch":     {run: pbLaunch(false)},
			"agent/launchSync": {run: pbLaunch(true)},
		},
	})
}

func pbGetOutput(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	a := &cmtyArgs{ec: ec, i: i}
	q := url.Values{}
	for k, v := range asMap(a.get("additionalFields", nil)) {
		q.Set(k, toString(v))
	}
	q.Set("id", a.str("agentId", ""))
	if a.err != nil {
		return nil, a.err
	}
	v, err := cmtyCall(ec, c, "GET", "/agents/fetch-output", q, nil)
	if err != nil {
		return nil, err
	}
	if !a.bool("resolveData", true) {
		return restItems(v), nil
	}
	r, err := cmtyCall(ec, c, "GET", "/containers/fetch-result-object", url.Values{"id": {toString(asMap(v)["containerId"])}}, nil)
	if err != nil {
		return nil, err
	}
	ro := asMap(r)["resultObject"]
	if ro == nil {
		return []Item{{JSON: map[string]interface{}{}}}, nil
	}
	var out interface{}
	if err := json.Unmarshal([]byte(toString(ro)), &out); err != nil {
		return nil, permanentError{ec.node.Name + ": resultObject is not valid JSON"}
	}
	return restItems(out), nil
}

func pbLaunch(sync bool) func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		a := &cmtyArgs{ec: ec, i: i}
		body := map[string]interface{}{"id": a.str("agentId", "")}
		af := map[string]interface{}{}
		for k, v := range asMap(a.get("additionalFields", nil)) {
			af[k] = v
		}
		if a.bool("jsonParameters", false) {
			for _, f := range [][2]string{{"argumentsJson", "arguments"}, {"bonusArgumentJson", "bonusArgument"}} {
				if s, ok := af[f[0]]; ok && !restEmpty(s) {
					v, err := cmtyJSON(s, f[1])
					if err != nil {
						return nil, permanentError{ec.node.Name + ": " + map[string]string{"arguments": "Arguments", "bonusArgument": "Bonus Argument"}[f[1]] + " must provide a valid JSON"}
					}
					body[f[1]] = v
					delete(af, f[0])
				}
			}
		} else {
			for _, f := range [][3]string{{"argumentsUi", "argumentValues", "arguments"}, {"bonusArgumentUi", "bonusArgumentValue", "bonusArgument"}} {
				obj := map[string]interface{}{}
				for _, x := range asList(asMap(af[f[0]])[f[1]]) {
					m := asMap(x)
					obj[toString(m["key"])] = m["value"]
				}
				if len(obj) > 0 {
					body[f[2]] = obj
				}
				delete(af, f[0])
			}
		}
		for k, v := range af {
			body[k] = v
		}
		if a.err != nil {
			return nil, a.err
		}
		if !sync {
			v, err := cmtyCall(ec, c, "POST", "/agents/launch", nil, body)
			if err != nil {
				return nil, err
			}
			if a.bool("resolveData", true) {
				if v, err = cmtyCall(ec, c, "GET", "/containers/fetch", url.Values{"id": {toString(asMap(v)["containerId"])}}, nil); err != nil {
					return nil, err
				}
			}
			return restItems(v), nil
		}
		summary, container, err := pbStream(ec, c, &restRequest{method: "POST", path: "/agents/launch-sync", query: url.Values{}, body: body})
		for n := 0; err == nil && summary == nil && container != "" && n < 5; n++ {
			if err = cmtySleep(ec, time.Second); err != nil {
				break
			}
			summary, _, err = pbStream(ec, c, &restRequest{method: "GET", path: "/containers/attach", query: url.Values{"id": {container}}})
		}
		if err != nil {
			return nil, err
		}
		if summary == nil {
			return nil, permanentError{ec.node.Name + ": Stream did not provide a response"}
		}
		return restItems(summary), nil
	}
}

// pbStream reads an NDJSON stream: "start" carries the container id,
// "summary" ends it with the result, "error" fails it.
func pbStream(ec *execCtx, c *restClient, r *restRequest) (interface{}, string, error) {
	res, err := cnRaw(ec, c, r)
	if err != nil {
		return nil, "", err
	}
	container := ""
	for _, line := range strings.Split(string(res.body), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, "", permanentError{ec.node.Name + ": Phantombuster sent a malformed NDJSON line"}
		}
		switch toString(msg["type"]) {
		case "start":
			if id := asMap(msg["data"])["containerId"]; id != nil {
				container = toString(id)
			}
		case "error":
			reason, ok := msg["data"].(string)
			if !ok {
				reason = mustJSON(msg["data"])
			}
			return nil, container, permanentError{ec.node.Name + ": " + reason}
		case "summary":
			if msg["data"] == nil {
				return map[string]interface{}{}, container, nil
			}
			return msg["data"], container, nil
		}
	}
	return nil, container, nil
}
