package n8n

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Zoho CRM (n8n-nodes-base.zohoCrm), API v2. The data center comes from
// the OAuth token's api_domain (or the credential's accessTokenUrl).

// zohoModule describes a CRM module: its path, the top-level parameters of
// create/upsert (param:Field, "#" marks an {id: …} reference), the id
// parameter and the payload adjustments of n8n's adjust*Payload.
type zohoModule struct {
	path, idParam string
	required      []string
	products      bool
	address       []string
	dates         []string
	ids           [][2]string
}

var zohoModules = map[string]zohoModule{
	"account": {path: "accounts", idParam: "accountId", required: []string{"accountName:Account_Name"}, address: []string{"Billing_Address", "Shipping_Address"}},
	"contact": {path: "contacts", idParam: "contactId", required: []string{"lastName:Last_Name"}, address: []string{"Mailing_Address", "Other_Address"}, dates: []string{"Date_of_Birth"}},
	"deal":    {path: "deals", idParam: "dealId", required: []string{"dealName:Deal_Name", "stage:Stage"}, dates: []string{"Closing_Date"}},
	"invoice": {path: "invoices", idParam: "invoiceId", required: []string{"subject:Subject"}, products: true, address: []string{"Billing_Address", "Shipping_Address"},
		dates: []string{"Invoice_Date", "Due_Date"}, ids: [][2]string{{"accountId", "Account_Name"}}},
	"lead":    {path: "leads", idParam: "leadId", required: []string{"Company:Company", "lastName:Last_Name"}, address: []string{"Address"}},
	"product": {path: "products", idParam: "productId", required: []string{"productName:Product_Name"}},
	"purchaseOrder": {path: "purchase_orders", idParam: "purchaseOrderId", required: []string{"subject:Subject", "vendorId:Vendor_Name#"}, products: true,
		address: []string{"Billing_Address", "Shipping_Address"}, dates: []string{"Due_Date", "PO_Date"}},
	"quote": {path: "quotes", idParam: "quoteId", required: []string{"subject:Subject"}, products: true, address: []string{"Billing_Address", "Shipping_Address"}, dates: []string{"Valid_Till"}},
	"salesOrder": {path: "sales_orders", idParam: "salesOrderId", required: []string{"accountId:Account_Name#", "subject:Subject"}, products: true,
		address: []string{"Billing_Address", "Shipping_Address"}, dates: []string{"Due_Date"},
		ids: [][2]string{{"accountId", "Account_Name"}, {"contactId", "Full_Name"}, {"dealId", "Deal_Name"}}},
	"vendor": {path: "vendors", idParam: "vendorId", required: []string{"vendorName:Vendor_Name"}, address: []string{"Address"}},
}

var zohoDomainRe = regexp.MustCompile(`accounts\.zoho\.([a-z.]+)`)

func init() {
	ops := map[string]*restOp{
		"lead/getFields": {path: "/settings/fields", query: "=module:leads", out: "fields"},
	}
	defOps := map[string]string{}
	for r, m := range zohoModules {
		r, m := r, m
		defOps[r] = "create"
		ops[r+"/create"] = &restOp{run: zohoWrite(r, m, "create")}
		ops[r+"/upsert"] = &restOp{run: zohoWrite(r, m, "upsert")}
		ops[r+"/update"] = &restOp{run: zohoWrite(r, m, "update")}
		ops[r+"/delete"] = &restOp{run: zohoWrite(r, m, "delete")}
		ops[r+"/get"] = &restOp{path: "/" + m.path + "/{" + m.idParam + "}", out: "data", post: zohoCheck}
		ops[r+"/getAll"] = &restOp{run: zohoList(m.path)}
	}
	registerREST("zohoCrm", &restService{
		defResource: "account",
		defOps:      defOps,
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("zohoOAuth2Api")
			if err != nil {
				return nil, err
			}
			domain := toString(jpath(c, "oauthTokenData.api_domain"))
			if domain == "" || domain == "<nil>" {
				domain = "https://www.zohoapis.com"
				if m := zohoDomainRe.FindStringSubmatch(credString(c, "accessTokenUrl", "authUrl")); m != nil {
					domain = "https://www.zohoapis." + m[1]
				}
			}
			base := ec.apiBase("zohoCrm", strings.TrimRight(domain, "/")) + "/crm/v2"
			return &restClient{base: base, headers: map[string]string{"Authorization": "Bearer " + accessToken(c)}}, nil
		},
		ops: ops,
	})
}

// zohoCheck raises the error status of a record result.
func zohoCheck(ec *execCtx, _ int, v interface{}) (interface{}, error) {
	return v, zohoErr(ec, v)
}

func zohoErr(ec *execCtx, v interface{}) error {
	rec := asMap(jpath(v, "data.0"))
	if toString(rec["status"]) == "error" {
		return permanentError{fmt.Sprintf("%s: %s %s", ec.node.Name, toString(rec["message"]), mustJSON(rec))}
	}
	return nil
}

