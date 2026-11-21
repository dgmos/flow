package n8n

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// QuickBooks Online (n8n-nodes-base.quickbooks), Accounting API v3. The
// client's base already contains /v3/company/{realmId}.

func init() {
	ops := map[string]*restOp{
		"transaction/getReport": {path: "/reports/TransactionList", build: qbReportQuery, post: qbReportPost},
	}
	for _, r := range []string{"bill", "customer", "employee", "estimate", "invoice", "item", "payment", "purchase", "vendor"} {
		ops[r+"/getAll"] = &restOp{path: "/query", list: true, listKey: "QueryResponse." + qbCap(r), pager: qbPager, build: qbQuery(qbCap(r))}
		ops[r+"/get"] = &restOp{path: "/" + r + "/{" + r + "Id}", out: qbCap(r)}
	}
	for _, r := range []string{"bill", "customer", "employee", "estimate", "invoice", "payment", "vendor"} {
		ops[r+"/create"] = &restOp{method: "POST", path: "/" + r, out: qbCap(r), build: qbCreate(r)}
		ref := map[string]string{"bill": "VendorRef", "estimate": "CustomerRef", "invoice": "CustomerRef", "payment": "CustomerRef"}[r]
		ops[r+"/update"] = &restOp{run: qbUpdate(r, ref)}
	}
	for _, r := range []string{"bill", "estimate", "invoice", "payment"} {
		ops[r+"/delete"] = &restOp{run: qbDelete(r, "delete")}
	}
	for _, r := range []string{"estimate", "invoice", "payment"} {
		ops[r+"/get"].run = qbGet(r)
		ops[r+"/send"] = &restOp{method: "POST", path: "/" + r + "/{" + r + "Id}/send", query: "email:sendTo", out: qbCap(r),
			build: func(ec *execCtx, i int, rq *restRequest) error {
				rq.raw, rq.contentType = []byte{}, "application/octet-stream"
				return nil
			}}
	}
	for _, r := range []string{"invoice", "payment"} {
		ops[r+"/void"] = &restOp{run: qbDelete(r, "void")}
	}
	registerREST("quickbooks", &restService{
		defResource: "customer",
		defOps: map[string]string{"bill": "get", "customer": "get", "employee": "get", "estimate": "get", "invoice": "get", "item": "get",
			"payment": "get", "purchase": "get", "transaction": "getReport", "vendor": "get"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("quickBooksOAuth2Api")
			if err != nil {
				return nil, err
			}
			realm := toString(jpath(c, "oauthTokenData.callbackQueryString.realmId"))
			if realm == "" {
				realm = credString(c, "realmId", "companyId")
			}
			if realm == "" {
				return nil, permanentError{ec.node.Name + ": the QuickBooks credential has no realmId (company ID)"}
			}
			host := "https://quickbooks.api.intuit.com"
			if credString(c, "environment") == "sandbox" {
				host = "https://sandbox-quickbooks.api.intuit.com"
			}
			base := ec.apiBase("quickbooks", host) + "/v3/company/" + url.PathEscape(realm)
			return &restClient{base: base, headers: map[string]string{"Authorization": "Bearer " + accessToken(c)}}, nil
		},
		ops: ops,
	})
}

// qbQuery builds the SQL-like query of getAll: returnAll pages with
// MAXRESULTS 1000 STARTPOSITION n, otherwise MAXRESULTS limit.
func qbQuery(table string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		q := "SELECT * FROM " + table
		if f := toString(fieldsOf(ec, i, "filters")["query"]); f != "" {
			q += " " + f
		}
		if all, _ := ec.boolean(i, "returnAll", false); all {
			q += " MAXRESULTS 1000 STARTPOSITION 1"
		} else {
			n, _ := ec.num(i, "limit", 50)
			q += " MAXRESULTS " + strconv.Itoa(int(n))
		}
		r.query.Set("query", q)
		return nil
	}
}

