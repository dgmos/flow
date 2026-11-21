package n8n

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Pipedrive (n8n-nodes-base.pipedrive). Version 1 of the node talks to the
// v1 API; version 2 uses API v2 where Pipedrive has it (activities, deals,
// deal products, organizations, persons, products) and v1 for the rest.
// Both versions are tables below; the registered ops dispatch on the node
// version.

var pdTables [2]map[string]*restOp // [0] node v1, [1] node v2

var pdDefOps = [2]map[string]string{
	{"activity": "create", "deal": "create", "dealActivity": "getAll", "dealProduct": "add", "file": "create", "lead": "create",
		"note": "create", "organization": "create", "person": "create", "product": "getAll"},
	{"activity": "create", "deal": "create", "dealProduct": "add", "file": "create", "lead": "create",
		"note": "create", "organization": "create", "person": "create", "product": "create"},
}

// pdExec runs table ops; its defaults are those of v1 list calls.
var pdExec = &restService{listKey: "data"}

func init() {
	pdTables[0] = pipedriveV1Ops()
	pdTables[1] = pipedriveV2Ops()
	ops := map[string]*restOp{}
	for _, t := range pdTables {
		for k := range t {
			ops[k] = &restOp{run: pdDispatch}
		}
	}
	registerREST("pipedrive", &restService{
		defResource: "deal",
		defOps:      pdDefOps[0],
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, t, err := ec.restCred(i, "pipedriveApi", map[string]string{"apiToken": "pipedriveApi", "oAuth2": "pipedriveOAuth2Api"})
			if err != nil {
				return nil, err
			}
			base := ec.apiBase("pipedrive", "https://api.pipedrive.com")
			if t == "pipedriveApi" {
				if tok := credString(c, "apiToken"); tok != "" {
					return &restClient{base: base, query: url.Values{"api_token": {tok}}, headers: map[string]string{}}, nil
				}
			}
			return bearerClient(base, c), nil
		},
		ops: ops,
	})
}

// pdDispatch picks the op of the node's version (defaults differ: product
// is getAll in v1 and create in v2).
func pdDispatch(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	v := 0
	if ec.version >= 2 {
		v = 1
	}
	r, o := ec.resourceOpBy(i, "deal", pdDefOps[v])
	op := pdTables[v][r+"/"+o]
	if op == nil {
		return nil, unsupportedOp(ec.node.Name, r, o)
	}
	if op.run != nil {
		return op.run(ec, i, it, c)
	}
	return pdExec.exec(ec, i, op, c)
}

// pdWrap sets pdNoData on single-record ops and wraps the custom-field
// ops of resources with custom fields.
func pdWrap(ops map[string]*restOp, operations []string, wrap func(r, o string, op *restOp) *restOp) map[string]*restOp {
	for _, op := range ops {
		if !op.list && !op.success && op.run == nil && op.post == nil {
			op.post = pdNoData
		}
	}
	for _, r := range []string{"activity", "deal", "organization", "person", "product"} {
		for _, o := range operations {
			if op := ops[r+"/"+o]; op != nil {
				ops[r+"/"+o] = wrap(r, o, op)
			}
		}
	}
	return ops
}

// pdNoData: n8n outputs nothing for "data": null.
func pdNoData(_ *execCtx, _ int, v interface{}) (interface{}, error) {
	if v == nil {
		return []interface{}{}, nil
	}
	return v, nil
}

// pdOffsetPager follows additional_data.pagination (v1).
func pdOffsetPager(p *restPage, res interface{}, _ http.Header) bool {
	pg := asMap(jpath(res, "additional_data.pagination"))
	if !toBool(pg["more_items_in_collection"]) {
		return false
	}
	p.query.Set("start", toString(pg["next_start"]))
	return true
}

var pdCursorPager = cursorPager("additional_data.next_cursor", "cursor")

// pdAddFields copies a collection like n8n's addAdditionalFields (v1:
// customProperties.property pairs become top-level keys) or addFieldsToBody
// (v2: customFields.property pairs go to custom_fields).
func pdAddFields(dst, fields map[string]interface{}, v2 bool) {
	for k, v := range fields {
		switch {
		case !v2 && k == "customProperties" && asMap(v)["property"] != nil:
			for _, p := range asList(asMap(v)["property"]) {
				m := asMap(p)
				dst[toString(m["name"])] = m["value"]
			}
		case v2 && k == "customFields" && asMap(v)["property"] != nil:
			cf := asMap(dst["custom_fields"])
			if cf == nil {
				cf = map[string]interface{}{}
			}
			for _, p := range asList(asMap(v)["property"]) {
				m := asMap(p)
				cf[toString(m["name"])] = m["value"]
			}
			dst["custom_fields"] = cf
		default:
			dst[k] = v
		}
	}
	if v2 {
		if vt, ok := dst["visible_to"]; ok {
			if f, ok := toFloat(vt); ok {
				dst["visible_to"] = f
			}
		}
	}
}