func zohoWrite(resource string, m zohoModule, op string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		r := &restRequest{query: url.Values{}, body: map[string]interface{}{}}
		var record map[string]interface{}
		switch op {
		case "create", "upsert":
			r.method, r.path = "POST", "/"+m.path
			if op == "upsert" {
				r.path += "/upsert"
			}
			record = map[string]interface{}{}
			for _, f := range m.required {
				p, field, _ := strings.Cut(f, ":")
				v, err := ec.param(i, p, "")
				if err != nil {
					return nil, err
				}
				if strings.HasSuffix(field, "#") {
					record[strings.TrimSuffix(field, "#")] = map[string]interface{}{"id": v}
				} else {
					record[field] = v
				}
			}
			if m.products {
				v, err := ec.param(i, "Product_Details", nil)
				if err != nil {
					return nil, err
				}
				pd := asListOrOne(v)
				if restEmpty(v) {
					pd = []interface{}{}
				}
				if op == "create" && len(pd) == 0 {
					return nil, permanentError{fmt.Sprintf("%s: Please enter at least one product for the %s.", ec.node.Name, resource)}
				}
				record["Product_Details"] = zohoProducts(pd, op == "upsert" && resource == "salesOrder")
			}
			for k, v := range zohoAdjust(m, fieldsOf(ec, i, "additionalFields")) {
				record[k] = v
			}
		case "update":
			p, err := ec.restPath(i, "/"+m.path+"/{"+m.idParam+"}")
			if err != nil {
				return nil, err
			}
			r.method, r.path = "PUT", p
			fields := fieldsOf(ec, i, "updateFields")
			if len(fields) == 0 {
				return nil, permanentError{fmt.Sprintf("%s: Please enter at least one field to update for the %s.", ec.node.Name, resource)}
			}
			record = zohoAdjust(m, fields)
			if resource == "invoice" && record["Product_Details"] != nil {
				var pd []interface{}
				for _, x := range asListOrOne(record["Product_Details"]) {
					q := map[string]interface{}{}
					for k, v := range asMap(x) {
						if k != "product" {
							q[k] = v
						}
					}
					q["product"] = map[string]interface{}{"id": q["id"]}
					q["quantity"] = zohoQty(q["quantity"])
					pd = append(pd, q)
				}
				record["Product_Details"] = pd
			}
		case "delete":
			p, err := ec.restPath(i, "/"+m.path+"/{"+m.idParam+"}")
			if err != nil {
				return nil, err
			}
			r.method, r.path = "DELETE", p
		}
		if record != nil {
			r.body = map[string]interface{}{"data": []interface{}{record}}
		}
		v, err := c.do(ec, r)
		if err != nil {
			return nil, err
		}
		if err := zohoErr(ec, v); err != nil {
			return nil, err
		}
		return restItems(jpath(v, "data.0.details")), nil
	}
}

func zohoQty(v interface{}) interface{} {
	if restEmpty(v) || v == false || v == float64(0) {
		return 1
	}
	return v
}

// zohoProducts nests each product id as {product: {id}} (n8n's adjustProductDetails).
func zohoProducts(pd []interface{}, upsert bool) []interface{} {
	out := make([]interface{}, 0, len(pd))
	for _, x := range pd {
		p := asMap(x)
		q := map[string]interface{}{"product": map[string]interface{}{"id": p["id"]}, "quantity": zohoQty(p["quantity"])}
		for k, v := range p {
			if (upsert && k == "id") || (!upsert && k == "product") {
				continue
			}
			q[k] = v
		}
		out = append(out, q)
	}
	return out
}

// zohoAdjust flattens address fields, trims dates, nests id references and
// spreads custom fields.
func zohoAdjust(m zohoModule, fields map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range fields {
		out[k] = v
	}
	for _, a := range m.address {
		if v, ok := out[a]; ok && !restEmpty(v) {
			delete(out, a)
			for k, x := range asMap(asMap(v)["address_fields"]) {
				out[k] = x
			}
		}
	}
	for _, d := range m.dates {
		if s, ok := out[d].(string); ok && s != "" {
			out[d] = strings.Split(s, "T")[0]
		}
	}
	for _, id := range m.ids {
		if v, ok := out[id[0]]; ok && !restEmpty(v) {
			delete(out, id[0])
			out[id[1]] = map[string]interface{}{"id": v}
		}
	}
	if cf, ok := out["customFields"]; ok {
		if l := asList(asMap(cf)["customFields"]); len(l) > 0 {
			delete(out, "customFields")
			for _, x := range l {
				f := asMap(x)
				out[toString(f["fieldId"])] = f["value"]
			}
		}
	}
	return out
}

// zohoList pages with per_page=200 while info.more_records (limit default 5).
func zohoList(path string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		q := url.Values{}
		for k, v := range fieldsOf(ec, i, "options") {
			if k == "fields" {
				var parts []string
				for _, f := range asListOrOne(v) {
					parts = append(parts, toString(f))
				}
				q.Set("fields", strings.Join(parts, ","))
				continue
			}
			q.Set(k, toString(v))
		}
		all, _ := ec.boolean(0, "returnAll", false)
		limit := -1
		if !all {
			n, _ := ec.num(0, "limit", 5)
			limit = int(n)
		}
		q.Set("per_page", "200")
		var out []interface{}
		for page := 1; page < 10000; page++ {
			q.Set("page", strconv.Itoa(page))
			v, err := c.do(ec, &restRequest{method: "GET", path: "/" + path, query: q})
			if err != nil {
				return nil, err
			}
			if v == nil {
				break
			}
			if err := zohoErr(ec, v); err != nil {
				return nil, err
			}
			out = append(out, asList(jpath(v, "data"))...)
			if (limit >= 0 && len(out) >= limit) || jpath(v, "info.more_records") != true {
				break
			}
		}
		if limit >= 0 && len(out) > limit {
			out = out[:limit]
		}
		return restItems(out), nil
	}
}
