package n8n

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// shopifyFake is a fake Admin API that can send Link headers; auth records
// Authorization or X-Shopify-Access-Token.
func shopifyFake(t *testing.T) *fakeAPI {
	f := &fakeAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		auth := firstNonEmpty(r.Header.Get("Authorization"), r.Header.Get("X-Shopify-Access-Token"))
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(b), auth, r.Header.Get("Content-Type")})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var out interface{}
		switch {
		case r.Method == "POST" && r.URL.Path == "/orders.json":
			out = map[string]interface{}{"order": map[string]interface{}{"id": 1001}}
		case r.Method == "GET" && r.URL.Path == "/orders/1001.json":
			out = map[string]interface{}{"order": map[string]interface{}{"id": 1001, "name": "#1001"}}
		case r.Method == "GET" && r.URL.Path == "/products.json":
			if r.URL.Query().Get("page_info") == "" {
				w.Header().Set("Link", `<`+f.srv.URL+`/products.json?limit=250&page_info=p2>; rel="next"`)
				out = map[string]interface{}{"products": []interface{}{map[string]interface{}{"id": 1}, map[string]interface{}{"id": 2}}}
			} else {
				w.Header().Set("Link", `<`+f.srv.URL+`/products.json?page_info=p1>; rel="previous"`)
				out = map[string]interface{}{"products": []interface{}{map[string]interface{}{"id": 3}}}
			}
		case r.Method == "PUT" && r.URL.Path == "/products/9.json":
			out = map[string]interface{}{"product": map[string]interface{}{"id": 9}}
		case r.Method == "DELETE":
			out = map[string]interface{}{}
		default:
			w.WriteHeader(404)
			out = map[string]interface{}{"errors": r.URL.Path}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestShopifyNode(t *testing.T) {
	f := shopifyFake(t)
	basic := map[string]map[string]interface{}{"shopifyApi": {"apiKey": "k", "password": "p", "shopSubdomain": "shop"}}

	// Default resource/operation: order/create, with n8n's limeItemsUi.
	out := restRun(t, f, "shopify", "shopify", map[string]interface{}{
		"limeItemsUi": map[string]interface{}{"lineItemValues": []interface{}{map[string]interface{}{"productId": "55", "quantity": 2}}},
		"additionalFields": map[string]interface{}{"email": "a@b.c", "shippingAddressUi": map[string]interface{}{
			"shippingAddressValues": map[string]interface{}{"firstName": "Ada", "city": "Paris"}}},
	}, basic)
	if len(out) != 1 || out[0]["id"] != float64(1001) {
		t.Fatalf("order create: %v", out)
	}
	r := f.find(t, "POST", "/orders.json")
	if r.auth != "Basic azpw" {
		t.Fatalf("auth %q", r.auth)
	}
	o := asMap(jsonBody(t, r)["order"])
	if o["test"] != true || o["email"] != "a@b.c" || mustJSON(o["line_items"]) != `[{"product_id":"55","quantity":2}]` ||
		mustJSON(o["shipping_address"]) != `{"city":"Paris","first_name":"Ada"}` {
		t.Fatalf("order body: %v", o)
	}

	tok := map[string]map[string]interface{}{"shopifyAccessTokenApi": {"accessToken": "shpat_1", "shopSubdomain": "shop"}}
	out = restRun(t, f, "shopify", "shopify", map[string]interface{}{"authentication": "accessToken", "operation": "get", "orderId": "1001",
		"options": map[string]interface{}{"fields": "id,name"}}, tok)
	if out[0]["name"] != "#1001" {
		t.Fatalf("order get: %v", out)
	}
	if r := f.find(t, "GET", "/orders/1001.json"); r.auth != "shpat_1" || r.query != "fields=id%2Cname" {
		t.Fatalf("order get request: %+v", r)
	}

	// Link-header pagination.
	out = restRun(t, f, "shopify", "shopify", map[string]interface{}{"authentication": "accessToken", "resource": "product", "operation": "getAll",
		"returnAll": true, "additionalFields": map[string]interface{}{"vendor": "Acme"}}, tok)
	if len(out) != 3 || out[2]["id"] != float64(3) {
		t.Fatalf("product getAll: %v", out)
	}
	if q := f.find(t, "GET", "/products.json").query; !strings.Contains(q, "vendor=Acme") || !strings.Contains(q, "limit=250") {
		t.Fatalf("product getAll query %q", q)
	}
	out = restRun(t, f, "shopify", "shopify", map[string]interface{}{"authentication": "accessToken", "resource": "product", "operation": "getAll", "limit": 1}, tok)
	if len(out) != 1 {
		t.Fatalf("product getAll limit: %v", out)
	}

	out = restRun(t, f, "shopify", "shopify", map[string]interface{}{"authentication": "accessToken", "resource": "product", "operation": "update", "productId": "9",
		"updateFields": map[string]interface{}{"title": "New", "productOptions": map[string]interface{}{"option": []interface{}{map[string]interface{}{"name": "Color", "value": "Red"}}}}}, tok)
	if out[0]["id"] != float64(9) {
		t.Fatalf("product update: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "PUT", "/products/9.json"))); b != `{"product":{"options":{"Color":"Red"},"title":"New"}}` {
		t.Fatalf("product update body %s", b)
	}

	out = restRun(t, f, "shopify", "shopify", map[string]interface{}{"authentication": "accessToken", "resource": "product", "operation": "delete", "productId": "9"}, tok)
	if out[0]["success"] != true {
		t.Fatalf("product delete: %v", out)
	}
}