// pdBody builds a body from a collection plus a fix-up.
func pdBody(coll string, v2 bool, fix func(ec *execCtx, i int, b map[string]interface{}) error) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if coll != "" {
			pdAddFields(r.body, fieldsOf(ec, i, coll), v2)
		}
		if fix != nil {
			return fix(ec, i, r.body)
		}
		return nil
	}
}

// pdQuery copies a collection into the query string, with fix-ups.
func pdQuery(coll string, fix func(ec *execCtx, i int, q map[string]interface{})) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		q := map[string]interface{}{}
		if coll != "" {
			pdAddFields(q, fieldsOf(ec, i, coll), false)
		}
		if fix != nil {
			fix(ec, i, q)
		}
		for k, v := range q {
			if v == nil {
				continue
			}
			r.query.Del(k)
			for _, s := range queryValues(v) {
				r.query.Add(k, s)
			}
		}
		return nil
	}
}

func rename(m map[string]interface{}, from, to string) {
	if v, ok := m[from]; ok {
		delete(m, from)
		if !restEmpty(v) {
			m[to] = v
		}
	}
}

func joinList(v interface{}) string {
	var parts []string
	for _, x := range asListOrOne(v) {
		parts = append(parts, toString(x))
	}
	return strings.Join(parts, ",")
}

// pdAssociate sets org_id/organization_id or person_id per associateWith.
func pdAssociate(orgKey string) func(ec *execCtx, i int, b map[string]interface{}) error {
	return func(ec *execCtx, i int, b map[string]interface{}) error {
		if a, _ := ec.str(i, "associateWith", "organization"); a == "organization" {
			b[orgKey], _ = ec.param(i, orgKey, nil)
		} else {
			b["person_id"], _ = ec.param(i, "person_id", nil)
		}
		return nil
	}
}

func pdLabelNull(_ *execCtx, _ int, b map[string]interface{}) error {
	if b["label"] == "null" {
		b["label"] = nil
	}
	return nil
}

// pdLead applies lead fields: value.valueProperties → value, date-only
// expected_close_date.
func pdLead(coll string, assoc bool) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if assoc {
			if err := pdAssociate("organization_id")(ec, i, r.body); err != nil {
				return err
			}
		}
		for k, v := range fieldsOf(ec, i, coll) {
			switch k {
			case "value":
				if vp, ok := asMap(v)["valueProperties"]; ok {
					r.body["value"] = vp
				}
			case "expected_close_date":
				if s := toString(v); s != "" {
					r.body[k] = strings.Split(s, "T")[0]
				}
			default:
				r.body[k] = v
			}
		}
		return nil
	}
}

// pdSearchPost flattens {result_score, item} search results.
func pdSearchPost(v1 bool) func(ec *execCtx, i int, v interface{}) (interface{}, error) {
	return func(ec *execCtx, i int, v interface{}) (interface{}, error) {
		if v1 {
			// n8n v1 leaves returnAll results and rawData raw.
			all, _ := ec.boolean(i, "returnAll", false)
			if all || toBool(fieldsOf(ec, i, "additionalFields")["rawData"]) {
				return v, nil
			}
		}
		out := []interface{}{}
		for _, x := range asList(v) {
			m := asMap(x)
			if it, ok := m["item"]; ok {
				f := map[string]interface{}{"result_score": m["result_score"]}
				for k, y := range asMap(it) {
					f[k] = y
				}
				out = append(out, f)
			} else {
				out = append(out, x)
			}
		}
		return out, nil
	}
}

// pdSearch builds a search op on the v1 endpoint.
func pdSearch(path string, v1 bool, fix func(ec *execCtx, i int, q map[string]interface{})) *restOp {
	return &restOp{path: path, list: true, listKey: "data.items", pager: pdOffsetPager, sizeParam: "limit", maxSize: 100,
		query: "term", post: pdSearchPost(v1), build: func(ec *execCtx, i int, r *restRequest) error {
			af := fieldsOf(ec, i, "additionalFields")
			q := map[string]interface{}{}
			if em, err := ec.param(i, "exactMatch", nil); err == nil && em != nil {
				q["exact_match"] = toBool(em)
			} else if toBool(af["exactMatch"]) {
				q["exact_match"] = true
			}
			if f := af["fields"]; !restEmpty(f) {
				q["fields"] = joinList(f)
			}
			if s := toString(af["organizationId"]); s != "" {
				q["organization_id"], _ = strconv.Atoi(s)
			}
			if s := toString(af["personId"]); s != "" {
				q["person_id"], _ = strconv.Atoi(s)
			}
			if s := toString(af["includeFields"]); s != "" {
				q["include_fields"] = s
			}
			if fix != nil {
				fix(ec, i, q)
			}
			for k, v := range q {
				r.query.Set(k, toString(v))
			}
			return nil
		}}
}

