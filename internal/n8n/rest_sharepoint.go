package n8n

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Microsoft SharePoint (n8n-nodes-base.microsoftSharePoint), version 1:
// SharePoint's Graph-compatible API at https://{subdomain}.sharepoint.com/_api/v2.0.

func init() {
	registerREST("microsoftSharePoint", &restService{
		defResource: "file",
		defOps:      map[string]string{"file": "download", "item": "getAll", "list": "getAll"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("microsoftSharePointOAuth2Api")
			if err != nil {
				return nil, err
			}
			sub := strings.TrimSpace(credString(c, "subdomain"))
			base := ec.apiBase("microsoftSharePoint", "")
			if base == "" {
				if sub == "" {
					return nil, permanentError{ec.node.Name + ": The SharePoint subdomain is not set"}
				}
				base = "https://" + sub + ".sharepoint.com/_api/v2.0"
			}
			return &restClient{base: base, headers: map[string]string{"Authorization": "Bearer " + accessToken(c)}}, nil
		},
		pager:   spNextLink,
		listKey: "value",
		ops: map[string]*restOp{
			"file/download": {run: spDownload},
			"file/upload":   {method: "PUT", path: "/sites/{site}/drive/items/{folder}:/{fileName}:/content", build: spBinaryBody},
			"file/update":   {run: spFileUpdate},
			"item/create":   {run: spItemWrite("create")},
			"item/update":   {run: spItemWrite("update")},
			"item/upsert":   {run: spItemWrite("upsert")},
			"item/delete": {method: "DELETE", path: "/sites/{site}/lists/{list}/items/{item}",
				post: func(*execCtx, int, interface{}) (interface{}, error) {
					return map[string]interface{}{"deleted": true}, nil
				}},
			"item/get": {path: "/sites/{site}/lists/{list}/items/{item}", build: spItemSelect, post: spSimplify(true)},
			"item/getAll": {path: "/sites/{site}/lists/{list}/items", list: true, sizeParam: "$top", query: "filter:$filter",
				build: spItemSelect, post: spSimplify(true)},
			"list/get":    {path: "/sites/{site}/lists/{list}", build: spListSelect, post: spSimplify(false)},
			"list/getAll": {path: "/sites/{site}/lists", list: true, sizeParam: "$top", build: spListSelect, post: spSimplify(false)},
		},
	})
}

// spNextLink follows @odata.nextLink (the key has a dot, so no jpath).
func spNextLink(p *restPage, res interface{}, _ http.Header) bool {
	u := toString(asMap(res)["@odata.nextLink"])
	if u == "" || u == "<nil>" {
		return false
	}
	p.url = u
	return true
}

func spItemSelect(ec *execCtx, i int, r *restRequest) error {
	if s, _ := ec.boolean(i, "simplify", true); s {
		r.query.Set("$select", "id,createdDateTime,lastModifiedDateTime,webUrl")
		r.query.Set("$expand", "fields(select=Title)")
		return nil
	}
	v, _ := ec.param(i, "options.fields", nil)
	var fields []string
	for _, f := range asListOrOne(v) {
		if s := toString(f); s != "" {
			fields = append(fields, s)
		}
	}
	if len(fields) > 0 {
		for _, f := range fields {
			if f == "fields" {
				r.query.Set("$expand", "fields")
			}
		}
		r.query.Set("$select", strings.Join(fields, ","))
	}
	return nil
}

func spListSelect(ec *execCtx, i int, r *restRequest) error {
	if s, _ := ec.boolean(i, "simplify", true); s {
		r.query.Set("$select", "id,name,displayName,description,createdDateTime,lastModifiedDateTime,webUrl")
	}
	return nil
}

// spSimplify drops the OData annotations when simplify is on.
func spSimplify(item bool) func(ec *execCtx, i int, v interface{}) (interface{}, error) {
	return func(ec *execCtx, i int, v interface{}) (interface{}, error) {
		if s, _ := ec.boolean(i, "simplify", true); !s {
			return v, nil
		}
		for _, x := range asListOrOne(v) {
			m := asMap(x)
			delete(m, "@odata.context")
			delete(m, "@odata.etag")
			if item {
				delete(m, "fields@odata.navigationLink")
				delete(asMap(m["fields"]), "@odata.etag")
			}
		}
		return v, nil
	}
}

// spBinaryBody sends the input item's binary property named by fileContents.
func spBinaryBody(ec *execCtx, i int, r *restRequest) error {
	prop, _ := ec.str(i, "fileContents", "data")
	var it Item
	if items := ec.items(); i < len(items) {
		it = items[i]
	}
	data, mt, _, err := binaryOf(it, strings.TrimSpace(prop))
	if err != nil {
		return permanentError{ec.node.Name + ": " + err.Error()}
	}
	r.raw, r.contentType = data, mt
	return nil
}

