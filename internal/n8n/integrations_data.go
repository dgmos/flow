package n8n

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// fieldsOf evaluates every key of a collection parameter (additionalFields,
// updateFields, options …) for item i.
func fieldsOf(ec *execCtx, i int, key string) map[string]interface{} {
	m := map[string]interface{}{}
	for k := range asMap(mustParam(ec, key, map[string]interface{}{})) {
		v, err := ec.param(i, key+"."+k, nil)
		if err == nil {
			m[k] = v
		}
	}
	return m
}

// ---------------------------------------------------------------- Baserow

func baserowNode(ec *execCtx) ([][]Item, error) {
	c, err := ec.credential("baserowApi")
	if err != nil {
		return nil, err
	}
	host := credBaseURL(c, "https://api.baserow.io")
	tok, _, err := ec.apiCall("POST", host+"/api/user/token-auth/", nil, map[string]interface{}{"email": credString(c, "username"), "username": credString(c, "username"), "password": credString(c, "password")}, "")
	if err != nil {
		return nil, fmt.Errorf("baserow login: %w", err)
	}
	h := map[string]string{"Authorization": "JWT " + firstNonEmpty(toString(tok["access_token"]), toString(tok["token"]))}
	names := map[string]map[string]string{} // table -> field_N -> name
	fieldMap := func(table string) (map[string]string, error) {
		if m, ok := names[table]; ok {
			return m, nil
		}
		_, raw, err := ec.apiCall("GET", host+"/api/database/fields/table/"+url.PathEscape(table)+"/", h, nil, "")
		if err != nil {
			return nil, err
		}
		var fields []map[string]interface{}
		_ = jsonUnmarshal(raw, &fields)
		m := map[string]string{}
		for _, f := range fields {
			m["field_"+toString(f["id"])] = toString(f["name"])
		}
		names[table] = m
		return m, nil
	}
	named := func(table string, row map[string]interface{}) (map[string]interface{}, error) {
		m, err := fieldMap(table)
		if err != nil {
			return nil, err
		}
		out := map[string]interface{}{}
		for k, v := range row {
			if n, ok := m[k]; ok {
				out[n] = v
			} else {
				out[k] = v
			}
		}
		return out, nil
	}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		op, _ := ec.str(i, "operation", "getAll")
		table, _ := ec.str(i, "tableId", "")
		rows := host + "/api/database/rows/table/" + url.PathEscape(table) + "/"
		rowID, _ := ec.str(i, "rowId", "")
		body := func() (map[string]interface{}, error) {
			m, err := fieldMap(table)
			if err != nil {
				return nil, err
			}
			byName := map[string]string{}
			for id, n := range m {
				byName[n] = id
			}
			out := map[string]interface{}{}
			if d, _ := ec.str(i, "dataToSend", "defineBelow"); d == "autoMapInputData" {
				skip := map[string]bool{}
				if s, _ := ec.str(i, "inputsToIgnore", ""); s != "" {
					for _, k := range splitList(s) {
						skip[k] = true
					}
				}
				for k, v := range it.JSON {
					if id, ok := byName[k]; ok && !skip[k] {
						out[id] = v
					}
				}
				return out, nil
			}
			fv, _ := ec.param(i, "fieldsUi.fieldValues", nil)
			for _, f := range asList(fv) {
				fm := asMap(f)
				id := toString(fm["fieldId"])
				if !strings.HasPrefix(id, "field_") {
					if n, ok := byName[id]; ok {
						id = n
					} else {
						id = "field_" + id
					}
				}
				out[id] = fm["fieldValue"]
			}
			return out, nil
		}
		switch op {
		case "getAll":
			all, limit := pageCount(ec, i, 50)
			q := url.Values{}
			opts := fieldsOf(ec, i, "additionalOptions")
			if s := toString(opts["search"]); s != "" {
				q.Set("search", s)
			}
			for _, f := range asList(asMap(opts["filters"])["fields"]) {
				fm := asMap(f)
				q.Set("filter__field_"+strings.TrimPrefix(toString(fm["field"]), "field_")+"__"+toString(fm["operator"]), toString(fm["value"]))
			}
			if t := toString(opts["filterType"]); t != "" {
				q.Set("filter_type", t)
			}
			var order []string
			for _, s := range asList(asMap(opts["order"])["fields"]) {
				sm := asMap(s)
				dir := ""
				if toString(sm["direction"]) == "-" {
					dir = "-"
				}
				order = append(order, dir+"field_"+strings.TrimPrefix(toString(sm["field"]), "field_"))
			}
			if len(order) > 0 {
				q.Set("order_by", strings.Join(order, ","))
			}
			var out []Item
			for page := 1; page <= 1000; page++ {
				q.Set("page", strconv.Itoa(page))
				q.Set("size", "100")
				res, _, err := ec.apiCall("GET", rows+"?"+q.Encode(), h, nil, "")
				if err != nil {
					return nil, err
				}
				for _, r := range asList(res["results"]) {
					n, err := named(table, asMap(r))
					if err != nil {
						return nil, err
					}
					out = append(out, Item{JSON: n})
				}
				if res["next"] == nil || (!all && len(out) >= limit) {
					break
				}
			}
			return capItems(out, all, limit), nil
		case "get", "delete":
			method := "GET"
			if op == "delete" {
				method = "DELETE"
			}
			res, _, err := ec.apiCall(method, rows+url.PathEscape(rowID)+"/", h, nil, "")
			if err != nil {
				return nil, err
			}
			if op == "delete" {
				return one(map[string]interface{}{"success": true}), nil
			}
			n, err := named(table, res)
			return one(n), err
		case "create", "update":
			b, err := body()
			if err != nil {
				return nil, err
			}
			method, u := "POST", rows
			if op == "update" {
				method, u = "PATCH", rows+url.PathEscape(rowID)+"/"
			}
			res, _, err := ec.apiCall(method, u, h, b, "")
			if err != nil {
				return nil, err
			}
			n, err := named(table, res)
			return one(n), err
		}
		return nil, unsupportedOp("Baserow", "row", op)
	})
}

// ---------------------------------------------------------------- NocoDB