func pdFileCreate(prefix string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		prop, _ := ec.str(i, "binaryPropertyName", "data")
		data, mt, name, err := binaryOf(it, prop)
		if err != nil {
			return nil, err
		}
		fields := map[string]string{}
		for k, v := range fieldsOf(ec, i, "additionalFields") {
			fields[k] = toString(v)
		}
		body, ct := multipartBody(fields, "file", name, mt, data)
		v, err := c.do(ec, &restRequest{method: "POST", path: prefix + "/files", query: url.Values{}, raw: body, contentType: ct})
		if err != nil {
			return nil, err
		}
		return restItems(jpath(v, "data")), nil
	}
}

// pdFileDownload returns the input item with the file as binary data.
func pdFileDownload(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/v1/files/{fileId}/download")
	if err != nil {
		return nil, err
	}
	u := c.base + path
	if len(c.query) > 0 {
		u += "?" + c.query.Encode()
	}
	res, err := ec.doRequest("GET", u, c.headers, nil, "")
	if err != nil {
		return nil, err
	}
	prop, _ := ec.str(i, "binaryPropertyName", "data")
	out := Item{JSON: it.JSON, Binary: map[string]interface{}{}}
	if out.JSON == nil {
		out.JSON = map[string]interface{}{}
	}
	for k, v := range it.Binary {
		out.Binary[k] = v
	}
	mt := res.contentType
	if k := strings.Index(mt, ";"); k >= 0 {
		mt = mt[:k]
	}
	out.Binary[firstNonEmpty(prop, "data")] = binaryData(res.body, firstNonEmpty(mt, "application/octet-stream"), "")
	return []Item{out}, nil
}

// ---------------------------------------------------------------- v1

