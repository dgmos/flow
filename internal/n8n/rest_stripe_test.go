package n8n

import (
	"strings"
	"testing"
)

func TestStripeNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer sk_test_1" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "GET" && r.path == "/balance":
			return 200, "application/json", map[string]interface{}{"object": "balance"}
		case r.method == "POST" && r.path == "/customers":
			return 200, "application/json", map[string]interface{}{"id": "cus_1"}
		case r.method == "POST" && r.path == "/charges":
			return 200, "application/json", map[string]interface{}{"id": "ch_9"}
		case r.method == "GET" && r.path == "/charges":
			if strings.Contains(r.query, "starting_after=ch_2") {
				return 200, "application/json", map[string]interface{}{"has_more": false, "data": []interface{}{map[string]interface{}{"id": "ch_3"}}}
			}
			return 200, "application/json", map[string]interface{}{"has_more": true, "data": []interface{}{map[string]interface{}{"id": "ch_1"}, map[string]interface{}{"id": "ch_2"}}}
		case r.method == "DELETE" && r.path == "/customers/cus_1":
			return 200, "application/json", map[string]interface{}{"id": "cus_1", "deleted": true}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"stripeApi": {"secretKey": "sk_test_1"}}

	// Default: balance/get.
	out := restRun(t, f, "stripe", "stripe", map[string]interface{}{}, cred)
	if out[0]["object"] != "balance" {
		t.Fatalf("balance: %v", out)
	}

	out = restRun(t, f, "stripe", "stripe", map[string]interface{}{"resource": "customer", "operation": "create", "name": "Bob",
		"additionalFields": map[string]interface{}{
			"email":    "bob@example.com",
			"address":  map[string]interface{}{"details": map[string]interface{}{"city": "Oslo", "line1": "Main 1"}},
			"metadata": map[string]interface{}{"metadataProperties": []interface{}{map[string]interface{}{"key": "plan", "value": "pro"}}},
		}}, cred)
	if out[0]["id"] != "cus_1" {
		t.Fatalf("customer create: %v", out)
	}
	r := f.find(t, "POST", "/customers")
	if r.ctype != "application/x-www-form-urlencoded" ||
		r.body != "address%5Bcity%5D=Oslo&address%5Bline1%5D=Main+1&email=bob%40example.com&metadata%5Bplan%5D=pro&name=Bob" {
		t.Fatalf("customer create request: %+v", r)
	}

	out = restRun(t, f, "stripe", "stripe", map[string]interface{}{"resource": "charge", "operation": "create", "customerId": "cus_1", "amount": 500,
		"currency": "USD", "source": "tok_visa", "additionalFields": map[string]interface{}{"description": "x"}}, cred)
	if out[0]["id"] != "ch_9" {
		t.Fatalf("charge create: %v", out)
	}
	if b := f.find(t, "POST", "/charges").body; b != "amount=500&currency=usd&customer=cus_1&description=x&source=tok_visa" {
		t.Fatalf("charge body %q", b)
	}

	out = restRun(t, f, "stripe", "stripe", map[string]interface{}{"resource": "charge", "operation": "getAll", "returnAll": true}, cred)
	if len(out) != 3 || out[2]["id"] != "ch_3" {
		t.Fatalf("charge getAll: %v", out)
	}
	out = restRun(t, f, "stripe", "stripe", map[string]interface{}{"resource": "charge", "operation": "getAll", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("charge getAll limit: %v", out)
	}

	out = restRun(t, f, "stripe", "stripe", map[string]interface{}{"resource": "customer", "operation": "delete", "customerId": "cus_1"}, cred)
	if out[0]["deleted"] != true {
		t.Fatalf("customer delete: %v", out)
	}
}

func TestStripeForm(t *testing.T) {
	got := stripeForm(map[string]interface{}{"card": map[string]interface{}{"number": "4242", "exp_month": float64(1)}, "items": []interface{}{"a", "b"}})
	if got != "card%5Bexp_month%5D=1&card%5Bnumber%5D=4242&items%5B0%5D=a&items%5B1%5D=b" {
		t.Fatal(got)
	}
}