func nocoDbNode(ec *execCtx) ([][]Item, error) {
	credType, header := "nocoDbApiToken", "xc-token"
	if a, _ := ec.str(0, "authentication", "nocoDbApiToken"); a == "nocoDb" {
		credType, header = "nocoDb", "xc-auth"
	}
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	host := credBaseURL(c, "https://app.nocodb.com")
	h := map[string]string{header: firstNonEmpty(credString(c, "apiToken"), credString(c, "apiKey"))}
	version := int(toFloatDefault(mustParam(ec, "version", 3.0), 3))
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		op, _ := ec.str(i, "operation", "getAll")
		project, _ := ec.rlString(i, "projectId")
		table, _ := ec.rlString(i, "table")
		var base string
		switch version {
		case 1:
			base = host + "/nc/" + url.PathEscape(project) + "/api/v1/" + url.PathEscape(table)
		case 4:
			base = host + "/api/v2/tables/" + url.PathEscape(table) + "/records"
		default:
			base = host + "/api/v1/db/data/noco/" + url.PathEscape(project) + "/" + url.PathEscape(table)
		}
		id, _ := ec.str(i, "id", "")
		data := func() map[string]interface{} {
			if d, _ := ec.str(i, "dataToSend", "defineBelow"); d == "autoMapInputData" {
				return cloneJSON(it.JSON)
			}
			out := map[string]interface{}{}
			fv, _ := ec.param(i, "fieldsUi.fieldValues", nil)
			for _, f := range asList(fv) {
				fm := asMap(f)
				out[toString(fm["fieldName"])] = fm["fieldValue"]
			}
			return out
		}
		switch op {
		case "getAll":
			all, limit := pageCount(ec, i, 50)
			q := url.Values{}
			for k, v := range fieldsOf(ec, i, "options") {
				switch k {
				case "where", "sort", "viewId":
					q.Set(k, toString(v))
				case "fields":
					var fs []string
					for _, x := range asList(v) {
						fs = append(fs, toString(x))
					}
					if len(fs) == 0 {
						fs = splitList(toString(v))
					}
					q.Set("fields", strings.Join(fs, ","))
				}
			}
			var out []Item
			for offset := 0; offset < 1000000; {
				q.Set("limit", "100")
				q.Set("offset", strconv.Itoa(offset))
				res, raw, err := ec.apiCall("GET", base+"?"+q.Encode(), h, nil, "")
				if err != nil {
					return nil, err
				}
				var page []Item
				if l, ok := res["list"]; ok {
					page = listItems(l)
				} else {
					var arr []interface{}
					_ = jsonUnmarshal(raw, &arr)
					page = listItems(arr)
				}
				out = append(out, page...)
				offset += len(page)
				if len(page) < 100 || (!all && len(out) >= limit) || toBool(asMap(res["pageInfo"])["isLastPage"]) {
					break
				}
			}
			return capItems(out, all, limit), nil
		case "get":
			res, _, err := ec.apiCall("GET", base+"/"+url.PathEscape(id), h, nil, "")
			return one(res), err
		case "delete":
			if version == 4 {
				res, _, err := ec.apiCall("DELETE", base, h, []interface{}{map[string]interface{}{"Id": id}}, "")
				return one(res), err
			}
			if _, _, err := ec.apiCall("DELETE", base+"/"+url.PathEscape(id), h, nil, ""); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "create":
			res, _, err := ec.apiCall("POST", base, h, data(), "")
			return one(res), err
		case "update":
			d := data()
			if version == 4 {
				d["Id"] = id
				res, _, err := ec.apiCall("PATCH", base, h, d, "")
				return one(res), err
			}
			res, _, err := ec.apiCall("PATCH", base+"/"+url.PathEscape(id), h, d, "")
			return one(res), err
		}
		return nil, unsupportedOp("NocoDB", "row", op)
	})
}

// ---------------------------------------------------------------- Odoo

var odooModels = map[string]string{"contact": "res.partner", "opportunity": "crm.lead", "note": "note.note"}

func odooNode(ec *execCtx) ([][]Item, error) {
	c, err := ec.credential("odooApi")
	if err != nil {
		return nil, err
	}
	endpoint := credBaseURL(c, "") + "/jsonrpc"
	db := credString(c, "db")
	if db == "" {
		if u, err := url.Parse(credBaseURL(c, "")); err == nil {
			db = strings.Split(u.Hostname(), ".")[0]
		}
	}
	user, pass := credString(c, "username"), credString(c, "password")
	rpc := func(service, method string, args ...interface{}) (interface{}, error) {
		res, _, err := ec.apiCall("POST", endpoint, nil, map[string]interface{}{"jsonrpc": "2.0", "method": "call", "id": 1,
			"params": map[string]interface{}{"service": service, "method": method, "args": args}}, "")
		if err != nil {
			return nil, err
		}
		if e := asMap(res["error"]); e != nil {
			msg := toString(asMap(e["data"])["message"])
			if msg == "" {
				msg = toString(e["message"])
			}
			return nil, permanentError{"odoo: " + msg}
		}
		return res["result"], nil
	}
	uidV, err := rpc("common", "login", db, user, pass)
	if err != nil {
		return nil, err
	}
	if toFloatDefault(uidV, 0) == 0 {
		return nil, permanentError{"odoo: login failed"}
	}
	uid := uidV
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOp(i, "contact", "create")
		model := odooModels[r]
		if r == "custom" {
			model, _ = ec.str(i, "customResource", "")
		}
		if model == "" {
			return nil, unsupportedOp("Odoo", r, o)
		}
		exec := func(method string, args []interface{}, kwargs map[string]interface{}) (interface{}, error) {
			if kwargs == nil {
				kwargs = map[string]interface{}{}
			}
			return rpc("object", "execute_kw", db, uid, pass, model, method, args, kwargs)
		}
		idKey := map[string]string{"contact": "contactId", "opportunity": "opportunityId", "note": "noteId", "custom": "customResourceId"}[r]
		idStr, _ := ec.str(i, idKey, "")
		id, _ := strconv.Atoi(idStr)
		fieldsList := func() []interface{} {
			v, _ := ec.param(i, "options.fieldsList", nil)
			return asList(v)
		}
		switch o {
		case "getAll":
			all, limit := pageCount(ec, i, 50)
			kw := map[string]interface{}{"fields": fieldsList()}
			if !all {
				kw["limit"] = limit
			}
			var domain []interface{}
			for _, f := range asList(asMap(mustParam(ec, "filterRequest", map[string]interface{}{}))["filter"]) {
				fm := asMap(f)
				domain = append(domain, []interface{}{fm["fieldName"], odooOperator(toString(fm["operator"])), fm["value"]})
			}
			if domain == nil {
				domain = []interface{}{}
			}
			res, err := exec("search_read", []interface{}{domain}, kw)
			if err != nil {
				return nil, err
			}
			return listItems(res), nil
		case "get":
			res, err := exec("read", []interface{}{[]interface{}{id}}, map[string]interface{}{"fields": fieldsList()})
			if err != nil {
				return nil, err
			}
			return listItems(res), nil
		case "delete":
			if _, err := exec("unlink", []interface{}{[]interface{}{id}}, nil); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "create", "update":
			vals := fieldsOf(ec, i, map[string]string{"create": "additionalFields", "update": "updateFields"}[o])
			if r == "custom" {
				vals = map[string]interface{}{}
				fv, _ := ec.param(i, "fieldsToCreateOrUpdate.fields", nil)
				for _, f := range asList(fv) {
					fm := asMap(f)
					vals[toString(fm["fieldName"])] = fm["fieldValue"]
				}
			}
			switch r {
			case "contact":
				if o == "create" {
					n, _ := ec.str(i, "contactName", "")
					vals["name"] = n
				}
			case "opportunity":
				if o == "create" {
					n, _ := ec.str(i, "opportunityName", "")
					vals["name"] = n
				}
			case "note":
				m, _ := ec.str(i, "memo", "")
				if m != "" {
					vals["memo"] = m
				}
			}
			if o == "create" {
				res, err := exec("create", []interface{}{vals}, nil)
				if err != nil {
					return nil, err
				}
				return one(map[string]interface{}{"id": res}), nil
			}
			if _, err := exec("write", []interface{}{[]interface{}{id}, vals}, nil); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"id": id}), nil
		}
		return nil, unsupportedOp("Odoo", r, o)
	})
}