func pipedriveV1Ops() map[string]*restOp {
	list := func(path string, build func(ec *execCtx, i int, r *restRequest) error) *restOp {
		return &restOp{path: path, list: true, pager: pdOffsetPager, sizeParam: "limit", maxSize: 100, build: build}
	}
	ops := map[string]*restOp{
		"activity/create": {method: "POST", path: "/v1/activities", body: "subject done type", build: pdBody("additionalFields", false, nil)},
		"activity/delete": {method: "DELETE", path: "/v1/activities/{activityId}", out: "data"},
		"activity/get":    {path: "/v1/activities/{activityId}", out: "data"},
		"activity/getAll": list("/v1/activities", pdQuery("additionalFields", func(_ *execCtx, _ int, q map[string]interface{}) {
			rename(q, "filterId", "filter_id")
			if t, ok := q["type"]; ok {
				q["type"] = joinList(t)
			}
		})),
		"activity/update": {method: "PUT", path: "/v1/activities/{activityId}", out: "data", build: pdBody("updateFields", false, nil)},

		"deal/create":    {method: "POST", path: "/v1/deals", out: "data", body: "title", build: pdBody("additionalFields", false, pdAssociate("org_id"))},
		"deal/delete":    {method: "DELETE", path: "/v1/deals/{dealId}", out: "data"},
		"deal/duplicate": {method: "POST", path: "/v1/deals/{dealId}/duplicate", out: "data"},
		"deal/get":       {path: "/v1/deals/{dealId}", out: "data"},
		"deal/getAll":    list("/v1/deals", pdQuery("filters", nil)),
		"deal/update":    {method: "PUT", path: "/v1/deals/{dealId}", out: "data", build: pdBody("updateFields", false, pdLabelNull)},
		"deal/search": pdSearch("/v1/deals/search", true, func(ec *execCtx, i int, q map[string]interface{}) {
			if s := toString(fieldsOf(ec, i, "additionalFields")["status"]); s != "" {
				q["status"] = s
			}
		}),

		"dealActivity/getAll": list("/v1/deals/{dealId}/activities", func(ec *execCtx, i int, r *restRequest) error {
			af := fieldsOf(ec, i, "additionalFields")
			if s := toString(af["exclude"]); s != "" {
				r.query.Set("exclude", s)
			}
			if d, ok := af["done"]; ok && d != nil {
				r.query.Set("done", map[bool]string{true: "1", false: "0"}[d == true])
			}
			return nil
		}),

		"dealProduct/add": {method: "POST", path: "/v1/deals/{dealId}/products", out: "data", body: "productId:product_id item_price quantity",
			build: pdBody("additionalFields", false, nil)},
		"dealProduct/getAll": {path: "/v1/deals/{dealId}/products", out: "data"},
		"dealProduct/remove": {method: "DELETE", path: "/v1/deals/{dealId}/products/{productAttachmentId}", out: "data"},
		"dealProduct/update": {method: "PUT", path: "/v1/deals/{dealId}/products/{productAttachmentId}", out: "data", build: pdBody("updateFields", false, nil)},

		"file/create":   {run: pdFileCreate("/v1")},
		"file/delete":   {method: "DELETE", path: "/v1/files/{fileId}", out: "data"},
		"file/download": {run: pdFileDownload},
		"file/get":      {path: "/v1/files/{fileId}", out: "data"},
		"file/update":   {method: "PUT", path: "/v1/files/{fileId}", out: "data", build: pdBody("updateFields", false, nil)},

		"note/create": {method: "POST", path: "/v1/notes", out: "data", body: "content", build: pdBody("additionalFields", false, nil)},
		"note/delete": {method: "DELETE", path: "/v1/notes/{noteId}", out: "data"},
		"note/get":    {path: "/v1/notes/{noteId}", out: "data"},
		"note/getAll": list("/v1/notes", pdQuery("additionalFields", nil)),
		"note/update": {method: "PUT", path: "/v1/notes/{noteId}", out: "data", build: pdBody("updateFields", false, nil)},

		"organization/create": {method: "POST", path: "/v1/organizations", out: "data", body: "name", build: pdBody("additionalFields", false, nil)},
		"organization/delete": {method: "DELETE", path: "/v1/organizations/{organizationId}", out: "data"},
		"organization/get":    {path: "/v1/organizations/{organizationId}", out: "data"},
		"organization/getAll": list("/v1/organizations", pdQuery("", func(ec *execCtx, i int, q map[string]interface{}) {
			f := fieldsOf(ec, i, "filters")
			if s := toString(f["filterId"]); s != "" {
				q["filter_id"] = s
			}
			if s := toString(f["firstChar"]); s != "" {
				q["first_char"] = s[:1]
			}
		})),
		"organization/update": {method: "PUT", path: "/v1/organizations/{organizationId}", out: "data", build: pdBody("updateFields", false, pdLabelNull)},
		"organization/search": pdSearch("/v1/organizations/search", true, nil),

		"person/create": {method: "POST", path: "/v1/persons", out: "data", body: "name", build: pdBody("additionalFields", false, nil)},
		"person/delete": {method: "DELETE", path: "/v1/persons/{personId}", out: "data"},
		"person/get":    {path: "/v1/persons/{personId}", out: "data"},
		"person/getAll": list("/v1/persons", pdQuery("", func(ec *execCtx, i int, q map[string]interface{}) {
			f := fieldsOf(ec, i, "additionalFields")
			for from, to := range map[string]string{"filterId": "filter_id", "firstChar": "first_char", "sort": "sort"} {
				if s := toString(f[from]); s != "" {
					q[to] = s
				}
			}
		})),
		"person/search": pdSearch("/v1/persons/search", true, nil),
		"person/update": {method: "PUT", path: "/v1/persons/{personId}", out: "data", build: pdBody("updateFields", false, pdLabelNull)},

		"product/getAll": list("/v1/products", nil),

		"lead/create": {method: "POST", path: "/v1/leads", out: "data", body: "title", build: pdLead("additionalFields", true)},
		"lead/delete": {method: "DELETE", path: "/v1/leads/{leadId}", out: "data"},
		"lead/get":    {path: "/v1/leads/{leadId}", out: "data"},
		"lead/getAll": list("/v1/leads", pdQuery("filters", nil)),
		"lead/update": {method: "PATCH", path: "/v1/leads/{leadId}", out: "data", build: pdLead("updateFields", false)},
	}
	return pdWrap(ops, []string{"get", "getAll", "update"}, pdCustomV1)
}

// ---------------------------------------------------------------- v2

