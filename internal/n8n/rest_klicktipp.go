package n8n

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// KlickTipp (n8n-nodes-klicktipp): email marketing. The credential logs in
// with username/password; the session cookie is cached and renewed on 403.
// POST/PUT bodies are PHP-style form encoded (a[0]=x, f[k]=v).

func init() {
	s := &restService{
		defResource: "tag",
		defOps:      map[string]string{"tag": "index", "subscriber": "index", "contact-tagging": "tag", "field": "index", "opt-in": "index"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			return &restClient{base: ec.apiBase("klicktipp", "https://api.klicktipp.com")}, nil
		},
		ops: map[string]*restOp{
			"tag/index": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				v, err := call("GET", "/tag", nil, nil)
				return ktIDValues(v), err
			})},
			"tag/get": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktRequired(ec, i, "tagId", "Tag ID is missing")
				if err != nil {
					return nil, err
				}
				return call("GET", "/tag/"+url.PathEscape(id), nil, nil)
			})},
			"tag/create": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				name, err := ktRequired(ec, i, "name", "Tag name is missing")
				if err != nil {
					return nil, err
				}
				body := map[string]interface{}{"name": name}
				if d, _ := ec.str(i, "description", ""); d != "" {
					body["text"] = d
				}
				v, err := call("POST", "/tag", body, nil)
				return map[string]interface{}{"id": ktFirst(v)}, err
			})},
			"tag/update": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktRequired(ec, i, "tagId", "Tag ID is missing")
				if err != nil {
					return nil, err
				}
				body := map[string]interface{}{}
				if n, _ := ec.str(i, "name", ""); n != "" {
					body["name"] = n
				}
				if d, _ := ec.str(i, "description", ""); d != "" {
					body["text"] = d
				}
				_, err = call("PUT", "/tag/"+url.PathEscape(id), body, nil)
				return nil, err
			})},
			"tag/delete": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktRequired(ec, i, "tagId", "Tag ID is missing")
				if err != nil {
					return nil, err
				}
				_, err = call("DELETE", "/tag/"+url.PathEscape(id), nil, nil)
				return nil, err
			})},
			"opt-in/index": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				v, err := call("GET", "/list", nil, nil)
				if m, ok := v.(map[string]interface{}); ok {
					for k, x := range m {
						if x == "" {
							m[k] = "Predefined double opt-in process"
						}
					}
				}
				return ktIDValues(v), err
			})},
			"opt-in/get": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktRequired(ec, i, "listId", "Opt-in process ID is missing")
				if err != nil {
					return nil, err
				}
				return call("GET", "/list/"+url.PathEscape(id), nil, nil)
			})},
			"opt-in/getRedirect": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktRequired(ec, i, "listId", "Opt-in process ID is missing")
				if err != nil {
					return nil, err
				}
				email, err := ktRequired(ec, i, "email", "Email is missing")
				if err != nil {
					return nil, err
				}
				v, err := call("POST", "/list/redirect", map[string]interface{}{"listid": id, "email": email}, nil)
				return map[string]interface{}{"redirectUrl": ktFirst(v)}, err
			})},
			"field/index": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				v, err := call("GET", "/field", nil, nil)
				return ktIDValues(v), err
			})},
			"field/get": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktRequired(ec, i, "apiFieldId", "Data field ID is required")
				if err != nil {
					return nil, err
				}
				return call("GET", "/field/"+url.PathEscape(strings.TrimPrefix(id, "field")), nil, nil)
			})},
			"subscriber/index": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				v, err := call("GET", "/subscriber", nil, ktStatusQuery(ec, i))
				return ktIDValues(v), err
			})},
			"subscriber/get": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktSubscriberID(ec, i, call)
				if err != nil {
					return nil, err
				}
				return call("GET", "/subscriber/"+url.PathEscape(id), nil, nil)
			})},
			"subscriber/delete": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktSubscriberID(ec, i, call)
				if err != nil {
					return nil, err
				}
				_, err = call("DELETE", "/subscriber/"+url.PathEscape(id), nil, nil)
				return nil, err
			})},
			"subscriber/update": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				body := map[string]interface{}{}
				if e, _ := ec.str(i, "email", ""); e != "" {
					body["newemail"] = e
				}
				if s, _ := ec.str(i, "smsNumber", ""); s != "" {
					body["newsmsnumber"] = s
				}
				if f := ktFields(ec, i); f != nil {
					body["fields"] = f
				}
				id, err := ktSubscriberID(ec, i, call)
				if err != nil {
					return nil, err
				}
				_, err = call("PUT", "/subscriber/"+url.PathEscape(id), body, nil)
				return nil, err
			})},
			"subscriber/search": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				email, err := ktRequired(ec, i, "email", "Email is missing")
				if err != nil {
					return nil, err
				}
				v, err := call("POST", "/subscriber/search", map[string]interface{}{"email": email}, nil)
				return map[string]interface{}{"id": ktFirst(v)}, err
			})},
			"subscriber/tagged": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				id, err := ktRequired(ec, i, "tagId", "Tag ID is missing")
				if err != nil {
					return nil, err
				}
				v, err := call("POST", "/subscriber/tagged", map[string]interface{}{"tagid": id}, ktStatusQuery(ec, i))
				var out []interface{}
				for _, x := range asList(ktIDValues(v)) {
					m := asMap(x)
					out = append(out, map[string]interface{}{"id": m["id"], "timestamp": m["value"]})
				}
				return out, err
			})},
			"subscriber/subscribe": {run: kt(ktSubscribe)},
			"subscriber/unsubscribe": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				email, err := ktRequired(ec, i, "email", "Email is missing")
				if err != nil {
					return nil, err
				}
				_, err = call("POST", "/subscriber/unsubscribe", map[string]interface{}{"email": email}, nil)
				return nil, err
			})},
			"contact-tagging/tag": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				email, err := ktRequired(ec, i, "email", "Email is missing")
				if err != nil {
					return nil, err
				}
				tags := ktTagIDs(ec, i, "tagId")
				if len(tags) == 0 {
					return nil, permanentError{ec.node.Name + ": Tag ID is missing"}
				}
				_, err = call("POST", "/subscriber/tag", map[string]interface{}{"email": email, "tagids": tags}, nil)
				return nil, err
			})},
			"contact-tagging/untag": {run: kt(func(ec *execCtx, i int, call ktCaller) (interface{}, error) {
				email, err := ktRequired(ec, i, "email", "Email is missing")
				if err != nil {
					return nil, err
				}
				param := "tagId"
				if ec.version >= 4 {
					param = "tagIds"
				}
				tags := ktTagIDs(ec, i, param)
				if len(tags) == 0 {
					return nil, permanentError{ec.node.Name + ": Tag ID is missing"}
				}
				for _, t := range tags {
					if _, err := call("POST", "/subscriber/untag", map[string]interface{}{"email": email, "tagid": t}, nil); err != nil {
						return nil, err
					}
				}
				return nil, nil
			})},
		},
	}
	registerREST("n8n-nodes-klicktipp.klicktipp", s)
	registerREST("CUSTOM.klicktipp", s)
}