func odooOperator(op string) string {
	m := map[string]string{"equal": "=", "notEqual": "!=", "greaterThen": ">", "lesserThen": "<", "greaterOrEqual": ">=", "lesserOrEqual": "<=", "like": "like", "in": "in", "notIn": "not in", "childOf": "child_of"}
	if v, ok := m[op]; ok {
		return v
	}
	return op
}

// ---------------------------------------------------------------- Jira

func jiraNode(ec *execCtx) ([][]Item, error) {
	ver, _ := ec.str(0, "jiraVersion", "cloud")
	credType := map[string]string{"cloud": "jiraSoftwareCloudApi", "server": "jiraSoftwareServerApi", "serverPat": "jiraSoftwareServerPatApi"}[ver]
	if credType == "" {
		credType = "jiraSoftwareCloudApi"
	}
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(credString(c, "domain"), "/")
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	h := basicAuth(credString(c, "email"), firstNonEmpty(credString(c, "apiToken"), credString(c, "password")))
	if ver == "serverPat" {
		h = map[string]string{"Authorization": "Bearer " + credString(c, "personalAccessToken")}
	}
	call := func(method, path string, body interface{}) (map[string]interface{}, []byte, error) {
		return ec.apiCall(method, base+path, h, body, "")
	}
	adf := func(text string) interface{} {
		return map[string]interface{}{"type": "doc", "version": 1, "content": []interface{}{map[string]interface{}{"type": "paragraph", "content": []interface{}{map[string]interface{}{"type": "text", "text": text}}}}}
	}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOpBy(i, "issue", map[string]string{"issue": "create", "issueComment": "add", "issueAttachment": "add"})
		key, _ := ec.str(i, "issueKey", "")
		ip := "/rest/api/2/issue/" + url.PathEscape(key)
		switch r + "." + o {
		case "issue.create":
			project, _ := ec.rlString(i, "project")
			typ, _ := ec.rlString(i, "issueType")
			summary, _ := ec.str(i, "summary", "")
			fields := map[string]interface{}{"project": map[string]interface{}{"id": project}, "issuetype": map[string]interface{}{"id": typ}, "summary": summary}
			if err := jiraFields(fields, fieldsOf(ec, i, "additionalFields")); err != nil {
				return nil, err
			}
			res, _, err := call("POST", "/rest/api/2/issue", map[string]interface{}{"fields": fields})
			return one(res), err
		case "issue.get":
			res, _, err := call("GET", ip, nil)
			return one(res), err
		case "issue.delete":
			if _, _, err := call("DELETE", ip, nil); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "issue.update":
			uf := fieldsOf(ec, i, "updateFields")
			status := toString(uf["statusId"])
			delete(uf, "statusId")
			fields := map[string]interface{}{}
			if err := jiraFields(fields, uf); err != nil {
				return nil, err
			}
			if len(fields) > 0 {
				if _, _, err := call("PUT", ip, map[string]interface{}{"fields": fields}); err != nil {
					return nil, err
				}
			}
			if status != "" {
				if _, _, err := call("POST", ip+"/transitions", map[string]interface{}{"transition": map[string]interface{}{"id": status}}); err != nil {
					return nil, err
				}
			}
			return one(map[string]interface{}{"success": true}), nil
		case "issue.getAll":
			all, limit := pageCount(ec, i, 50)
			opts := fieldsOf(ec, i, "options")
			q := url.Values{"jql": {toString(opts["jql"])}}
			if f := toString(opts["fields"]); f != "" {
				q.Set("fields", f)
			}
			if e := toString(opts["expand"]); e != "" {
				q.Set("expand", e)
			}
			var out []Item
			for start := 0; start < 100000; {
				q.Set("startAt", strconv.Itoa(start))
				q.Set("maxResults", "100")
				res, _, err := call("GET", "/rest/api/2/search?"+q.Encode(), nil)
				if err != nil {
					return nil, err
				}
				page := listItems(res["issues"])
				out = append(out, page...)
				start += len(page)
				if len(page) == 0 || start >= int(toFloatDefault(res["total"], 0)) || (!all && len(out) >= limit) {
					break
				}
			}
			return capItems(out, all, limit), nil
		case "issue.transitions":
			res, _, err := call("GET", ip+"/transitions", nil)
			if err != nil {
				return nil, err
			}
			return listItems(res["transitions"]), nil
		case "issueComment.add":
			comment, _ := ec.str(i, "comment", "")
			if ver == "cloud" {
				if wiki, _ := ec.boolean(i, "options.wikiMarkup", false); !wiki {
					res, _, err := call("POST", "/rest/api/3/issue/"+url.PathEscape(key)+"/comment", map[string]interface{}{"body": adf(comment)})
					return one(res), err
				}
			}
			res, _, err := call("POST", ip+"/comment", map[string]interface{}{"body": comment})
			return one(res), err
		case "issueComment.getAll":
			all, limit := pageCount(ec, i, 50)
			res, _, err := call("GET", ip+"/comment?maxResults=1000", nil)
			if err != nil {
				return nil, err
			}
			return capItems(listItems(res["comments"]), all, limit), nil
		case "issueComment.get", "issueComment.remove", "issueComment.update":
			cid, _ := ec.str(i, "commentId", "")
			cp := ip + "/comment/" + url.PathEscape(cid)
			switch o {
			case "get":
				res, _, err := call("GET", cp, nil)
				return one(res), err
			case "remove":
				if _, _, err := call("DELETE", cp, nil); err != nil {
					return nil, err
				}
				return one(map[string]interface{}{"success": true}), nil
			default:
				comment, _ := ec.str(i, "comment", "")
				res, _, err := call("PUT", cp, map[string]interface{}{"body": comment})
				return one(res), err
			}
		}
		return nil, unsupportedOp("Jira", r, o)
	})
}

