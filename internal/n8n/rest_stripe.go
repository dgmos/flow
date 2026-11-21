package n8n

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Stripe (n8n-nodes-base.stripe), API v1 with form-encoded bodies.

func init() {
	list := func(path string, query string) *restOp {
		return &restOp{path: path, list: true, query: query}
	}
	s := &restService{
		defResource: "balance",
		defOps: map[string]string{"balance": "get", "charge": "get", "coupon": "create", "customer": "get", "customerCard": "get",
			"meterEvent": "create", "source": "get", "token": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("stripeApi")
			if err != nil {
				return nil, err
			}
			return bearerClient(ec.apiBase("stripe", "https://api.stripe.com/v1"), c, "secretKey"), nil
		},
		pager:   lastIDPager("has_more", "starting_after"),
		listKey: "data",
		ops: map[string]*restOp{
			"balance/get":         {path: "/balance"},
			"customerCard/add":    {method: "POST", path: "/customers/{customerId}/sources", form: true, body: "token:source"},
			"customerCard/remove": {method: "DELETE", path: "/customers/{customerId}/sources/{cardId}"},
			"customerCard/get":    {path: "/customers/{customerId}/sources/{sourceId}"},
			"charge/create": {method: "POST", path: "/charges", form: true, body: "customerId:customer currency amount source",
				build: stripeFields("additionalFields", func(b map[string]interface{}) {
					if c, ok := b["currency"].(string); ok {
						b["currency"] = strings.ToLower(c)
					}
				})},
			"charge/get":      {path: "/charges/{chargeId}"},
			"charge/getAll":   list("/charges", ""),
			"charge/update":   {method: "POST", path: "/charges/{chargeId}", form: true, build: stripeFields("updateFields", nil)},
			"coupon/create":   {method: "POST", path: "/coupons", form: true, body: "duration", build: stripeCoupon},
			"coupon/getAll":   list("/coupons", ""),
			"customer/create": {method: "POST", path: "/customers", form: true, body: "name", build: stripeFields("additionalFields", nil)},
			"customer/delete": {method: "DELETE", path: "/customers/{customerId}"},
			"customer/get":    {path: "/customers/{customerId}"},
			"customer/getAll": list("/customers", "@filters email"),
			"customer/update": {method: "POST", path: "/customers/{customerId}", form: true, build: stripeFields("updateFields", nil)},
			"source/create":   {method: "POST", form: true, run: stripeSourceCreate},
			"source/delete":   {method: "DELETE", path: "/customers/{customerId}/sources/{sourceId}"},
			"source/get":      {path: "/sources/{sourceId}"},
			"token/create": {method: "POST", path: "/tokens", form: true,
				body: "number:card.number expirationMonth:card.exp_month expirationYear:card.exp_year cvc:card.cvc",
				build: func(ec *execCtx, i int, r *restRequest) error {
					if t, _ := ec.str(i, "type", "cardToken"); t != "cardToken" {
						return permanentError{"Only card token creation implemented."}
					}
					return nil
				}},
			"meterEvent/create": {method: "POST", path: "/billing/meter_events", form: true,
				body: "eventName:event_name customerId:payload.stripe_customer_id value:payload.value @additionalFields identifier timestamp:timestamp:sec",
				build: func(ec *execCtx, i int, r *restRequest) error {
					p := asMap(r.body["payload"])
					if p == nil {
						p = map[string]interface{}{}
					}
					if _, ok := p["value"]; !ok {
						p["value"] = 1 // the parameter's default
					}
					for _, x := range asList(asMap(fieldsOf(ec, i, "additionalFields")["customPayload"])["properties"]) {
						m := asMap(x)
						if k := toString(m["key"]); k != "stripe_customer_id" && k != "value" {
							p[k] = m["value"]
						}
					}
					r.body["payload"] = p
					return nil
				}},
		},
	}
	// Stripe bodies use bracket notation (metadata[k]=v, card[number]=…):
	// re-encode form bodies after the field lists and hooks ran.
	for _, op := range s.ops {
		if !op.form || op.run != nil {
			continue
		}
		prev := op.build
		op.build = func(ec *execCtx, i int, r *restRequest) error {
			if prev != nil {
				if err := prev(ec, i, r); err != nil {
					return err
				}
			}
			r.raw, r.contentType = []byte(stripeForm(r.body)), "application/x-www-form-urlencoded"
			return nil
		}
	}
	registerREST("stripe", s)
}