// qbPager advances STARTPOSITION while full pages come back (n8n counts
// first with SELECT COUNT(*); a short page ends the listing the same way).
func qbPager(p *restPage, _ interface{}, _ http.Header) bool {
	q := p.query.Get("query")
	k := strings.Index(q, " STARTPOSITION ")
	if k < 0 || len(p.last) < 1000 {
		return false
	}
	p.query.Set("query", q[:k]+" STARTPOSITION "+strconv.Itoa(p.got+1))
	return true
}

func qbCreate(resource string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		param := func(name string) interface{} {
			v, _ := ec.param(i, name, nil)
			return rlValue(v)
		}
		switch resource {
		case "customer", "vendor":
			r.body["DisplayName"] = param("displayName")
		case "employee":
			r.body["FamilyName"], r.body["GivenName"] = param("FamilyName"), param("GivenName")
		case "payment":
			r.body["CustomerRef"] = map[string]interface{}{"value": param("CustomerRef")}
			v, _ := ec.param(i, "TotalAmt", nil)
			r.body["TotalAmt"] = v
		default: // bill, estimate, invoice
			ref := "CustomerRef"
			if resource == "bill" {
				ref = "VendorRef"
			}
			r.body[ref] = map[string]interface{}{"value": param(ref)}
			v, err := ec.param(i, "Line", nil)
			if err != nil {
				return err
			}
			var in []interface{}
			if !restEmpty(v) {
				in = asListOrOne(v)
			}
			lines, err := qbLines(ec, resource, in)
			if err != nil {
				return err
			}
			r.body["Line"] = lines
		}
		qbPopulate(resource, r.body, fieldsOf(ec, i, "additionalFields"))
		return nil
	}
}

// qbLines validates the Line collection and nests the item/account
// references the way n8n's processLines does.
func qbLines(ec *execCtx, resource string, in []interface{}) ([]interface{}, error) {
	if len(in) == 0 {
		return nil, permanentError{fmt.Sprintf("%s: Please enter at least one line for the %s.", ec.node.Name, resource)}
	}
	out := make([]interface{}, 0, len(in))
	for _, x := range in {
		l := map[string]interface{}{}
		for k, v := range asMap(x) {
			l[k] = v
		}
		if l["DetailType"] == nil || l["Amount"] == nil || l["Description"] == nil {
			return nil, permanentError{ec.node.Name + ": Please enter detail type, amount and description for every line."}
		}
		switch dt := toString(l["DetailType"]); {
		case resource == "bill" && dt == "AccountBasedExpenseLineDetail":
			if l["accountId"] == nil {
				return nil, permanentError{ec.node.Name + ": Please enter an account ID for the associated line."}
			}
			l[dt] = map[string]interface{}{"AccountRef": map[string]interface{}{"value": l["accountId"]}}
			delete(l, "accountId")
		case resource == "bill" && dt == "ItemBasedExpenseLineDetail":
			if l["itemId"] == nil {
				return nil, permanentError{ec.node.Name + ": Please enter an item ID for the associated line."}
			}
			l[dt] = map[string]interface{}{"ItemRef": map[string]interface{}{"value": l["itemId"]}}
			delete(l, "itemId")
		case resource != "bill" && dt == "SalesItemLineDetail":
			if l["itemId"] == nil {
				return nil, permanentError{ec.node.Name + ": Please enter an item ID for the associated line."}
			}
			d := map[string]interface{}{"ItemRef": map[string]interface{}{"value": l["itemId"]}, "TaxCodeRef": map[string]interface{}{"value": l["TaxCodeRef"]}}
			if resource == "invoice" {
				if q, ok := l["Qty"]; ok {
					d["Qty"] = q
				}
				delete(l, "Qty")
			}
			l[dt] = d
			delete(l, "itemId")
			delete(l, "TaxCodeRef")
		}
		out = append(out, l)
	}
	return out, nil
}