func pipedriveV2Ops() map[string]*restOp {
	cursor := func(path string, build func(ec *execCtx, i int, r *restRequest) error) *restOp {
		return &restOp{path: path, list: true, pager: pdCursorPager, sizeParam: "limit", maxSize: 500, build: build}
	}
	offset := func(path string, build func(ec *execCtx, i int, r *restRequest) error) *restOp {
		return &restOp{path: path, list: true, pager: pdOffsetPager, sizeParam: "limit", maxSize: 100, build: build}
	}
	del := func(path string) *restOp { return &restOp{method: "DELETE", path: path, success: true} }
	ownerID := func(b map[string]interface{}) { rename(b, "user_id", "owner_id") }
	activity := func(update bool) func(ec *execCtx, i int, b map[string]interface{}) error {
		return func(ec *execCtx, i int, b map[string]interface{}) error {
			ownerID(b)
			if p := b["person_id"]; !restEmpty(p) && p != false && p != float64(0) {
				b["participants"] = []interface{}{map[string]interface{}{"person_id": p, "primary": true}}
			}
			delete(b, "person_id")
			if d, ok := b["done"]; ok || !update {
				b["done"] = pdCoerceBool(d)
			}
			if d := toString(b["due_date"]); d != "" {
				b["due_date"] = pdDateOnly(d)
			}
			return nil
		}
	}
	numbers := func(keys ...string) func(b map[string]interface{}) error {
		return func(b map[string]interface{}) error {
			for _, k := range keys {
				if v, ok := b[k]; ok && v != nil {
					f, ok := toFloat(v)
					if !ok {
						return permanentError{"Cannot convert \"" + toString(v) + "\" to a number"}
					}
					b[k] = f
				}
			}
			return nil
		}
	}
	deal := func(ec *execCtx, i int, b map[string]interface{}) error {
		ownerID(b)
		if d := toString(b["expected_close_date"]); d != "" {
			b["expected_close_date"] = pdDateOnly(d)
		}
		return numbers("value", "probability")(b)
	}
	flags := func(b map[string]interface{}) {
		for _, k := range []string{"pinned_to_deal_flag", "pinned_to_person_flag", "pinned_to_organization_flag"} {
			if v, ok := b[k]; ok {
				b[k] = map[bool]int{true: 1, false: 0}[toBool(v)]
			}
		}
	}
	unwrap := func(key, sub string) func(ec *execCtx, i int, b map[string]interface{}) error {
		return func(_ *execCtx, _ int, b map[string]interface{}) error {
			if s, ok := asMap(b[key])[sub]; ok {
				b[key] = s
			}
			return nil
		}
	}
	person := func(update bool) func(ec *execCtx, i int, b map[string]interface{}) error {
		return func(ec *execCtx, i int, b map[string]interface{}) error {
			_ = unwrap("emails", "emailProperties")(ec, i, b)
			_ = unwrap("phones", "phoneProperties")(ec, i, b)
			if update {
				return pdLabelNull(ec, i, b)
			}
			return nil
		}
	}
	filters := func(m map[string]string) func(ec *execCtx, i int, r *restRequest) error {
		return func(ec *execCtx, i int, r *restRequest) error {
			f := fieldsOf(ec, i, "filters")
			for from, to := range m {
				s := toString(f[from])
				if s == "" {
					continue
				}
				if from == "first_char" {
					s = s[:1]
				}
				r.query.Set(to, s)
			}
			return nil
		}
	}
	ops := map[string]*restOp{
		"activity/create": {method: "POST", path: "/api/v2/activities", out: "data", body: "subject type",
			build: pdBody("additionalFields", true, func(ec *execCtx, i int, b map[string]interface{}) error {
				b["done"], _ = ec.param(i, "done", false)
				return activity(false)(ec, i, b)
			})},
		"activity/delete": del("/api/v2/activities/{activityId}"),
		"activity/get":    {path: "/api/v2/activities/{activityId}", out: "data"},
		"activity/getAll": cursor("/api/v2/activities", func(ec *execCtx, i int, r *restRequest) error {
			f := fieldsOf(ec, i, "filters")
			for from, to := range map[string]string{"filterId": "filter_id", "user_id": "owner_id", "start_date": "start_date", "end_date": "end_date", "dealId": "deal_id"} {
				if s := toString(f[from]); s != "" {
					r.query.Set(to, s)
				}
			}
			if t := f["type"]; !restEmpty(t) {
				r.query.Set("type", joinList(t))
			}
			if d, ok := f["done"]; ok && d != nil {
				r.query.Set("done", map[bool]string{true: "1", false: "0"}[d == true])
			}
			return nil
		}),
		"activity/update": {method: "PATCH", path: "/api/v2/activities/{activityId}", out: "data", build: pdBody("updateFields", true, activity(true))},

		"deal/create": {method: "POST", path: "/api/v2/deals", out: "data", body: "title",
			build: pdBody("additionalFields", true, func(ec *execCtx, i int, b map[string]interface{}) error {
				if err := pdAssociate("org_id")(ec, i, b); err != nil {
					return err
				}
				return deal(ec, i, b)
			})},
		"deal/delete":    del("/api/v2/deals/{dealId}"),
		"deal/duplicate": {method: "POST", path: "/api/v2/deals/{dealId}/duplicate", out: "data"},
		"deal/get":       {path: "/api/v2/deals/{dealId}", out: "data"},
		"deal/getAll":    cursor("/api/v2/deals", filters(map[string]string{"filter_id": "filter_id", "stage_id": "stage_id", "status": "status", "user_id": "owner_id"})),
		"deal/search": pdSearch("/v1/deals/search", false, func(ec *execCtx, i int, q map[string]interface{}) {
			if s := toString(fieldsOf(ec, i, "additionalFields")["status"]); s != "" {
				q["status"] = s
			}
		}),
		"deal/update": {method: "PATCH", path: "/api/v2/deals/{dealId}", out: "data",
			build: pdBody("updateFields", true, func(ec *execCtx, i int, b map[string]interface{}) error {
				_ = pdLabelNull(ec, i, b)
				return deal(ec, i, b)
			})},

		"dealProduct/add": {method: "POST", path: "/api/v2/deals/{dealId}/products", out: "data", body: "productId:product_id item_price quantity @additionalFields *",
			build: func(ec *execCtx, i int, r *restRequest) error {
				return numbers("item_price", "quantity", "discount", "tax")(r.body)
			}},
		"dealProduct/getAll": cursor("/api/v2/deals/{dealId}/products", nil),
		"dealProduct/remove": del("/api/v2/deals/{dealId}/products/{productAttachmentId}"),
		"dealProduct/update": {method: "PATCH", path: "/api/v2/deals/{dealId}/products/{productAttachmentId}", out: "data", body: "@updateFields *",
			build: func(ec *execCtx, i int, r *restRequest) error {
				return numbers("item_price", "quantity", "discount", "tax")(r.body)
			}},

		"file/create":   {run: pdFileCreate("/v1")},
		"file/delete":   del("/v1/files/{fileId}"),
		"file/download": {run: pdFileDownload},
		"file/get":      {path: "/v1/files/{fileId}", out: "data"},
		"file/update":   {method: "PUT", path: "/v1/files/{fileId}", out: "data", body: "@updateFields *"},

		"lead/create": {method: "POST", path: "/v1/leads", out: "data", body: "title", build: pdLead("additionalFields", true)},
		"lead/delete": del("/v1/leads/{leadId}"),
		"lead/get":    {path: "/v1/leads/{leadId}", out: "data"},
		"lead/getAll": offset("/v1/leads", pdQuery("filters", nil)),
		// n8n v2 sends PUT, which the leads API does not accept; v1's PATCH is used.
		"lead/update": {method: "PATCH", path: "/v1/leads/{leadId}", out: "data", build: pdLead("updateFields", false)},

		"note/create": {method: "POST", path: "/v1/notes", out: "data", body: "content @additionalFields *",
			build: func(ec *execCtx, i int, r *restRequest) error { flags(r.body); return nil }},
		"note/delete": del("/v1/notes/{noteId}"),
		"note/get":    {path: "/v1/notes/{noteId}", out: "data"},
		"note/getAll": offset("/v1/notes", pdQuery("filters", func(_ *execCtx, _ int, q map[string]interface{}) { flags(q) })),
		"note/update": {method: "PUT", path: "/v1/notes/{noteId}", out: "data", body: "@updateFields *",
			build: func(ec *execCtx, i int, r *restRequest) error { flags(r.body); return nil }},

		"organization/create": {method: "POST", path: "/api/v2/organizations", out: "data", body: "name", build: pdBody("additionalFields", true, nil)},
		"organization/delete": del("/api/v2/organizations/{organizationId}"),
		"organization/get":    {path: "/api/v2/organizations/{organizationId}", out: "data"},
		"organization/getAll": cursor("/api/v2/organizations", filters(map[string]string{"filter_id": "filter_id", "first_char": "first_char"})),
		"organization/search": pdSearch("/v1/organizations/search", false, nil),
		"organization/update": {method: "PATCH", path: "/api/v2/organizations/{organizationId}", out: "data", build: pdBody("updateFields", true, pdLabelNull)},

		"person/create": {method: "POST", path: "/api/v2/persons", out: "data", body: "name", build: pdBody("additionalFields", true, person(false))},
		"person/delete": del("/api/v2/persons/{personId}"),
		"person/get":    {path: "/api/v2/persons/{personId}", out: "data"},
		"person/getAll": cursor("/api/v2/persons", filters(map[string]string{"filter_id": "filter_id", "first_char": "first_char"})),
		"person/search": pdSearch("/v1/persons/search", false, nil),
		"person/update": {method: "PATCH", path: "/api/v2/persons/{personId}", out: "data", build: pdBody("updateFields", true, person(true))},

		"product/create": {method: "POST", path: "/api/v2/products", out: "data", body: "name", build: pdBody("additionalFields", true, unwrap("prices", "pricesValues"))},
		"product/delete": del("/api/v2/products/{productId}"),
		"product/get":    {path: "/api/v2/products/{productId}", out: "data"},
		"product/getAll": cursor("/api/v2/products", nil),
		"product/search": pdSearch("/v1/products/search", false, nil),
		"product/update": {method: "PATCH", path: "/api/v2/products/{productId}", out: "data", build: pdBody("updateFields", true, unwrap("prices", "pricesValues"))},
	}
	return pdWrap(ops, []string{"create", "get", "getAll", "update"}, pdCustomV2)
}