// jiraFields maps n8n's additional/update fields to Jira issue fields.
func jiraFields(dst, src map[string]interface{}) error {
	for k, v := range src {
		switch k {
		case "summary", "description", "environment", "duedate":
			dst[k] = v
		case "assignee", "reporter":
			dst[k] = map[string]interface{}{"accountId": rlValue(v)}
		case "priority":
			dst[k] = map[string]interface{}{"id": rlValue(v)}
		case "labels":
			l := append([]interface{}(nil), asList(v)...)
			if l == nil {
				for _, s := range splitList(toString(v)) {
					l = append(l, s)
				}
			}
			dst[k] = l
		case "parentIssueKey":
			dst["parent"] = map[string]interface{}{"key": v}
		case "customFieldsUi":
			for _, f := range asList(asMap(v)["customFieldsValues"]) {
				fm := asMap(f)
				dst[toString(rlValue(fm["fieldId"]))] = fm["fieldValue"]
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- Ghost

func ghostNode(ec *execCtx) ([][]Item, error) {
	source, _ := ec.str(0, "source", "contentApi")
	credType := "ghostContentApi"
	if source == "adminApi" {
		credType = "ghostAdminApi"
	}
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	base := credBaseURL(c, "")
	h := map[string]string{"Accept-Version": "v5.0"}
	key := credString(c, "apiKey")
	api := base + "/ghost/api/content"
	if source == "adminApi" {
		api = base + "/ghost/api/admin"
		id, secret, ok := strings.Cut(key, ":")
		if !ok {
			return nil, permanentError{"Ghost: admin API key must be id:secret"}
		}
		sec, err := hex.DecodeString(secret)
		if err != nil {
			return nil, permanentError{"Ghost: admin API key secret is not hex"}
		}
		now := time.Now().Unix()
		tok, err := signJWT(map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": id}, map[string]interface{}{"iat": now, "exp": now + 300, "aud": "/admin/"}, sec)
		if err != nil {
			return nil, err
		}
		h["Authorization"] = "Ghost " + tok
	}
	withKey := func(q url.Values) string {
		if source == "contentApi" {
			q.Set("key", key)
		}
		return q.Encode()
	}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		op, _ := ec.str(i, "operation", "get")
		switch op {
		case "get":
			by, _ := ec.str(i, "by", "id")
			ident, _ := ec.str(i, "identifier", "")
			p := "/posts/" + url.PathEscape(ident) + "/"
			if by == "slug" {
				p = "/posts/slug/" + url.PathEscape(ident) + "/"
			}
			q := url.Values{}
			for k, v := range fieldsOf(ec, i, "options") {
				q.Set(k, toString(v))
			}
			res, _, err := ec.apiCall("GET", api+p+"?"+withKey(q), h, nil, "")
			if err != nil {
				return nil, err
			}
			return listItems(res["posts"]), nil
		case "getAll":
			all, limit := pageCount(ec, i, 50)
			q := url.Values{"limit": {"all"}}
			if !all {
				q.Set("limit", strconv.Itoa(limit))
			}
			for k, v := range fieldsOf(ec, i, "options") {
				q.Set(k, toString(v))
			}
			res, _, err := ec.apiCall("GET", api+"/posts/?"+withKey(q), h, nil, "")
			if err != nil {
				return nil, err
			}
			return capItems(listItems(res["posts"]), all, limit), nil
		case "create", "update":
			if source != "adminApi" {
				return nil, permanentError{"Ghost: creating and updating posts needs the Admin API"}
			}
			post := fieldsOf(ec, i, map[string]string{"create": "additionalFields", "update": "updateFields"}[op])
			q := url.Values{}
			if op == "create" {
				t, _ := ec.str(i, "title", "")
				post["title"] = t
				content, _ := ec.str(i, "content", "")
				if f, _ := ec.str(i, "contentFormat", "html"); f == "mobileDoc" {
					post["mobiledoc"] = content
				} else {
					post["html"] = content
					q.Set("source", "html")
				}
				res, _, err := ec.apiCall("POST", api+"/posts/?"+q.Encode(), h, map[string]interface{}{"posts": []interface{}{post}}, "")
				if err != nil {
					return nil, err
				}
				return listItems(res["posts"]), nil
			}
			id, _ := ec.str(i, "postId", "")
			cur, _, err := ec.apiCall("GET", api+"/posts/"+url.PathEscape(id)+"/", h, nil, "")
			if err != nil {
				return nil, err
			}
			if posts := asList(cur["posts"]); len(posts) > 0 {
				post["updated_at"] = asMap(posts[0])["updated_at"]
			}
			if _, ok := post["html"]; ok {
				q.Set("source", "html")
			}
			res, _, err := ec.apiCall("PUT", api+"/posts/"+url.PathEscape(id)+"/?"+q.Encode(), h, map[string]interface{}{"posts": []interface{}{post}}, "")
			if err != nil {
				return nil, err
			}
			return listItems(res["posts"]), nil
		case "delete":
			id, _ := ec.str(i, "postId", "")
			if _, _, err := ec.apiCall("DELETE", api+"/posts/"+url.PathEscape(id)+"/", h, nil, ""); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		}
		return nil, unsupportedOp("Ghost", "post", op)
	})
}

// ---------------------------------------------------------------- Dropbox

func dropboxNode(ec *execCtx) ([][]Item, error) {
	credType := "dropboxApi"
	if a, _ := ec.str(0, "authentication", "accessToken"); a == "oAuth2" {
		credType = "dropboxOAuth2Api"
	}
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	tok := credString(c, "accessToken")
	if tok == "" {
		tok = accessToken(c)
	}
	api := ec.apiBase("dropbox", "https://api.dropboxapi.com") + "/2"
	content := ec.apiBase("dropboxcontent", "https://content.dropboxapi.com") + "/2"
	h := func(arg interface{}) map[string]string {
		m := map[string]string{"Authorization": "Bearer " + tok}
		if arg != nil {
			m["Dropbox-API-Arg"] = asciiJSON(mustJSON(arg))
		}
		return m
	}
	rpc := func(path string, body interface{}) (map[string]interface{}, error) {
		res, _, err := ec.apiCall("POST", api+path, h(nil), body, "")
		return res, err
	}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOpBy(i, "file", map[string]string{"file": "upload", "folder": "create", "search": "query"})
		path, _ := ec.str(i, "path", "")
		switch r + "." + o {
		case "file.upload":
			var data []byte
			if bin, _ := ec.boolean(i, "binaryData", false); bin {
				prop, _ := ec.str(i, "binaryPropertyName", "data")
				d, _, _, err := binaryOf(it, prop)
				if err != nil {
					return nil, err
				}
				data = d
			} else {
				s, _ := ec.str(i, "fileContent", "")
				data = []byte(s)
			}
			mode := "add"
			if ow, _ := ec.boolean(i, "overwrite", false); ow {
				mode = "overwrite"
			}
			res, _, err := ec.apiCall("POST", content+"/files/upload", h(map[string]interface{}{"path": path, "mode": mode}), data, "application/octet-stream")
			return one(res), err
		case "file.download":
			res, err := ec.doRequest("POST", content+"/files/download", h(map[string]interface{}{"path": path}), nil, "")
			if err != nil {
				return nil, err
			}
			prop, _ := ec.str(i, "binaryPropertyName", "data")
			name := path[strings.LastIndex(path, "/")+1:]
			return []Item{{JSON: it.JSON, Binary: map[string]interface{}{prop: binaryData(res.body, mimeByExt(name), name)}}}, nil
		case "file.delete", "folder.delete":
			res, err := rpc("/files/delete_v2", map[string]interface{}{"path": path})
			return one(res), err
		case "file.copy", "folder.copy", "file.move", "folder.move":
			from, _ := ec.str(i, "path", "")
			to, _ := ec.str(i, "toPath", "")
			res, err := rpc("/files/"+o+"_v2", map[string]interface{}{"from_path": from, "to_path": to})
			return one(res), err
		case "folder.create":
			res, err := rpc("/files/create_folder_v2", map[string]interface{}{"path": path})
			return one(res), err
		case "folder.list":
			all, limit := pageCount(ec, i, 100)
			res, err := rpc("/files/list_folder", map[string]interface{}{"path": path, "limit": 2000})
			if err != nil {
				return nil, err
			}
			out := listItems(res["entries"])
			for toBool(res["has_more"]) && (all || len(out) < limit) {
				if res, err = rpc("/files/list_folder/continue", map[string]interface{}{"cursor": res["cursor"]}); err != nil {
					return nil, err
				}
				out = append(out, listItems(res["entries"])...)
			}
			return capItems(out, all, limit), nil
		case "search.query":
			q, _ := ec.str(i, "query", "")
			all, limit := pageCount(ec, i, 100)
			res, err := rpc("/files/search_v2", map[string]interface{}{"query": q})
			if err != nil {
				return nil, err
			}
			var out []Item
			for _, m := range asList(res["matches"]) {
				if md := asMap(asMap(asMap(m)["metadata"])["metadata"]); md != nil {
					out = append(out, Item{JSON: md})
				}
			}
			return capItems(out, all, limit), nil
		}
		return nil, unsupportedOp("Dropbox", r, o)
	})
}

// asciiJSON escapes non-ASCII characters, as HTTP headers require.
func asciiJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 {
			b.WriteRune(r)
		} else if r > 0xFFFF {
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		} else {
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- Google Tasks

func googleTasksNode(ec *execCtx) ([][]Item, error) {
	h, err := ec.googleAuth("tasks", "googleTasksOAuth2Api")
	if err != nil {
		return nil, err
	}
	base := ec.apiBase("tasks", "https://www.googleapis.com") + "/tasks/v1"
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		op, _ := ec.str(i, "operation", "create")
		list, _ := ec.str(i, "task", "@default")
		lp := base + "/lists/" + url.PathEscape(list) + "/tasks"
		id, _ := ec.str(i, "taskId", "")
		taskBody := func(f map[string]interface{}) map[string]interface{} {
			b := map[string]interface{}{}
			for k, v := range f {
				switch k {
				case "dueDate":
					if t, ok := timeOf(v); ok {
						b["due"] = t.UTC().Format(time.RFC3339)
					}
				case "completed":
					if t, ok := timeOf(v); ok {
						b["completed"] = t.UTC().Format(time.RFC3339)
					}
				case "title", "notes", "status", "deleted":
					b[k] = v
				}
			}
			return b
		}
		switch op {
		case "create":
			f := fieldsOf(ec, i, "additionalFields")
			b := taskBody(f)
			t, _ := ec.str(i, "title", "")
			b["title"] = t
			q := url.Values{}
			for _, k := range []string{"parent", "previous"} {
				if s := toString(f[k]); s != "" {
					q.Set(k, s)
				}
			}
			res, _, err := ec.apiCall("POST", lp+"?"+q.Encode(), h, b, "")
			return one(res), err
		case "get":
			res, _, err := ec.apiCall("GET", lp+"/"+url.PathEscape(id), h, nil, "")
			return one(res), err
		case "delete":
			if _, _, err := ec.apiCall("DELETE", lp+"/"+url.PathEscape(id), h, nil, ""); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "update":
			res, _, err := ec.apiCall("PATCH", lp+"/"+url.PathEscape(id), h, taskBody(fieldsOf(ec, i, "updateFields")), "")
			return one(res), err
		case "getAll":
			all, limit := pageCount(ec, i, 20)
			q := url.Values{}
			for k, v := range fieldsOf(ec, i, "additionalFields") {
				switch k {
				case "completedMax", "completedMin", "dueMin", "dueMax", "updatedMin":
					if t, ok := timeOf(v); ok {
						q.Set(k, t.UTC().Format(time.RFC3339))
					}
				case "showCompleted", "showDeleted", "showHidden":
					q.Set(k, strconv.FormatBool(toBool(v)))
				}
			}
			var out []Item
			for page := 0; page < 100; page++ {
				q.Set("maxResults", "100")
				res, _, err := ec.apiCall("GET", lp+"?"+q.Encode(), h, nil, "")
				if err != nil {
					return nil, err
				}
				out = append(out, listItems(res["items"])...)
				next := toString(res["nextPageToken"])
				if next == "" || (!all && len(out) >= limit) {
					break
				}
				q.Set("pageToken", next)
			}
			return capItems(out, all, limit), nil
		}
		return nil, unsupportedOp("Google Tasks", "task", op)
	})
}

