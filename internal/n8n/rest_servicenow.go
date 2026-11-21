package n8n

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ServiceNow (n8n-nodes-base.serviceNow): the Table and Attachment APIs at
// https://{subdomain}.service-now.com/api. Results are unwrapped from
// "result"; returnAll pages by sysparm_offset up to X-Total-Count.

func init() {
	table := func(t string) map[string]*restOp {
		return map[string]*restOp{
			"getAll": {path: "/now/table/" + t, list: true, listKey: "result", sizeParam: "sysparm_limit", query: "@options *", build: snQuery},
		}
	}
	ops := map[string]*restOp{
		"incident/create": {method: "POST", path: "/now/table/incident", out: "result", body: "short_description @additionalFields *"},
		"incident/delete": {method: "DELETE", path: "/now/table/incident/{id}", success: true},
		"incident/get":    {path: "/now/table/incident/{id}", out: "result", query: "@options *", build: snQuery},
		"incident/update": {method: "PATCH", path: "/now/table/incident/{id}", out: "result", body: "@updateFields *"},

		"tableRecord/create": {method: "POST", path: "/now/table/{tableName}", out: "result", build: snRecordBody},
		"tableRecord/delete": {method: "DELETE", path: "/now/table/{tableName}/{id}", success: true},
		"tableRecord/get":    {path: "/now/table/{tableName}/{id}", out: "result", query: "@options *", build: snQuery},
		"tableRecord/getAll": {path: "/now/table/{tableName}", list: true, listKey: "result", sizeParam: "sysparm_limit", query: "@options *", build: snQuery},
		"tableRecord/update": {method: "PATCH", path: "/now/table/{tableName}/{id}", out: "result", build: snRecordBody},

		"user/create": {method: "POST", path: "/now/table/sys_user", out: "result", body: "@additionalFields *"},
		"user/delete": {method: "DELETE", path: "/now/table/sys_user/{id}", success: true},
		"user/get": {path: "/now/table/sys_user", out: "result", query: "@options *", build: func(ec *execCtx, i int, r *restRequest) error {
			if err := snQuery(ec, i, r); err != nil {
				return err
			}
			if by, _ := ec.str(i, "getOption", "id"); by == "id" {
				p, err := ec.restPath(i, "/now/table/sys_user/{id}")
				r.path = p
				return err
			}
			name, err := ec.str(i, "user_name", "")
			if err != nil {
				return err
			}
			if err := snNoDelims(ec, "Username", name); err != nil {
				return err
			}
			r.query.Set("sysparm_query", "user_name="+name)
			r.query.Set("sysparm_limit", "1")
			return nil
		}},
		"user/update": {method: "PATCH", path: "/now/table/sys_user/{id}", out: "result", body: "@updateFields *"},

		"attachment/get":    {run: snAttachmentGet},
		"attachment/getAll": {run: snAttachmentGetAll},
		"attachment/upload": {run: snAttachmentUpload},
		"attachment/delete": {method: "DELETE", path: "/now/attachment/{attachmentId}", success: true},
	}
	for r, t := range map[string]string{"incident": "incident", "user": "sys_user", "businessService": "cmdb_ci_service",
		"configurationItems": "cmdb_ci", "department": "cmn_department", "dictionary": "sys_dictionary",
		"userGroup": "sys_user_group", "userRole": "sys_user_role"} {
		for o, op := range table(t) {
			ops[r+"/"+o] = op
		}
	}
	registerREST("serviceNow", &restService{
		defResource: "user",
		defOps: map[string]string{"attachment": "upload", "businessService": "getAll", "configurationItems": "getAll",
			"department": "getAll", "dictionary": "getAll", "incident": "get", "tableRecord": "get", "user": "get",
			"userGroup": "getAll", "userRole": "getAll"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, t, err := ec.restCred(i, "serviceNowOAuth2Api", map[string]string{"basicAuth": "serviceNowBasicApi", "oAuth2": "serviceNowOAuth2Api"})
			if err != nil {
				return nil, err
			}
			base := ec.apiBase("serviceNow", "")
			if base == "" {
				sub := strings.TrimSpace(credString(c, "subdomain"))
				if sub == "" {
					return nil, permanentError{ec.node.Name + ": the ServiceNow subdomain is not set"}
				}
				base = "https://" + sub + ".service-now.com/api"
			}
			h := map[string]string{"Authorization": "Bearer " + accessToken(c)}
			if t == "serviceNowBasicApi" {
				h = basicAuth(credString(c, "user"), credString(c, "password"))
			}
			return &restClient{base: base, headers: h}, nil
		},
		pager: snPager,
		ops:   ops,
	})
}

const snPageSize = 100

// snQuery joins sysparm_fields lists and pages returnAll lists by 100.
func snQuery(ec *execCtx, i int, r *restRequest) error {
	if f := r.query["sysparm_fields"]; len(f) > 1 {
		r.query.Set("sysparm_fields", strings.Join(f, ","))
	}
	if tn, ok := ec.raw("tableName"); ok && tn != nil {
		name, err := ec.str(i, "tableName", "")
		if err != nil {
			return err
		}
		if err := snNoDelims(ec, "Table Name", name); err != nil {
			return err
		}
	}
	if all, _ := ec.boolean(i, "returnAll", false); all {
		r.query.Set("sysparm_limit", strconv.Itoa(snPageSize))
	}
	return nil
}