func pdCoerceBool(v interface{}) bool {
	switch t := v.(type) {
	case string:
		switch strings.ToLower(t) {
		case "1", "true":
			return true
		case "0", "false", "":
			return false
		}
		return true
	case float64:
		return t != 0
	}
	return toBool(v)
}

func pdDateOnly(s string) string {
	d := strings.Split(strings.Split(s, "T")[0], " ")[0]
	if len(d) == 10 && d[4] == '-' && d[7] == '-' {
		return d
	}
	return s
}

// ---------------------------------------------------------------- custom fields

type pdField struct {
	key, name, typ string
	options        []interface{}
}

// pdFields loads a resource's field definitions keyed by field key.
func pdFields(ec *execCtx, c *restClient, resource string, v2 bool) (map[string]pdField, error) {
	out := map[string]pdField{}
	path := "/v1/" + resource + "Fields"
	q := url.Values{}
	if v2 {
		path = "/api/v2/" + resource + "Fields"
		q.Set("limit", "500")
	}
	for n := 0; n < 100; n++ {
		v, err := c.do(ec, &restRequest{method: "GET", path: path, query: q})
		if err != nil {
			return nil, err
		}
		for _, x := range asList(jpath(v, "data")) {
			m := asMap(x)
			k := toString(pick(m, "field_code", "key"))
			name := toString(pick(m, "field_name", "name"))
			if k != "" && name != "" {
				out[k] = pdField{key: k, name: name, typ: toString(m["field_type"]), options: asList(m["options"])}
			}
		}
		next := toString(jpath(v, "additional_data.next_cursor"))
		if !v2 || next == "" {
			break
		}
		q.Set("cursor", next)
	}
	return out, nil
}