// ---------------------------------------------------------------- Google Analytics (GA4)

func googleAnalyticsNode(ec *execCtx) ([][]Item, error) {
	h, err := ec.googleAuth("analytics", "googleAnalyticsOAuth2")
	if err != nil {
		return nil, err
	}
	base := ec.apiBase("analyticsdata", "https://analyticsdata.googleapis.com") + "/v1beta"
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOp(i, "report", "get")
		if r != "report" || o != "get" {
			return nil, unsupportedOp("Google Analytics", r, o)
		}
		if pt, _ := ec.str(i, "propertyType", "ga4"); pt != "ga4" {
			return nil, permanentError{"Google Analytics: Universal Analytics properties are retired; use a GA4 property"}
		}
		prop, _ := ec.rlString(i, "propertyId")
		start, end := gaDateRange(ec, i)
		names := func(key, field string) []interface{} {
			v, _ := ec.param(i, key+"."+field, nil)
			var out []interface{}
			for _, e := range asList(v) {
				m := asMap(e)
				n := toString(m["listName"])
				if n == "other" || n == "" {
					n = toString(m["name"])
				}
				if n != "" {
					out = append(out, map[string]interface{}{"name": n})
				}
			}
			return out
		}
		body := map[string]interface{}{
			"dateRanges": []interface{}{map[string]interface{}{"startDate": start, "endDate": end}},
			"metrics":    names("metricsGA4", "metricValues"),
			"dimensions": names("dimensionsGA4", "dimensionValues"),
		}
		af := fieldsOf(ec, i, "additionalFields")
		if cc := toString(af["currencyCode"]); cc != "" {
			body["currencyCode"] = cc
		}
		if toBool(af["keepEmptyRows"]) {
			body["keepEmptyRows"] = true
		}
		all, limit := pageCount(ec, i, 50)
		var rows []interface{}
		var res map[string]interface{}
		for offset := 0; offset < 1000000; {
			body["offset"] = offset
			body["limit"] = 10000
			if !all {
				body["limit"] = limit
			}
			res, _, err = ec.apiCall("POST", base+"/properties/"+url.PathEscape(strings.TrimPrefix(prop, "properties/"))+":runReport", h, body, "")
			if err != nil {
				return nil, err
			}
			page := asList(res["rows"])
			rows = append(rows, page...)
			offset += len(page)
			if !all || len(page) == 0 || offset >= int(toFloatDefault(res["rowCount"], 0)) {
				break
			}
		}
		if simple, _ := ec.boolean(i, "simple", true); !simple {
			res["rows"] = rows
			return one(res), nil
		}
		var dims, mets []string
		var types []string
		for _, d := range asList(res["dimensionHeaders"]) {
			dims = append(dims, toString(asMap(d)["name"]))
		}
		for _, m := range asList(res["metricHeaders"]) {
			mets = append(mets, toString(asMap(m)["name"]))
			types = append(types, toString(asMap(m)["type"]))
		}
		var out []Item
		for _, row := range rows {
			rm := asMap(row)
			j := map[string]interface{}{}
			for k, v := range asList(rm["dimensionValues"]) {
				if k < len(dims) {
					j[dims[k]] = asMap(v)["value"]
				}
			}
			for k, v := range asList(rm["metricValues"]) {
				if k < len(mets) {
					val := toString(asMap(v)["value"])
					if f, err := strconv.ParseFloat(val, 64); err == nil && types[k] != "TYPE_STRING" {
						j[mets[k]] = f
					} else {
						j[mets[k]] = val
					}
				}
			}
			out = append(out, Item{JSON: j})
		}
		return out, nil
	})
}

