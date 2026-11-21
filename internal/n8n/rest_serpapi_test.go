package n8n

import "testing"

func TestSerpAPICommunityNode(t *testing.T) {
	const typ = "n8n-nodes-serpapi.serpApi"
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		if r.method != "GET" || r.path != "/search.json" || r.query.Get("api_key") != "sk" {
			t.Errorf("request %s %s %v", r.method, r.path, r.query)
		}
		return 200, "", map[string]interface{}{"search_metadata": map[string]interface{}{"status": "Success"}, "organic_results": []interface{}{map[string]interface{}{"title": "Go"}}}
	})
	bases := map[string]string{"serpApi": ""}
	cred := map[string]map[string]interface{}{"serpApi": {"apiKey": "sk"}}

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"q": "={{ 'golang' }}", "location": "Austin, Texas", "requestOptions": map[string]interface{}{},
		"additionalFields": map[string]interface{}{"gl": "us", "num": 20, "safe": ""}}, cred, nil)
	q := f.find(t, "GET", "/search.json").query
	if q.Get("engine") != "google" || q.Get("q") != "golang" || q.Get("location") != "Austin, Texas" || q.Get("gl") != "us" || q.Get("num") != "20" || q.Has("safe") || q.Has("requestOptions") {
		t.Fatalf("google query %v", q)
	}
	if len(out) != 1 || out[0].JSON["organic_results"] == nil {
		t.Fatalf("output %v", out)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "google_maps_reviews", "data_id": "0x1", "additionalFields": map[string]interface{}{"sort_by": "newestFirst"}}, cred, nil)
	if q := f.find(t, "GET", "/search.json").query; q.Get("engine") != "google_maps_reviews" || q.Get("data_id") != "0x1" || q.Get("sort_by") != "newestFirst" || q.Has("operation") {
		t.Fatalf("maps reviews query %v", q)
	}
	if cmtyRunnable(t, typ, map[string]interface{}{"operation": "altavista"}) {
		t.Fatal("unknown engine should block import")
	}
	if !aiSubNodeSupported("n8n-nodes-serpapi.serpApiTool") {
		t.Fatal("serpApiTool should be a supported AI tool")
	}
}