var pdPassthroughTypes = map[string]bool{"date": true, "address": true, "double": true, "monetary": true, "org": true, "people": true,
	"phone": true, "text": true, "time": true, "user": true, "varchar": true, "varchar_auto": true, "int": true, "timerange": true}

func (f pdField) optionLabel(id interface{}) (interface{}, bool) {
	for _, o := range f.options {
		m := asMap(o)
		if toString(m["id"]) == toString(id) {
			return m["label"], true
		}
	}
	return nil, false
}

func (f pdField) optionID(label interface{}) (interface{}, bool) {
	for _, o := range f.options {
		m := asMap(o)
		if toString(m["label"]) == toString(label) {
			return m["id"], true
		}
	}
	return nil, false
}

// pdResolve renames custom field keys to names and option ids to labels;
// it returns false when the value stays under its key (v1 leaves unknown
// option ids unchanged).
func pdResolve(f pdField, v interface{}, v2 bool) (interface{}, bool) {
	if v == nil || pdPassthroughTypes[f.typ] {
		return v, true
	}
	switch {
	case (f.typ == "enum" || f.typ == "visible_to") && f.options != nil:
		if l, ok := f.optionLabel(v); ok {
			return l, true
		}
		return v, v2
	case f.typ == "set" && f.options != nil:
		var ids []string
		if l, ok := v.([]interface{}); ok && v2 {
			for _, x := range l {
				ids = append(ids, toString(x))
			}
		} else if s, ok := v.(string); ok {
			ids = strings.Split(s, ",")
		} else if !v2 {
			return v, false
		} else {
			ids = []string{toString(v)}
		}
		labels := []interface{}{}
		for _, o := range f.options {
			m := asMap(o)
			for _, id := range ids {
				if toString(m["id"]) == id {
					labels = append(labels, m["label"])
					break
				}
			}
		}
		return labels, true
	}
	return v, v2
}