func gaDateRange(ec *execCtx, i int) (string, string) {
	now := time.Now().UTC()
	d := func(t time.Time) string { return t.Format("2006-01-02") }
	switch r, _ := ec.str(i, "dateRange", "last7days"); r {
	case "today":
		return "today", "today"
	case "yesterday":
		return "yesterday", "yesterday"
	case "last30days":
		return "30daysAgo", "yesterday"
	case "lastCalendarWeek":
		wd := int(now.Weekday())
		end := now.AddDate(0, 0, -wd-1)
		return d(end.AddDate(0, 0, -6)), d(end)
	case "lastCalendarMonth":
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return d(first.AddDate(0, -1, 0)), d(first.AddDate(0, 0, -1))
	case "custom":
		s, _ := ec.param(i, "startDate", "")
		e, _ := ec.param(i, "endDate", "")
		st, ok1 := timeOf(s)
		et, ok2 := timeOf(e)
		if ok1 && ok2 {
			return d(st), d(et)
		}
		return toString(s), toString(e)
	default:
		return "7daysAgo", "yesterday"
	}
}

// ---------------------------------------------------------------- S3 (generic, SigV4)

type s3Client struct {
	ec                           *execCtx
	endpoint                     *url.URL
	region, key, secret, session string
	pathStyle                    bool
}