type ktCaller func(method, path string, body map[string]interface{}, query url.Values) (interface{}, error)

// ktSessions caches session cookies by credential.
var ktSessions sync.Map

// kt adapts a KlickTipp operation; a nil result means {success: true}.
func kt(fn func(ec *execCtx, i int, call ktCaller) (interface{}, error)) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		cred, err := ec.credential("klickTippApi")
		if err != nil {
			return nil, err
		}
		user, pass := credString(cred, "username"), credString(cred, "password")
		key := c.base + "\x00" + user + "\x00" + pass
		login := func() (string, error) {
			res, _, err := ec.apiCall("POST", c.base+"/account/login", nil, map[string]interface{}{"username": user, "password": pass}, "")
			if err != nil {
				return "", err
			}
			sid, name := toString(res["sessid"]), toString(res["session_name"])
			if sid == "" || name == "" {
				return "", permanentError{ec.node.Name + ": KlickTipp login failed: invalid credentials or missing session data"}
			}
			cookie := name + "=" + sid
			ktSessions.Store(key, cookie)
			return cookie, nil
		}
		call := func(method, path string, body map[string]interface{}, query url.Values) (interface{}, error) {
			r := &restRequest{method: method, path: path, query: query, headers: map[string]string{"Client-Identifier": "n8n-KlickTipp"}}
			if (method == "POST" || method == "PUT") && len(body) > 0 {
				r.raw, r.contentType = []byte(ktForm(body, "")), "application/x-www-form-urlencoded"
			}
			for attempt := 0; ; attempt++ {
				cookie, _ := ktSessions.Load(key)
				if cookie == nil {
					if cookie, err = login(); err != nil {
						return nil, err
					}
				}
				r.headers["Cookie"] = cookie.(string)
				res, err := cnRaw(ec, c, r)
				if err != nil && res.status == 403 && attempt == 0 {
					ktSessions.Delete(key)
					continue
				}
				if err != nil {
					return nil, err
				}
				return cnDecode(res), nil
			}
		}
		v, err := fn(ec, i, call)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return []Item{{JSON: map[string]interface{}{"success": true}}}, nil
		}
		return restItems(v), nil
	}
}

// ktForm encodes a body like PHP's http_build_query.
func ktForm(v map[string]interface{}, prefix string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		name := k
		if prefix != "" {
			name = prefix + "[" + k + "]"
		}
		parts = append(parts, ktFormValue(name, v[k])...)
	}
	return strings.Join(parts, "&")
}

func ktFormValue(name string, v interface{}) []string {
	switch x := v.(type) {
	case map[string]interface{}:
		return []string{ktForm(x, name)}
	case []interface{}:
		var parts []string
		for n, e := range x {
			parts = append(parts, ktFormValue(fmt.Sprintf("%s[%d]", name, n), e)...)
		}
		return parts
	}
	return []string{url.QueryEscape(name) + "=" + url.QueryEscape(toString(v))}
}