// pdCustomV1 resolves/encodes custom properties when resolveProperties
// (get, getAll) or encodeProperties (update) is set; output is resolved in
// both cases, as n8n does.
func pdCustomV1(resource, operation string, op *restOp) *restOp {
	param := "resolveProperties"
	if operation == "update" {
		param = "encodeProperties"
	}
	return &restOp{run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		if on, _ := ec.boolean(i, param, false); !on {
			return pdExec.exec(ec, i, op, c)
		}
		props, err := pdFields(ec, c, resource, false)
		if err != nil {
			return nil, err
		}
		o := *op
		o.build = func(ec *execCtx, i int, r *restRequest) error {
			if op.build != nil {
				if err := op.build(ec, i, r); err != nil {
					return err
				}
			}
			byName := map[string]pdField{}
			for _, f := range props {
				byName[f.name] = f
			}
			for k, v := range r.body {
				f, ok := byName[k]
				if !ok {
					continue
				}
				if v != nil && f.options != nil {
					if id, ok := f.optionID(v); ok {
						delete(r.body, k)
						r.body[f.key] = id
					}
					continue
				}
				delete(r.body, k)
				r.body[f.key] = v
			}
			return nil
		}
		o.post = func(ec *execCtx, i int, v interface{}) (interface{}, error) {
			if op.post != nil {
				var err error
				if v, err = op.post(ec, i, v); err != nil {
					return nil, err
				}
			}
			for _, x := range asListOrOne(v) {
				m := asMap(x)
				for k, val := range m {
					f, ok := props[k]
					if !ok {
						continue
					}
					if nv, ok := pdResolve(f, val, false); ok {
						delete(m, k)
						m[f.name] = nv
					}
				}
			}
			return v, nil
		}
		return pdExec.exec(ec, i, &o, c)
	}}
}

// pdCustomV2 encodes custom_fields names/labels to keys/ids on create and
// update unless rawCustomFieldKeys, and resolves custom_fields in the
// output unless rawCustomFieldOutput (create/update resolve with the keys
// option).
func pdCustomV2(resource, operation string, op *restOp) *restOp {
	param := "rawCustomFieldOutput"
	write := operation == "create" || operation == "update"
	if write {
		param = "rawCustomFieldKeys"
	}
	return &restOp{run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		if raw, _ := ec.boolean(i, param, false); raw {
			return pdExec.exec(ec, i, op, c)
		}
		props, err := pdFields(ec, c, resource, true)
		if err != nil {
			return nil, err
		}
		o := *op
		if write {
			o.build = func(ec *execCtx, i int, r *restRequest) error {
				if op.build != nil {
					if err := op.build(ec, i, r); err != nil {
						return err
					}
				}
				pdEncodeV2(props, r.body)
				return nil
			}
		}
		o.post = func(ec *execCtx, i int, v interface{}) (interface{}, error) {
			if op.post != nil {
				var err error
				if v, err = op.post(ec, i, v); err != nil {
					return nil, err
				}
			}
			for _, x := range asListOrOne(v) {
				m := asMap(x)
				cf, ok := m["custom_fields"].(map[string]interface{})
				if !ok {
					continue
				}
				res := map[string]interface{}{}
				for k, val := range cf {
					f, ok := props[k]
					if !ok {
						res[k] = val
						continue
					}
					nv, _ := pdResolve(f, val, true)
					res[f.name] = nv
				}
				m["custom_fields"] = res
			}
			return v, nil
		}
		return pdExec.exec(ec, i, &o, c)
	}}
}

func pdEncodeV2(props map[string]pdField, body map[string]interface{}) {
	in := asMap(body["custom_fields"])
	if len(in) == 0 {
		delete(body, "custom_fields")
		return
	}
	byName := map[string]pdField{}
	for _, f := range props {
		byName[f.name] = f
	}
	out := map[string]interface{}{}
	for k, v := range in {
		f, ok := byName[k]
		if !ok {
			f, ok = props[k]
		}
		if !ok {
			out[k] = v
			continue
		}
		if v == nil || f.options == nil {
			out[f.key] = v
			continue
		}
		if f.typ == "set" {
			var labels []interface{}
			if l, ok := v.([]interface{}); ok {
				labels = l
			} else {
				for _, s := range strings.Split(toString(v), ",") {
					labels = append(labels, strings.TrimSpace(s))
				}
			}
			ids := []interface{}{}
			for _, l := range labels {
				if id, ok := f.optionID(l); ok {
					ids = append(ids, id)
				} else {
					ids = append(ids, toString(l))
				}
			}
			out[f.key] = ids
			continue
		}
		if id, ok := f.optionID(v); ok {
			out[f.key] = id
		} else {
			out[f.key] = v
		}
	}
	body["custom_fields"] = out
}