func (ec *execCtx) s3Client(credType string) (*s3Client, error) {
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	region := firstNonEmpty(credString(c, "region"), "us-east-1")
	ep := credString(c, "endpoint")
	if toBool(c["customEndpoints"]) {
		ep = credString(c, "s3Endpoint")
	}
	if ep == "" {
		ep = "https://s3." + region + ".amazonaws.com"
	}
	if !strings.HasPrefix(ep, "http") {
		ep = "https://" + ep
	}
	u, err := url.Parse(strings.TrimRight(ep, "/"))
	if err != nil {
		return nil, permanentError{"S3: invalid endpoint"}
	}
	path := toBool(c["forcePathStyle"]) || !strings.HasSuffix(u.Hostname(), "amazonaws.com")
	return &s3Client{ec: ec, endpoint: u, region: region, key: credString(c, "accessKeyId"), secret: credString(c, "secretAccessKey"), session: credString(c, "sessionToken"), pathStyle: path}, nil
}

func (s *s3Client) do(method, bucket, key string, q url.Values, body []byte, headers map[string]string) (*rawResp, error) {
	u := *s.endpoint
	p := ""
	if bucket != "" {
		if s.pathStyle {
			p = "/" + bucket
		} else {
			u.Host = bucket + "." + u.Host
		}
	}
	if key != "" {
		p += "/" + escapePath(key)
	}
	if p == "" {
		p = "/"
	}
	u.Path, _ = url.PathUnescape(p)
	u.RawPath = p
	u.Opaque = ""
	if q == nil {
		q = url.Values{}
	}
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	sum := sha256.Sum256(body)
	payload := hex.EncodeToString(sum[:])
	h := map[string]string{"x-amz-date": amzDate, "x-amz-content-sha256": payload}
	if s.session != "" {
		h["x-amz-security-token"] = s.session
	}
	for k, v := range headers {
		h[strings.ToLower(k)] = v
	}
	signed := map[string]string{"host": u.Host}
	for k, v := range h {
		if strings.HasPrefix(k, "x-amz-") || k == "content-type" || k == "content-md5" {
			signed[k] = strings.TrimSpace(v)
		}
	}
	keys := make([]string, 0, len(signed))
	for k := range signed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canonHeaders strings.Builder
	for _, k := range keys {
		canonHeaders.WriteString(k + ":" + signed[k] + "\n")
	}
	signedNames := strings.Join(keys, ";")
	canon := strings.Join([]string{method, p, u.RawQuery, canonHeaders.String(), signedNames, payload}, "\n")
	scope := day + "/" + s.region + "/s3/aws4_request"
	csum := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(csum[:])
	mac := func(k []byte, d string) []byte { m := hmac.New(sha256.New, k); m.Write([]byte(d)); return m.Sum(nil) }
	kSign := mac(mac(mac(mac([]byte("AWS4"+s.secret), day), s.region), "s3"), "aws4_request")
	h["Authorization"] = "AWS4-HMAC-SHA256 Credential=" + s.key + "/" + scope + ", SignedHeaders=" + signedNames + ", Signature=" + hex.EncodeToString(mac(kSign, toSign))
	ct := h["content-type"]
	delete(h, "content-type")
	r, err := s.ec.doRequest(method, u.String(), h, body, ct)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func s3Node(ec *execCtx) ([][]Item, error) {
	s, err := ec.s3Client("s3")
	if err != nil {
		return nil, err
	}
	return s3Operations(ec, s)
}

// awsS3Node is n8n's AWS S3 node: the S3 operations with an AWS credential
// (an optional custom S3 endpoint).
func awsS3Node(ec *execCtx) ([][]Item, error) {
	s, err := ec.s3Client("aws")
	if err != nil {
		return nil, err
	}
	if u := ec.apiBase("awsS3", ""); u != "" {
		if s.endpoint, err = url.Parse(u); err != nil {
			return nil, err
		}
		s.pathStyle = true
	}
	return s3Operations(ec, s)
}

func s3Operations(ec *execCtx, s *s3Client) ([][]Item, error) {
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOp(i, "file", "download")
		bucket, _ := ec.str(i, "bucketName", "")
		switch r + "." + o {
		case "file.upload":
			name, _ := ec.str(i, "fileName", "")
			var data []byte
			ct := "text/plain"
			if bin, _ := ec.boolean(i, "binaryData", true); bin {
				prop, _ := ec.str(i, "binaryPropertyName", "data")
				d, m, fname, err := binaryOf(it, prop)
				if err != nil {
					return nil, err
				}
				data, ct = d, m
				if name == "" {
					name = fname
				}
			} else {
				c, _ := ec.str(i, "fileContent", "")
				data = []byte(c)
			}
			af := fieldsOf(ec, i, "additionalFields")
			if p := toString(af["parentFolderKey"]); p != "" {
				name = strings.TrimSuffix(p, "/") + "/" + name
			}
			h := map[string]string{"content-type": ct}
			for k, hdr := range map[string]string{"acl": "x-amz-acl", "storageClass": "x-amz-storage-class", "serverSideEncryption": "x-amz-server-side-encryption"} {
				if v := toString(af[k]); v != "" {
					h[hdr] = s3HeaderValue(k, v)
				}
			}
			if tags, _ := ec.param(i, "tagsUi.tagsValues", nil); len(asList(tags)) > 0 {
				tv := url.Values{}
				for _, t := range asList(tags) {
					tm := asMap(t)
					tv.Set(toString(tm["key"]), toString(tm["value"]))
				}
				h["x-amz-tagging"] = tv.Encode()
			}
			if _, err := s.do("PUT", bucket, name, nil, data, h); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "file.download":
			key, _ := ec.str(i, "fileKey", "")
			res, err := s.do("GET", bucket, key, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			prop, _ := ec.str(i, "binaryPropertyName", "data")
			name := key[strings.LastIndex(key, "/")+1:]
			mt := strings.Split(res.contentType, ";")[0]
			if mt == "" || mt == "binary/octet-stream" {
				mt = mimeByExt(name)
			}
			return []Item{{JSON: it.JSON, Binary: map[string]interface{}{prop: binaryData(res.body, mt, name)}}}, nil
		case "file.delete":
			key, _ := ec.str(i, "fileKey", "")
			if _, err := s.do("DELETE", bucket, key, nil, nil, nil); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "file.getAll", "folder.getAll":
			all, limit := pageCount(ec, i, 100)
			q := url.Values{"list-type": {"2"}}
			if p, _ := ec.str(i, "options.folderKey", ""); p != "" {
				q.Set("prefix", p)
			}
			var out []Item
			for page := 0; page < 1000; page++ {
				res, err := s.do("GET", bucket, "", q, nil, nil)
				if err != nil {
					return nil, err
				}
				var lr struct {
					Contents []struct {
						Key          string `xml:"Key"`
						LastModified string `xml:"LastModified"`
						ETag         string `xml:"ETag"`
						Size         int64  `xml:"Size"`
						StorageClass string `xml:"StorageClass"`
					} `xml:"Contents"`
					IsTruncated bool   `xml:"IsTruncated"`
					NextToken   string `xml:"NextContinuationToken"`
				}
				if err := xml.Unmarshal(res.body, &lr); err != nil {
					return nil, permanentError{"S3: invalid list response"}
				}
				for _, c := range lr.Contents {
					isFolder := strings.HasSuffix(c.Key, "/")
					if (o == "getAll" && r == "folder") != isFolder {
						continue
					}
					out = append(out, Item{JSON: map[string]interface{}{"Key": c.Key, "LastModified": c.LastModified, "ETag": c.ETag, "Size": float64(c.Size), "StorageClass": c.StorageClass}})
				}
				if !lr.IsTruncated || (!all && len(out) >= limit) {
					break
				}
				q.Set("continuation-token", lr.NextToken)
			}
			return capItems(out, all, limit), nil
		case "folder.create":
			name, _ := ec.str(i, "folderName", "")
			if p, _ := ec.str(i, "additionalFields.parentFolderKey", ""); p != "" {
				name = strings.TrimSuffix(p, "/") + "/" + name
			}
			if _, err := s.do("PUT", bucket, strings.TrimSuffix(name, "/")+"/", nil, []byte{}, nil); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "bucket.getAll":
			res, err := s.do("GET", "", "", nil, nil, nil)
			if err != nil {
				return nil, err
			}
			var lb struct {
				Buckets []struct {
					Name         string `xml:"Name"`
					CreationDate string `xml:"CreationDate"`
				} `xml:"Buckets>Bucket"`
			}
			_ = xml.Unmarshal(res.body, &lb)
			var out []Item
			for _, b := range lb.Buckets {
				out = append(out, Item{JSON: map[string]interface{}{"Name": b.Name, "CreationDate": b.CreationDate}})
			}
			return out, nil
		case "bucket.create":
			name, _ := ec.str(i, "name", "")
			if _, err := s.do("PUT", name, "", nil, []byte{}, nil); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "bucket.delete":
			name, _ := ec.str(i, "name", "")
			if _, err := s.do("DELETE", name, "", nil, nil, nil); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "file.copy":
			src, _ := ec.str(i, "sourcePath", "")
			dst, _ := ec.str(i, "destinationPath", "")
			db, dk, _ := strings.Cut(strings.TrimPrefix(dst, "/"), "/")
			if db == "" || dk == "" || strings.Count(strings.TrimPrefix(src, "/"), "/") < 1 {
				return nil, permanentError{"S3: source and destination paths must look like /bucket/key"}
			}
			h := map[string]string{"x-amz-copy-source": "/" + (&url.URL{Path: strings.TrimPrefix(src, "/")}).EscapedPath()}
			for k, v := range fieldsOf(ec, i, "additionalFields") {
				switch k {
				case "acl":
					h["x-amz-acl"] = s3HeaderValue(k, toString(v))
				case "storageClass":
					h["x-amz-storage-class"] = s3HeaderValue(k, toString(v))
				}
			}
			res, err := s.do("PUT", db, dk, nil, []byte{}, h)
			if err != nil {
				return nil, err
			}
			var cr struct {
				ETag         string `xml:"ETag"`
				LastModified string `xml:"LastModified"`
			}
			_ = xml.Unmarshal(res.body, &cr)
			return one(map[string]interface{}{"CopyObjectResult": map[string]interface{}{"ETag": cr.ETag, "LastModified": cr.LastModified}}), nil
		case "folder.delete":
			prefix, _ := ec.str(i, "folderKey", "")
			if prefix == "" {
				return nil, permanentError{"S3: folder key is required"}
			}
			prefix = strings.TrimSuffix(prefix, "/") + "/"
			q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
			for page := 0; page < 1000; page++ {
				res, err := s.do("GET", bucket, "", q, nil, nil)
				if err != nil {
					return nil, err
				}
				var lr struct {
					Keys        []string `xml:"Contents>Key"`
					IsTruncated bool     `xml:"IsTruncated"`
					NextToken   string   `xml:"NextContinuationToken"`
				}
				if err := xml.Unmarshal(res.body, &lr); err != nil {
					return nil, permanentError{"S3: invalid list response"}
				}
				for _, k := range lr.Keys {
					if _, err := s.do("DELETE", bucket, k, nil, nil, nil); err != nil {
						return nil, err
					}
				}
				if !lr.IsTruncated {
					break
				}
				q.Set("continuation-token", lr.NextToken)
			}
			return one(map[string]interface{}{"success": true}), nil
		}
		return nil, unsupportedOp("S3", r, o)
	})
}

