package n8n

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Shopify (n8n-nodes-base.shopify), Admin REST API 2024-07.

func init() {
	orderListQuery := "@options fields attributionAppId:attribution_app_id createdAtMin:created_at_min createdAtMax:created_at_max " +
		"updatedAtMin:updated_at_min updatedAtMax:updated_at_max processedAtMin:processed_at_min processedAtMax:processed_at_max " +
		"sinceId:since_id ids status financialStatus:financial_status fulfillmentStatus:fulfillment_status"
	registerREST("shopify", &restService{
		defResource: "order",
		defOps:      map[string]string{"order": "create", "product": "create"},
		client:      shopifyClient,
		pager:       linkHeaderPager,
		ops: map[string]*restOp{
			"order/create": {method: "POST", path: "/orders.json", wrap: "order", out: "order",
				body: "@additionalFields fulfillmentStatus:fulfillment_status inventoryBehaviour:inventory_behaviour locationId:location_id note " +
					"sendFulfillmentReceipt:send_fulfillment_receipt sendReceipt:send_receipt sourceName:source_name tags email",
				build: shopifyOrderBody},
			"order/delete": {method: "DELETE", path: "/orders/{orderId}.json", success: true},
			"order/get":    {path: "/orders/{orderId}.json", query: "@options fields", out: "order"},
			"order/getAll": {path: "/orders.json", list: true, listKey: "orders", query: orderListQuery, sizeParam: "limit", maxSize: 250},
			"order/update": {method: "PUT", path: "/orders/{orderId}.json", wrap: "order", out: "order",
				body: "@updateFields locationId:location_id note sourceName:source_name tags email", build: shopifyShipping("updateFields")},
			"product/create": {method: "POST", path: "/products.json", wrap: "product", out: "product", body: "@additionalFields * @ title",
				build: shopifyProductOptions("additionalFields")},
			"product/delete": {method: "DELETE", path: "/products/{productId}.json", success: true},
			"product/get":    {path: "/products/{productId}.json", query: "@additionalFields *", out: "product"},
			"product/getAll": {path: "/products.json", list: true, listKey: "products", query: "@additionalFields *", sizeParam: "limit", maxSize: 250},
			"product/update": {method: "PUT", path: "/products/{productId}.json", wrap: "product", out: "product", body: "@updateFields *",
				build: shopifyProductOptions("updateFields")},
		},
	})
}

// shopifyClient authenticates with the credential picked by the node's
// authentication parameter (n8n's default is apiKey); the shop subdomain
// comes from the credential.
func shopifyClient(ec *execCtx, i int) (*restClient, error) {
	types := map[string]string{"accessToken": "shopifyAccessTokenApi", "oAuth2": "shopifyOAuth2Api", "apiKey": "shopifyApi"}
	c, _, err := ec.restCred(i, "shopifyApi", types)
	if err != nil {
		// The default (apiKey) may not match the attached credential.
		if a, _ := ec.str(i, "authentication", ""); a == "" {
			for _, t := range []string{"shopifyAccessTokenApi", "shopifyOAuth2Api"} {
				if c2, err2 := ec.credential(t); err2 == nil {
					c, err = c2, nil
					break
				}
			}
		}
		if err != nil {
			return nil, err
		}
	}
	shop := strings.TrimSuffix(strings.TrimSpace(credString(c, "shopSubdomain")), ".myshopify.com")
	base := ec.apiBase("shopify", fmt.Sprintf("https://%s.myshopify.com/admin/api/2024-07", shop))
	h := map[string]string{}
	if tok := accessToken(c); tok != "" {
		h["X-Shopify-Access-Token"] = tok
	} else {
		h["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(credString(c, "apiKey")+":"+credString(c, "password")))
	}
	return &restClient{base: base, headers: h}, nil
}

// snakeKeys converts an object's keys to snake_case (n8n's keysToSnakeCase).
func snakeKeys(v interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, x := range asMap(v) {
		out[snakeCase(k)] = x
	}
	return out
}

func shopifyOrderBody(ec *execCtx, i int, r *restRequest) error {
	// limeItemsUi: n8n's parameter name (sic).
	v, err := ec.param(i, "limeItemsUi.lineItemValues", nil)
	if err != nil {
		return err
	}
	var lines []interface{}
	for _, l := range asList(v) {
		lines = append(lines, snakeKeys(l))
	}
	if len(lines) == 0 {
		return permanentError{ec.node.Name + ": At least one line item has to be added"}
	}
	r.body["line_items"] = lines
	// n8n starts from test: true and can only set it to true again, so every
	// order it creates is a test order.
	r.body["test"] = true
	f := fieldsOf(ec, i, "additionalFields")
	if d := asMap(f["discountCodesUi"]); len(d) > 0 {
		r.body["discount_codes"] = d["discountCodesValues"]
	}
	if b := asMap(f["billingAddressUi"]); len(b) > 0 {
		r.body["billing_address"] = snakeKeys(b["billingAddressValues"])
	}
	return shopifyShipping("additionalFields")(ec, i, r)
}

func shopifyShipping(coll string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if s := asMap(fieldsOf(ec, i, coll)["shippingAddressUi"]); len(s) > 0 {
			r.body["shipping_address"] = snakeKeys(s["shippingAddressValues"])
		}
		return nil
	}
}

// shopifyProductOptions turns productOptions.option [{name, value}] into
// options {name: value}, as n8n does.
func shopifyProductOptions(coll string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		po, ok := r.body["productOptions"]
		if !ok {
			return nil
		}
		delete(r.body, "productOptions")
		opts := map[string]interface{}{}
		for _, o := range asList(asMap(po)["option"]) {
			m := asMap(o)
			opts[toString(m["name"])] = m["value"]
		}
		r.body["options"] = opts
		return nil
	}
}
