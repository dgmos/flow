package n8n

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// restRunFileInput runs Start -> Set -> Convert to File -> service node, so
// the service node gets an item with binary "data" (content as fileName).
func restRunFileInput(t *testing.T, f *fakeAPI, apiKey, nodeType string, params map[string]interface{}, creds map[string]map[string]interface{}, content, fileName, mime string) []Item {
	t.Helper()
	sc := NewStaticCredentials()
	var credType string
	for k, v := range creds {
		sc.Set(k, "*", v)
		credType = k
	}
	h := newHarness(t, &Options{Credentials: sc, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{apiKey: f.srv.URL}})
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		nd("Seed", "set", 3.4, map[string]interface{}{"assignments": map[string]interface{}{"assignments": []interface{}{
			assign("b", "string", base64.StdEncoding.EncodeToString([]byte(content)))}}}),
		nd("Bin", "convertToFile", 1.1, map[string]interface{}{"operation": "toBinary", "sourceProperty": "b", "options": map[string]interface{}{"fileName": fileName, "mimeType": mime}}),
		credNode(map[string]interface{}{"name": "Svc", "type": "n8n-nodes-base." + nodeType, "typeVersion": 1, "parameters": params}, credType),
	}, "Start->Seed", "Seed->Bin", "Bin->Svc")
	_, steps := h.run(t, data, "")
	if steps["Svc"].Error != "" {
		t.Fatalf("node failed: %s", steps["Svc"].Error)
	}
	return decodeOutput(steps["Svc"].Output)[0]
}

func TestSharePointNode(t *testing.T) {
	var f *fakeAPI
	f = newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer sp-tok" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "PUT" && r.path == "/sites/S1/drive/items/F1:/report.txt:/content":
			return 201, "application/json", map[string]interface{}{"id": "new", "name": "report.txt"}
		case r.method == "GET" && r.path == "/sites/S1/lists/L1/items" && strings.Contains(r.query, "filter"):
			if strings.Contains(r.query, "skiptoken") {
				return 200, "application/json", map[string]interface{}{"value": []interface{}{map[string]interface{}{"id": "3"}}}
			}
			if strings.Contains(r.query, "Email") {
				return 200, "application/json", map[string]interface{}{"value": []interface{}{map[string]interface{}{"id": "7"}}}
			}
			return 200, "application/json", map[string]interface{}{"value": []interface{}{
				map[string]interface{}{"id": "1", "@odata.etag": "x", "fields": map[string]interface{}{"Title": "a", "@odata.etag": "y"}},
				map[string]interface{}{"id": "2"}}, "@odata.nextLink": f.srv.URL + "/sites/S1/lists/L1/items?$filter=x&$skiptoken=2"}
		case r.method == "PATCH" && r.path == "/sites/S1/lists/L1/items/7":
			return 200, "application/json", map[string]interface{}{"id": "7"}
		case r.method == "POST" && r.path == "/sites/S1/lists/L1/items":
			return 201, "application/json", map[string]interface{}{"id": "9"}
		case r.method == "DELETE":
			return 204, "application/json", ""
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"microsoftSharePointOAuth2Api": {"subdomain": "contoso", "oauthTokenData": map[string]interface{}{"access_token": "sp-tok"}}}
	rl := func(v string) map[string]interface{} {
		return map[string]interface{}{"__rl": true, "mode": "id", "value": v}
	}

	// file/upload sends the binary body.
	items := restRunFileInput(t, f, "microsoftSharePoint", "microsoftSharePoint", map[string]interface{}{"operation": "upload", "site": rl("S1"), "folder": rl("F1"),
		"fileName": "report.txt", "fileContents": "data"}, cred, "hello", "r.txt", "text/plain")
	if items[0].JSON["id"] != "new" {
		t.Fatalf("upload: %v", items[0].JSON)
	}
	if r := f.find(t, "PUT", "/content"); r.body != "hello" || r.ctype != "text/plain" {
		t.Fatalf("upload request %+v", r)
	}

	// item/getAll (default op) with simplify and nextLink paging.
	out := restRun(t, f, "microsoftSharePoint", "microsoftSharePoint", map[string]interface{}{"resource": "item", "site": rl("S1"), "list": rl("L1"),
		"filter": "fields/Title eq 'a'", "returnAll": true}, cred)
	if len(out) != 3 || out[0]["@odata.etag"] != nil || asMap(out[0]["fields"])["@odata.etag"] != nil {
		t.Fatalf("getAll: %v", out)
	}
	if q := f.find(t, "GET", "/sites/S1/lists/L1/items").query; !strings.Contains(q, "%24select=id") || !strings.Contains(q, "%24expand=fields") {
		t.Fatalf("getAll query %q", q)
	}
	out = restRun(t, f, "microsoftSharePoint", "microsoftSharePoint", map[string]interface{}{"resource": "item", "operation": "getAll", "site": rl("S1"), "list": rl("L1"),
		"filter": "x", "limit": 1}, cred)
	if len(out) != 1 {
		t.Fatalf("getAll limit: %v", out)
	}

	// item/update matches by a column, then patches that item.
	out = restRun(t, f, "microsoftSharePoint", "microsoftSharePoint", map[string]interface{}{"resource": "item", "operation": "update", "site": rl("S1"), "list": rl("L1"),
		"columns": map[string]interface{}{"mappingMode": "defineBelow", "value": map[string]interface{}{"Email": "a@b.c", "Title": "T"}, "matchingColumns": []interface{}{"Email"},
			"schema": []interface{}{map[string]interface{}{"id": "Email", "type": "string"}}}}, cred)
	if out[0]["id"] != "7" {
		t.Fatalf("update: %v", out)
	}
	if b := mustJSON(jsonBody(t, f.find(t, "PATCH", "/items/7"))); b != `{"fields":{"Email":"a@b.c","Title":"T"}}` {
		t.Fatalf("update body %s", b)
	}

	// item/create, item/delete.
	out = restRun(t, f, "microsoftSharePoint", "microsoftSharePoint", map[string]interface{}{"resource": "item", "operation": "create", "site": rl("S1"), "list": rl("L1"),
		"columns": map[string]interface{}{"mappingMode": "defineBelow", "value": map[string]interface{}{"Title": "New"}}}, cred)
	if out[0]["id"] != "9" {
		t.Fatalf("create: %v", out)
	}
	out = restRun(t, f, "microsoftSharePoint", "microsoftSharePoint", map[string]interface{}{"resource": "item", "operation": "delete", "site": rl("S1"), "list": rl("L1"), "item": rl("9")}, cred)
	if out[0]["deleted"] != true {
		t.Fatalf("delete: %v", out)
	}
}