// qbPopulate copies additional/update fields into a body (n8n's populateFields).
func qbPopulate(resource string, body, fields map[string]interface{}) {
	details := func(v interface{}) map[string]interface{} {
		d := asMap(asMap(v)["details"])
		out := map[string]interface{}{}
		for k, x := range d {
			if x != "" {
				out[k] = x
			}
		}
		return out
	}
	ref := func(v interface{}) interface{} {
		d, ok := asMap(v)["details"]
		if !ok {
			return map[string]interface{}{"value": v}
		}
		m := asMap(d)
		return map[string]interface{}{"name": m["name"], "value": m["value"]}
	}
	for k, v := range fields {
		switch resource {
		case "bill":
			if strings.HasSuffix(k, "Ref") {
				body[k] = ref(v)
				continue
			}
		case "customer", "employee", "vendor":
			switch k {
			case "BillAddr":
				body[k] = details(v)
				continue
			case "PrimaryEmailAddr":
				body[k] = map[string]interface{}{"Address": v}
				continue
			case "PrimaryPhone":
				body[k] = map[string]interface{}{"FreeFormNumber": v}
				continue
			}
		case "estimate", "invoice":
			switch {
			case k == "BillAddr" || k == "ShipAddr":
				body[k] = details(v)
				continue
			case k == "BillEmail":
				body[k] = map[string]interface{}{"Address": v}
				continue
			case k == "CustomFields":
				var cf []interface{}
				for _, f := range asList(asMap(v)["Field"]) {
					m := map[string]interface{}{"Type": "StringType"}
					for fk, fv := range asMap(f) {
						m[fk] = fv
					}
					m["Type"] = "StringType"
					cf = append(cf, m)
				}
				body["CustomField"] = cf
				continue
			case k == "CustomerMemo":
				body[k] = map[string]interface{}{"value": v}
				continue
			case strings.HasSuffix(k, "Ref"):
				body[k] = ref(v)
				continue
			case k == "TotalTax":
				body["TxnTaxDetail"] = map[string]interface{}{"TotalTax": v}
				continue
			}
		}
		body[k] = v
	}
}

// qbCall sends one request and returns the decoded object.
func qbCall(ec *execCtx, c *restClient, method, path string, q url.Values, body map[string]interface{}) (map[string]interface{}, error) {
	if q == nil {
		q = url.Values{}
	}
	if body == nil {
		body = map[string]interface{}{}
	}
	v, err := c.do(ec, &restRequest{method: method, path: path, query: q, body: body})
	return asMap(v), err
}

func qbCap(r string) string { return strings.ToUpper(r[:1]) + r[1:] }

// qbUpdate reads the SyncToken (and the reference n8n keeps) and posts a
// sparse update.
func qbUpdate(resource, refKey string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		path, err := ec.restPath(i, "/"+resource+"/{"+resource+"Id}")
		if err != nil {
			return nil, err
		}
		cur, err := qbCall(ec, c, "GET", path, nil, nil)
		if err != nil {
			return nil, err
		}
		obj := asMap(cur[qbCap(resource)])
		id, _ := ec.param(i, resource+"Id", nil)
		body := map[string]interface{}{"Id": rlValue(id), "SyncToken": obj["SyncToken"], "sparse": true}
		if refKey != "" {
			r := asMap(obj[refKey])
			body[refKey] = map[string]interface{}{"name": r["name"], "value": r["value"]}
		}
		f := fieldsOf(ec, i, "updateFields")
		if len(f) == 0 {
			return nil, permanentError{fmt.Sprintf("%s: Please enter at least one field to update for the %s.", ec.node.Name, resource)}
		}
		qbPopulate(resource, body, f)
		res, err := qbCall(ec, c, "POST", "/"+resource, nil, body)
		if err != nil {
			return nil, err
		}
		return restItems(res[qbCap(resource)]), nil
	}
}