// snPager fetches the next offset page while records remain (X-Total-Count).
func snPager(p *restPage, _ interface{}, hdr http.Header) bool {
	total, err := strconv.Atoi(hdr.Get("X-Total-Count"))
	if err != nil || p.got >= total {
		return false
	}
	p.query.Set("sysparm_limit", strconv.Itoa(snPageSize))
	p.query.Set("sysparm_offset", strconv.Itoa(p.got))
	return true
}

// snNoDelims rejects values that would break an encoded query (n8n's
// assertNoQueryDelimiters).
func snNoDelims(ec *execCtx, what, v string) error {
	if strings.Contains(v, "^") {
		return permanentError{ec.node.Name + ": " + what + ` cannot contain "^"`}
	}
	return nil
}

// snRecordBody builds a table record from the input item or from columns.
func snRecordBody(ec *execCtx, i int, r *restRequest) error {
	switch mode, _ := ec.str(i, "dataToSend", "columns"); mode {
	case "mapInput":
		ignore, err := ec.str(i, "inputsToIgnore", "")
		if err != nil {
			return err
		}
		skip := map[string]bool{}
		for _, k := range strings.Split(ignore, ",") {
			skip[strings.TrimSpace(k)] = true
		}
		if items := ec.items(); i < len(items) {
			for k, v := range items[i].JSON {
				if !skip[k] {
					r.body[k] = v
				}
			}
		}
	case "columns":
		v, err := ec.param(i, "fieldsToSend.field", nil)
		if err != nil {
			return err
		}
		for _, f := range asListOrOne(v) {
			if m := asMap(f); toString(m["column"]) != "" {
				r.body[toString(m["column"])] = m["value"]
			}
		}
	}
	return nil
}

func snAttachmentGet(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/now/attachment/{attachmentId}")
	if err != nil {
		return nil, err
	}
	v, err := cmtyCall(ec, c, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	meta := asMap(jpath(v, "result"))
	it := Item{JSON: meta}
	if dl, _ := ec.boolean(i, "download", false); dl {
		if it, err = snWithFile(ec, i, c, meta); err != nil {
			return nil, err
		}
	}
	return []Item{it}, nil
}

func snAttachmentGetAll(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	table, err := ec.str(i, "tableName", "")
	if err != nil {
		return nil, err
	}
	if err := snNoDelims(ec, "Table Name", table); err != nil {
		return nil, err
	}
	q := "table_name=" + table
	if f, _ := ec.str(i, "options.queryFilter", ""); f != "" {
		q += "^" + f
	}
	all, _ := ec.boolean(i, "returnAll", false)
	limit, _ := ec.num(i, "limit", 50)
	var records []interface{}
	for off := 0; ; off += snPageSize {
		qs := url.Values{"sysparm_query": {q}}
		if all {
			qs.Set("sysparm_limit", strconv.Itoa(snPageSize))
			if off > 0 {
				qs.Set("sysparm_offset", strconv.Itoa(off))
			}
		} else {
			qs.Set("sysparm_limit", strconv.Itoa(int(limit)))
		}
		v, hdr, err := c.doH(ec, &restRequest{method: "GET", path: "/now/attachment", query: qs}, "")
		if err != nil {
			return nil, err
		}
		batch := asList(jpath(v, "result"))
		records = append(records, batch...)
		total, _ := strconv.Atoi(hdr.Get("X-Total-Count"))
		if !all || len(batch) == 0 || len(records) >= total {
			break
		}
	}
	dl, _ := ec.boolean(i, "download", false)
	out := make([]Item, 0, len(records))
	for _, r := range records {
		it := Item{JSON: asMap(r)}
		if dl {
			if it, err = snWithFile(ec, i, c, asMap(r)); err != nil {
				return nil, err
			}
		}
		out = append(out, it)
	}
	return out, nil
}

// snWithFile downloads an attachment's content into the outputField binary.
func snWithFile(ec *execCtx, i int, c *restClient, meta map[string]interface{}) (Item, error) {
	prop, _ := ec.str(i, "outputField", "data")
	res, err := restRaw(ec, c, "GET", c.base+"/now/attachment/"+url.PathEscape(toString(meta["sys_id"]))+"/file")
	if err != nil {
		return Item{}, err
	}
	return cnBinaryItem(meta, prop, res.body, toString(meta["content_type"]), toString(meta["file_name"])), nil
}

func snAttachmentUpload(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	table, err := ec.str(i, "tableName", "")
	if err != nil {
		return nil, err
	}
	id, err := ec.str(i, "id", "")
	if err != nil {
		return nil, err
	}
	prop, _ := ec.str(i, "inputDataFieldName", "data")
	data, mt, name, err := binaryOf(it, prop)
	if err != nil {
		return nil, permanentError{ec.node.Name + ": " + err.Error()}
	}
	if b := asMap(it.Binary[prop]); toString(b["fileName"]) == "" {
		name = prop + "." + toString(b["fileExtension"])
	}
	q := url.Values{"table_name": {table}, "table_sys_id": {id}, "file_name": {name}}
	for k, v := range fieldsOf(ec, i, "options") {
		if !restEmpty(v) {
			q.Set(k, toString(v))
		}
	}
	v, err := c.do(ec, &restRequest{method: "POST", path: "/now/attachment/file", query: q, raw: data, contentType: mt})
	if err != nil {
		return nil, err
	}
	return restItems(jpath(v, "result")), nil
}