var spFileNameRe = regexp.MustCompile(`filename\*=(?:[^\s']*'[^']*')?([^;\n]*)|filename="?([^";]*)"?`)

func spDownload(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/sites/{site}/drive/items/{file}/content")
	if err != nil {
		return nil, err
	}
	res, err := restRaw(ec, c, "GET", c.base+path)
	if err != nil {
		return nil, err
	}
	name := ""
	if m := spFileNameRe.FindStringSubmatch(res.header.Get("Content-Disposition")); m != nil {
		if m[1] != "" {
			name, _ = url.PathUnescape(strings.Trim(m[1], `"`))
		} else {
			name = m[2]
		}
	}
	return []Item{{JSON: map[string]interface{}{}, Binary: map[string]interface{}{"data": binaryData(res.body, res.contentType, name)}}}, nil
}

// restRaw sends an authenticated request with the client's headers and
// returns the raw response (downloads).
func restRaw(ec *execCtx, c *restClient, method, u string) (rawResp, error) {
	h := map[string]string{}
	for k, v := range c.headers {
		h[k] = v
	}
	return ec.doRequest(method, u, h, nil, "")
}

func spFileUpdate(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/sites/{site}/drive/items/{file}")
	if err != nil {
		return nil, err
	}
	r := &restRequest{method: "PATCH", path: path, query: url.Values{}, body: map[string]interface{}{}}
	if n, _ := ec.str(i, "fileName", ""); n != "" {
		r.body["name"] = n
	}
	v, err := c.do(ec, r)
	if err != nil {
		return nil, err
	}
	if ch, _ := ec.boolean(i, "changeFileContent", false); ch {
		r = &restRequest{method: "PUT", path: path + "/content", query: url.Values{}}
		if err := spBinaryBody(ec, i, r); err != nil {
			return nil, err
		}
		if v, err = c.do(ec, r); err != nil {
			return nil, err
		}
	}
	return restItems(v), nil
}

// spItemWrite creates, updates or upserts a list item from the columns
// resource mapper; update and upsert find the item by the matching columns.
func spItemWrite(op string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		path, err := ec.restPath(i, "/sites/{site}/lists/{list}/items")
		if err != nil {
			return nil, err
		}
		colsV, err := ec.param(i, "columns", nil)
		if err != nil {
			return nil, err
		}
		cols := asMap(colsV)
		schema := asList(cols["schema"])
		value := map[string]interface{}{}
		if toString(cols["mappingMode"]) == "autoMapInputData" {
			ids := map[string]bool{}
			for _, s := range schema {
				m := asMap(s)
				if !toBool(m["readOnly"]) || toString(m["id"]) == "id" {
					ids[toString(m["id"])] = true
				}
			}
			if len(ids) == 0 {
				return nil, permanentError{ec.node.Name + ": Could not map the input data to the list columns"}
			}
			for k, v := range it.JSON {
				if ids[k] {
					value[k] = v
				}
			}
		} else {
			for k, v := range asMap(cols["value"]) {
				value[k] = v
			}
		}
		method := "POST"
		if op != "create" {
			var match []string
			for _, m := range asList(cols["matchingColumns"]) {
				match = append(match, toString(m))
			}
			if op == "update" {
				method = "PATCH"
			}
			if len(match) > 0 {
				hasID := false
				var conds []string
				for _, m := range match {
					hasID = hasID || m == "id"
					conds = append(conds, "fields/"+m+" eq '"+strings.ReplaceAll(toString(value[m]), "'", "''")+"'")
				}
				if !hasID {
					r := &restRequest{method: "GET", path: path, query: url.Values{"$filter": {strings.Join(conds, " and ")}},
						headers: map[string]string{"Prefer": "HonorNonIndexedQueriesWarningMayFailRandomly"}}
					res, err := c.do(ec, r)
					if err != nil {
						return nil, err
					}
					if l := asList(asMap(res)["value"]); len(l) == 1 {
						hasID = true
						value["id"] = asMap(l[0])["id"]
					}
				}
				id := toString(value["id"])
				if hasID && id != "" && id != "<nil>" {
					path += "/" + url.PathEscape(id)
					delete(value, "id")
					method = "PATCH"
				} else if op == "update" || hasID {
					return nil, permanentError{ec.node.Name + ": The column(s) don't match any existing item"}
				}
			}
		}
		fields := map[string]interface{}{}
		for k, v := range value {
			fields[k] = v
			for _, s := range schema {
				if m := asMap(s); toString(m["id"]) == k && toString(m["type"]) == "url" {
					fields[k] = map[string]interface{}{"Description": v, "Url": v}
				}
			}
		}
		res, err := c.do(ec, &restRequest{method: method, path: path, query: url.Values{}, body: map[string]interface{}{"fields": fields}})
		if err != nil {
			return nil, err
		}
		return restItems(res), nil
	}
}