func s3HeaderValue(k, v string) string {
	switch k {
	case "acl":
		m := map[string]string{"authenticatedRead": "authenticated-read", "awsExecRead": "aws-exec-read", "bucketOwnerFullControl": "bucket-owner-full-control", "bucketOwnerRead": "bucket-owner-read", "private": "private", "publicRead": "public-read", "publicReadWrite": "public-read-write"}
		if x, ok := m[v]; ok {
			return x
		}
	case "storageClass":
		m := map[string]string{"deepArchive": "DEEP_ARCHIVE", "glacier": "GLACIER", "intelligentTiering": "INTELLIGENT_TIERING", "onezoneIA": "ONEZONE_IA", "standard": "STANDARD", "standardIA": "STANDARD_IA", "reducedRedundancy": "REDUCED_REDUNDANCY"}
		if x, ok := m[v]; ok {
			return x
		}
	case "serverSideEncryption":
		if v == "AES256" || v == "aws:kms" {
			return v
		}
	}
	return v
}

// signJWT builds an HS256 JSON Web Token.
func signJWT(header, claims map[string]interface{}, secret []byte) (string, error) {
	enc := func(v interface{}) string { return base64.RawURLEncoding.EncodeToString([]byte(mustJSON(v))) }
	msg := enc(header) + "." + enc(claims)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(msg))
	return msg + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}