// stripeFields copies a collection into the body the way n8n's
// adjustChargeFields/adjustCustomerFields do: metadata pairs become an
// object, address.details and shipping.shippingProperties[0] are unwrapped.
func stripeFields(coll string, fix func(map[string]interface{})) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		for k, v := range fieldsOf(ec, i, coll) {
			switch k {
			case "metadata":
				md := map[string]interface{}{}
				for _, x := range asList(asMap(v)["metadataProperties"]) {
					m := asMap(x)
					md[toString(m["key"])] = m["value"]
				}
				if len(md) > 0 {
					r.body["metadata"] = md
				}
			case "address":
				if d, ok := asMap(v)["details"]; ok {
					r.body["address"] = d
				}
			case "shipping":
				sp := asList(asMap(v)["shippingProperties"])
				if len(sp) == 0 {
					continue
				}
				s := map[string]interface{}{}
				for sk, sv := range asMap(sp[0]) {
					if sk == "address" {
						sv = asMap(sv)["details"]
					}
					s[sk] = sv
				}
				r.body["shipping"] = s
			default:
				r.body[k] = v
			}
		}
		if coll == "updateFields" && len(r.body) == 0 {
			return permanentError{ec.node.Name + ": Please enter at least one field to update"}
		}
		if fix != nil {
			fix(r.body)
		}
		return nil
	}
}

func stripeCoupon(ec *execCtx, i int, r *restRequest) error {
	if t, _ := ec.str(i, "type", "percent"); t == "fixedAmount" {
		r.body["amount_off"], _ = ec.param(i, "amountOff", 0)
		r.body["currency"], _ = ec.param(i, "currency", "")
	} else {
		r.body["percent_off"], _ = ec.param(i, "percentOff", 1)
	}
	return nil
}

// stripeSourceCreate creates a source and attaches it to the customer.
func stripeSourceCreate(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	op := &restOp{method: "POST", path: "/sources", form: true, body: "type amount currency", build: stripeFields("additionalFields", nil)}
	req, err := op.request(ec, i)
	if err != nil {
		return nil, err
	}
	req.raw = []byte(stripeForm(req.body))
	v, err := c.do(ec, req)
	if err != nil {
		return nil, err
	}
	attach := &restOp{method: "POST", path: "/customers/{customerId}/sources", form: true}
	ar, err := attach.request(ec, i)
	if err != nil {
		return nil, err
	}
	ar.raw = []byte(stripeForm(map[string]interface{}{"source": asMap(v)["id"]}))
	if _, err := c.do(ec, ar); err != nil {
		return nil, err
	}
	return restItems(v), nil
}

// stripeForm encodes a body like the request library's form option
// (qs.stringify): nested objects as a[b]=…, lists as a[0]=….
func stripeForm(body map[string]interface{}) string {
	var parts []string
	var walk func(prefix string, v interface{})
	walk = func(prefix string, v interface{}) {
		switch t := v.(type) {
		case map[string]interface{}:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(prefix+"["+k+"]", t[k])
			}
		case []interface{}:
			for n, x := range t {
				walk(prefix+"["+strconv.Itoa(n)+"]", x)
			}
		case nil:
			parts = append(parts, url.QueryEscape(prefix)+"=")
		default:
			parts = append(parts, url.QueryEscape(prefix)+"="+url.QueryEscape(toString(t)))
		}
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		walk(k, body[k])
	}
	return strings.Join(parts, "&")
}