// qbDelete deletes or voids a transaction (operation=delete|void).
func qbDelete(resource, operation string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		path, err := ec.restPath(i, "/"+resource+"/{"+resource+"Id}")
		if err != nil {
			return nil, err
		}
		cur, err := qbCall(ec, c, "GET", path, nil, nil)
		if err != nil {
			return nil, err
		}
		id, _ := ec.param(i, resource+"Id", nil)
		sync := asMap(cur[qbCap(resource)])["SyncToken"]
		q := url.Values{"operation": {operation}}
		var body map[string]interface{}
		if operation == "void" {
			q.Set("Id", rlValue(id))
			q.Set("SyncToken", toString(sync))
		} else {
			body = map[string]interface{}{"Id": rlValue(id), "SyncToken": sync}
		}
		res, err := qbCall(ec, c, "POST", "/"+resource, q, body)
		if err != nil {
			return nil, err
		}
		return restItems(res[qbCap(resource)]), nil
	}
}

// qbGet returns the record, or with download=true the input item with the
// PDF added as binary data.
func qbGet(resource string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		path, err := ec.restPath(i, "/"+resource+"/{"+resource+"Id}")
		if err != nil {
			return nil, err
		}
		if dl, _ := ec.boolean(i, "download", false); !dl {
			res, err := qbCall(ec, c, "GET", path, nil, nil)
			if err != nil {
				return nil, err
			}
			return restItems(res[qbCap(resource)]), nil
		}
		h := map[string]string{}
		for k, v := range c.headers {
			h[k] = v
		}
		h["Accept"] = "application/pdf"
		raw, err := ec.doRequest("GET", c.base+path+"/pdf", h, nil, "")
		if err != nil {
			return nil, err
		}
		prop, _ := ec.str(i, "binaryProperty", "data")
		name, _ := ec.str(i, "fileName", "")
		bin := binaryData(raw.body, "application/pdf", name)
		bin["fileExtension"] = "pdf"
		out := Item{JSON: it.JSON, Binary: map[string]interface{}{}}
		for k, v := range it.Binary {
			out.Binary[k] = v
		}
		out.Binary[firstNonEmpty(prop, "data")] = bin
		if out.JSON == nil {
			out.JSON = map[string]interface{}{}
		}
		return []Item{out}, nil
	}
}

// qbReportQuery maps the transaction report filters to query parameters.
func qbReportQuery(ec *execCtx, i int, r *restRequest) error {
	for k, v := range fieldsOf(ec, i, "filters") {
		switch k {
		case "columns", "memo", "term", "customer", "vendor":
			var parts []string
			for _, x := range asListOrOne(v) {
				parts = append(parts, toString(x))
			}
			if len(parts) > 0 {
				r.query.Set(k, strings.Join(parts, ","))
			}
		case "dateRangeCustom", "dateRangeDueCustom", "dateRangeModificationCustom", "dateRangeCreationCustom":
			for dk, dv := range asMap(asMap(v)[k+"Properties"]) {
				r.query.Set(dk, strings.Split(toString(dv), "T")[0])
			}
		default:
			if !restEmpty(v) {
				r.query.Set(k, toString(v))
			}
		}
	}
	return nil
}

// qbReportPost simplifies the report into one object per row unless
// simple is false.
func qbReportPost(ec *execCtx, i int, v interface{}) (interface{}, error) {
	rows := asMap(jpath(v, "Rows"))
	if len(rows) == 0 {
		return []interface{}{}, nil
	}
	if simple, _ := ec.boolean(i, "simple", true); !simple {
		return v, nil
	}
	var cols []string
	for _, c := range asList(jpath(v, "Columns.Column")) {
		cols = append(cols, toString(asMap(c)["ColType"]))
	}
	out := []interface{}{}
	for _, row := range asList(rows["Row"]) {
		t := map[string]interface{}{}
		for n, cd := range asList(asMap(row)["ColData"]) {
			if n < len(cols) {
				t[cols[n]] = asMap(cd)["value"]
			}
		}
		out = append(out, t)
	}
	return out, nil
}