func ktRequired(ec *execCtx, i int, name, msg string) (string, error) {
	v, _ := ec.param(i, name, "")
	s := strings.TrimSpace(rlValue(v))
	if s == "" {
		return "", permanentError{ec.node.Name + ": " + msg}
	}
	return s, nil
}

// ktIDValues turns {id: value} into [{id, value}] and [id] into [{id}].
func ktIDValues(v interface{}) interface{} {
	out := []interface{}{}
	switch t := v.(type) {
	case []interface{}:
		for _, id := range t {
			out = append(out, map[string]interface{}{"id": id})
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, map[string]interface{}{"id": k, "value": t[k]})
		}
	}
	return out
}

func ktFirst(v interface{}) interface{} {
	if l, ok := v.([]interface{}); ok && len(l) > 0 {
		return l[0]
	}
	return nil
}

func ktStatusQuery(ec *execCtx, i int) url.Values {
	q := url.Values{}
	for param, key := range map[string]string{"subscriptionStatus": "status", "bounceStatus": "bounceStatus"} {
		v, _ := ec.param(i, param, nil)
		var vals []string
		seen := map[string]bool{}
		for _, x := range asListOrOne(v) {
			if s := toString(x); s != "" && !seen[s] {
				seen[s] = true
				vals = append(vals, s)
			}
		}
		if len(vals) > 0 {
			q.Set(key, strings.Join(vals, ","))
		}
	}
	return q
}

// ktFields reads fields.dataFields [{fieldId, fieldValue}] (first wins).
func ktFields(ec *execCtx, i int) map[string]interface{} {
	v, _ := ec.param(i, "fields.dataFields", nil)
	if v == nil {
		return nil
	}
	out := map[string]interface{}{}
	for _, x := range asList(v) {
		m := asMap(x)
		id := toString(m["fieldId"])
		if _, dup := out[id]; id != "" && !dup {
			out[id] = m["fieldValue"]
		}
	}
	return out
}

func ktTagIDs(ec *execCtx, i int, param string) []interface{} {
	v, _ := ec.param(i, param, nil)
	var out []interface{}
	seen := map[string]bool{}
	for _, x := range asListOrOne(v) {
		if s := toString(rlValue(x)); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, x)
		}
	}
	return out
}

// ktSubscriberID resolves the contact by ID or (v2+) by email; v3 looks up
// by email by default.
func ktSubscriberID(ec *execCtx, i int, call ktCaller) (string, error) {
	def := "id"
	if ec.version >= 3 {
		def = "email"
	}
	if ec.version < 2 {
		def = "id"
	}
	by, _ := ec.str(i, "identifierType", def)
	if by == "id" || ec.version < 2 {
		id, err := ktRequired(ec, i, "subscriberId", "Contact Identifier (ID or Key) is missing")
		if err == nil && strings.ContainsAny(id, " \t\n") {
			err = permanentError{ec.node.Name + ": Contact Identifier (ID or Key) must not contain whitespace"}
		}
		return id, err
	}
	email, err := ktRequired(ec, i, "lookupEmail", "Email is missing")
	if err != nil {
		return "", err
	}
	v, err := call("POST", "/subscriber/search", map[string]interface{}{"email": email}, nil)
	if err != nil {
		return "", err
	}
	id := ktFirst(v)
	if id == nil {
		return "", permanentError{ec.node.Name + ": No contact found for the provided email"}
	}
	return toString(id), nil
}

func ktSubscribe(ec *execCtx, i int, call ktCaller) (interface{}, error) {
	email, _ := ec.str(i, "email", "")
	sms, _ := ec.str(i, "smsNumber", "")
	listID, _ := ec.param(i, "listId", "")
	tags := ktTagIDs(ec, i, "tagId")
	if ec.version >= 4 {
		tags = ktTagIDs(ec, i, "tagIds")
		email, sms = strings.TrimSpace(email), strings.TrimSpace(sms)
	}
	if email == "" && sms == "" {
		return nil, permanentError{ec.node.Name + ": Email or SMS number is missing"}
	}
	body := map[string]interface{}{}
	if email != "" {
		body["email"] = strings.TrimSpace(email)
	}
	if sms != "" {
		body["smsnumber"] = strings.TrimSpace(sms)
	}
	if l := rlValue(listID); l != "" {
		body["listid"] = l
	}
	if len(tags) > 0 {
		body["tagid"] = tags[0]
	}
	if f := ktFields(ec, i); f != nil {
		body["fields"] = f
	}
	v, err := call("POST", "/subscriber", body, nil)
	if err != nil || len(tags) < 2 {
		return v, err
	}
	// v4: further tags are added one by one.
	for _, t := range tags[1:] {
		if email != "" {
			_, err = call("POST", "/subscriber/tag", map[string]interface{}{"email": email, "tagids": []interface{}{t}}, nil)
		} else {
			v, err = call("POST", "/subscriber", map[string]interface{}{"smsnumber": sms, "tagid": t}, nil)
		}
		if err != nil {
			return nil, err
		}
	}
	if id := toString(asMap(v)["id"]); email != "" && id != "" {
		return call("GET", "/subscriber/"+url.PathEscape(id), nil, nil)
	}
	return v, nil
}
