package n8n

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// Each case runs one node against a fake API and checks the request it
// sent and the items it produced.
func TestRESTIntegrations(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		p := r.path
		switch {
		case strings.HasSuffix(p, "/contents/docs/a.md") && r.method == "GET":
			return 200, "application/json", map[string]interface{}{"sha": "SHA1", "name": "a.md", "content": base64.StdEncoding.EncodeToString([]byte("# hi"))}
		case strings.HasSuffix(p, "/pulls"):
			return 200, "application/json", []interface{}{map[string]interface{}{"number": 1}, map[string]interface{}{"number": 2}}
		case strings.HasPrefix(p, "/api/v1/search"):
			return 200, "application/json", map[string]interface{}{"hits": []interface{}{map[string]interface{}{"title": "Go 2"}}, "nbPages": 1}
		case strings.HasPrefix(p, "/r/golang/search.json"):
			return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"children": []interface{}{map[string]interface{}{"data": map[string]interface{}{"title": "post"}}}}}
		case strings.HasSuffix(p, "/Messages.json"):
			return 201, "application/json", map[string]interface{}{"sid": "SM1"}
		case strings.HasPrefix(p, "/data/2.5/weather"):
			return 200, "application/json", map[string]interface{}{"name": "Izmir", "main": map[string]interface{}{"temp": 21}}
		case strings.HasSuffix(p, "/api/user/token-auth/"):
			return 200, "application/json", map[string]interface{}{"token": "JWT1"}
		case strings.HasPrefix(p, "/api/database/fields/table/7/"):
			return 200, "application/json", []interface{}{map[string]interface{}{"id": 11, "name": "Name"}}
		case strings.HasPrefix(p, "/api/database/rows/table/7/") && r.method == "GET":
			return 200, "application/json", map[string]interface{}{"results": []interface{}{map[string]interface{}{"id": 1, "field_11": "Ada"}}, "next": nil}
		case strings.HasPrefix(p, "/api/database/rows/table/7/") && r.method == "POST":
			return 200, "application/json", map[string]interface{}{"id": 2, "field_11": "Bob"}
		case p == "/jsonrpc" && strings.Contains(r.body, `"login"`):
			return 200, "application/json", map[string]interface{}{"result": 5}
		case p == "/jsonrpc":
			return 200, "application/json", map[string]interface{}{"result": []interface{}{map[string]interface{}{"id": 9, "name": "Deal"}}}
		case strings.HasPrefix(p, "/rest/api/2/search"):
			return 200, "application/json", map[string]interface{}{"issues": []interface{}{map[string]interface{}{"key": "P-1"}}, "total": 1}
		case strings.HasSuffix(p, "/comment") && r.method == "POST":
			return 201, "application/json", map[string]interface{}{"id": "c1"}
		case strings.HasPrefix(p, "/ghost/api/content/posts/"):
			return 200, "application/json", map[string]interface{}{"posts": []interface{}{map[string]interface{}{"id": "g1", "title": "Hello"}}}
		case strings.HasSuffix(p, "/files/upload"):
			return 200, "application/json", map[string]interface{}{"id": "id:dbx", "name": "n.txt"}
		case strings.Contains(p, "/tasks/v1/lists/"):
			return 200, "application/json", map[string]interface{}{"items": []interface{}{map[string]interface{}{"title": "T1"}}}
		case strings.HasSuffix(p, ":runReport"):
			return 200, "application/json", map[string]interface{}{"dimensionHeaders": []interface{}{map[string]interface{}{"name": "country"}},
				"metricHeaders": []interface{}{map[string]interface{}{"name": "activeUsers", "type": "TYPE_INTEGER"}},
				"rows":          []interface{}{map[string]interface{}{"dimensionValues": []interface{}{map[string]interface{}{"value": "TR"}}, "metricValues": []interface{}{map[string]interface{}{"value": "42"}}}}, "rowCount": 1}
		case strings.HasSuffix(p, "/channels") && r.method == "POST":
			return 201, "application/json", map[string]interface{}{"id": "ch1"}
		case strings.HasSuffix(p, "/usedRange"):
			return 200, "application/json", map[string]interface{}{"address": "Sheet1!A1:B2", "values": []interface{}{[]interface{}{"name", "qty"}, []interface{}{"x", 1}}}
		case strings.HasPrefix(p, "/bkt/") && r.method == "PUT":
			return 200, "application/xml", ""
		case strings.HasPrefix(p, "/wp-json/wc/v3/customers"):
			return 200, "application/json", []interface{}{map[string]interface{}{"id": 3, "email": "c@x.com"}}
		case strings.HasPrefix(p, "/api/v1/db/data/noco/"):
			return 200, "application/json", map[string]interface{}{"Id": 8}
		}
		return 200, "application/json", map[string]interface{}{"ok": true}
	})
	u := f.srv.URL
	creds := NewStaticCredentials()
	for typ, c := range map[string]map[string]interface{}{
		"githubApi":               {"accessToken": "gh", "server": u},
		"redditOAuth2Api":         {"oauthTokenData": map[string]interface{}{"access_token": "rd"}},
		"twilioApi":               {"accountSid": "AC1", "authToken": "tok"},
		"openWeatherMapApi":       {"accessToken": "ow"},
		"baserowApi":              {"host": u, "username": "u@x.com", "password": "p"},
		"odooApi":                 {"url": u, "username": "u", "password": "p", "db": "db1"},
		"jiraSoftwareCloudApi":    {"domain": u, "email": "e@x.com", "apiToken": "jt"},
		"ghostContentApi":         {"url": u, "apiKey": "ck"},
		"dropboxApi":              {"accessToken": "dbx"},
		"googleTasksOAuth2Api":    {"oauthTokenData": map[string]interface{}{"access_token": "gt"}},
		"googleAnalyticsOAuth2":   {"oauthTokenData": map[string]interface{}{"access_token": "ga"}},
		"microsoftTeamsOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "ms"}},
		"microsoftExcelOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "ms"}},
		"s3":                      {"endpoint": u, "region": "eu-west-1", "accessKeyId": "AKID", "secretAccessKey": "SECRET", "forcePathStyle": true},
		"aws":                     {"region": "eu-west-1", "accessKeyId": "AKID", "secretAccessKey": "SECRET", "customEndpoints": true, "s3Endpoint": u},
		"wooCommerceApi":          {"url": u, "consumerKey": "ck", "consumerSecret": "cs"},
		"nocoDbApiToken":          {"host": u, "apiToken": "nt"},
	} {
		creds.Set(typ, "*", c)
	}
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}},
		APIBaseURLs: map[string]string{"hackernews": u, "reddit": u, "twilio": u, "openweathermap": u, "dropbox": u, "dropboxcontent": u,
			"tasks": u, "analyticsdata": u, "graph": u}})
	cases := []struct {
		name, typ, cred string
		params          map[string]interface{}
		method, path    string
		bodyHas         string
		check           func(items []map[string]interface{}) bool
	}{
		{"github edit", "github", "githubApi", map[string]interface{}{"resource": "file", "operation": "edit", "owner": "o", "repository": "r", "filePath": "docs/a.md", "fileContent": "new", "commitMessage": "m"},
			"PUT", "/repos/o/r/contents/docs/a.md", `"sha":"SHA1"`, nil},
		{"github prs", "github", "githubApi", map[string]interface{}{"resource": "repository", "operation": "getPullRequests", "owner": "o", "repository": "r", "returnAll": true},
			"GET", "/repos/o/r/pulls", "", func(it []map[string]interface{}) bool { return len(it) == 2 }},
		{"hacker news", "hackerNews", "", map[string]interface{}{"resource": "all", "limit": 5, "additionalFields": map[string]interface{}{"keyword": "go"}},
			"GET", "/api/v1/search", "", func(it []map[string]interface{}) bool { return len(it) == 1 && it[0]["title"] == "Go 2" }},
		{"reddit search", "reddit", "redditOAuth2Api", map[string]interface{}{"operation": "search", "subreddit": "golang", "keyword": "generics", "limit": 5},
			"GET", "/r/golang/search.json", "", func(it []map[string]interface{}) bool { return len(it) == 1 }},
		{"twilio sms", "twilio", "twilioApi", map[string]interface{}{"from": "+1", "to": "+2", "message": "hi"},
			"POST", "/2010-04-01/Accounts/AC1/Messages.json", "Body=hi", nil},
		{"weather", "openWeatherMap", "openWeatherMapApi", map[string]interface{}{"cityName": "Izmir"},
			"GET", "/data/2.5/weather", "", func(it []map[string]interface{}) bool { return it[0]["name"] == "Izmir" }},
		{"baserow list", "baserow", "baserowApi", map[string]interface{}{"databaseId": 1, "tableId": 7, "returnAll": true},
			"GET", "/api/database/rows/table/7/", "", func(it []map[string]interface{}) bool { return it[0]["Name"] == "Ada" }},
		{"baserow create", "baserow", "baserowApi", map[string]interface{}{"operation": "create", "databaseId": 1, "tableId": 7, "fieldsUi": map[string]interface{}{"fieldValues": []interface{}{map[string]interface{}{"fieldId": 11, "fieldValue": "Bob"}}}},
			"POST", "/api/database/rows/table/7/", `"field_11":"Bob"`, func(it []map[string]interface{}) bool { return it[0]["Name"] == "Bob" }},
		{"odoo", "odoo", "odooApi", map[string]interface{}{"resource": "opportunity", "operation": "getAll", "returnAll": true},
			"POST", "/jsonrpc", `"crm.lead"`, func(it []map[string]interface{}) bool { return it[0]["name"] == "Deal" }},
		{"jira search", "jira", "jiraSoftwareCloudApi", map[string]interface{}{"operation": "getAll", "limit": 10, "options": map[string]interface{}{"jql": "project = P"}},
			"GET", "/rest/api/2/search", "", func(it []map[string]interface{}) bool { return it[0]["key"] == "P-1" }},
		{"jira comment", "jira", "jiraSoftwareCloudApi", map[string]interface{}{"resource": "issueComment", "issueKey": "P-1", "comment": "done", "options": map[string]interface{}{}},
			"POST", "/rest/api/3/issue/P-1/comment", `"type":"doc"`, nil},
		{"ghost", "ghost", "ghostContentApi", map[string]interface{}{"operation": "getAll", "limit": 5},
			"GET", "/ghost/api/content/posts/", "", func(it []map[string]interface{}) bool { return it[0]["title"] == "Hello" }},
		{"dropbox", "dropbox", "dropboxApi", map[string]interface{}{"path": "/n.txt", "fileContent": "note"},
			"POST", "/2/files/upload", "note", nil},
		{"tasks", "googleTasks", "googleTasksOAuth2Api", map[string]interface{}{"operation": "getAll", "task": "L1", "returnAll": true},
			"GET", "/tasks/v1/lists/L1/tasks", "", func(it []map[string]interface{}) bool { return it[0]["title"] == "T1" }},
		{"analytics", "googleAnalytics", "googleAnalyticsOAuth2", map[string]interface{}{"propertyId": map[string]interface{}{"__rl": true, "mode": "id", "value": "123"},
			"metricsGA4":    map[string]interface{}{"metricValues": []interface{}{map[string]interface{}{"listName": "activeUsers"}}},
			"dimensionsGA4": map[string]interface{}{"dimensionValues": []interface{}{map[string]interface{}{"listName": "country"}}}},
			"POST", "/v1beta/properties/123:runReport", `"7daysAgo"`, func(it []map[string]interface{}) bool {
				return it[0]["country"] == "TR" && it[0]["activeUsers"] == 42.0
			}},
		{"teams", "microsoftTeams", "microsoftTeamsOAuth2Api", map[string]interface{}{"teamId": "T1", "name": "general2"},
			"POST", "/teams/T1/channels", `"displayName":"general2"`, nil},
		{"excel append", "microsoftExcel", "microsoftExcelOAuth2Api", map[string]interface{}{"resource": "worksheet", "operation": "append", "dataMode": "autoMap",
			"workbook": map[string]interface{}{"__rl": true, "mode": "id", "value": "WB"}, "worksheet": map[string]interface{}{"__rl": true, "mode": "id", "value": "WS"}},
			"PATCH", "/me/drive/items/WB/workbook/worksheets/WS/range(address='A3:B3')", `"y"`, nil},
		{"s3 upload", "s3", "s3", map[string]interface{}{"operation": "upload", "bucketName": "bkt", "fileName": "a/b.txt", "binaryData": false, "fileContent": "hello"},
			"PUT", "/bkt/a/b.txt", "hello", nil},
		{"aws s3 upload", "awsS3", "aws", map[string]interface{}{"operation": "upload", "bucketName": "bkt", "fileName": "c.txt", "binaryData": false, "fileContent": "hi"},
			"PUT", "/bkt/c.txt", "hi", nil},
		{"aws s3 copy", "awsS3", "aws", map[string]interface{}{"operation": "copy", "sourcePath": "/bkt/c.txt", "destinationPath": "/bkt2/d.txt"},
			"PUT", "/bkt2/d.txt", "", nil},
		{"aws s3 bucket delete", "awsS3", "aws", map[string]interface{}{"resource": "bucket", "operation": "delete", "name": "old"},
			"DELETE", "/old", "", nil},
		{"woocommerce", "wooCommerce", "wooCommerceApi", map[string]interface{}{"resource": "customer", "operation": "getAll", "limit": 10},
			"GET", "/wp-json/wc/v3/customers", "", func(it []map[string]interface{}) bool { return it[0]["email"] == "c@x.com" }},
		{"nocodb", "nocoDb", "nocoDbApiToken", map[string]interface{}{"operation": "create", "projectId": "P1", "table": "T1", "fieldsUi": map[string]interface{}{"fieldValues": []interface{}{map[string]interface{}{"fieldName": "Title", "fieldValue": "x"}}}},
			"POST", "/api/v1/db/data/noco/P1/T1", `"Title":"x"`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.mu.Lock()
			f.reqs = nil
			f.mu.Unlock()
			n := nd("Node", c.typ, 1, c.params)
			if c.typ == "googleAnalytics" || c.typ == "microsoftExcel" || c.typ == "nocoDb" {
				n["typeVersion"] = map[string]float64{"googleAnalytics": 2, "microsoftExcel": 2.1, "nocoDb": 2}[c.typ]
			}
			if c.cred != "" {
				n = credNode(n, c.cred)
			}
			data := wfJSON(t, []map[string]interface{}{nd("Start", "manualTrigger", 1, nil),
				nd("Seed", "set", 3.4, map[string]interface{}{"mode": "raw", "jsonOutput": `{"name":"y","qty":2}`}), n}, "Start->Seed", "Seed->Node")
			_, rep, _ := Import(data, ImportOptions{})
			if !rep.Runnable {
				t.Fatalf("not runnable: %+v", rep.Blocking)
			}
			_, steps := h.run(t, data, "")
			if steps["Node"].Status != "COMPLETED" {
				t.Fatalf("node failed: %s", steps["Node"].Error)
			}
			found := false
			f.mu.Lock()
			for _, r := range f.reqs {
				if r.method == c.method && strings.HasPrefix(r.path, c.path) && strings.Contains(r.body+r.query, c.bodyHas) {
					found = true
				}
			}
			reqs := f.reqs
			f.mu.Unlock()
			if !found {
				t.Fatalf("no %s %s request containing %q; got %+v", c.method, c.path, c.bodyHas, reqs)
			}
			if c.check != nil && !c.check(itemsOf(t, steps["Node"], 0)) {
				t.Fatalf("unexpected output: %v", itemsOf(t, steps["Node"], 0))
			}
		})
	}
}

func TestRSSTriggerDHLAndN8NAPI(t *testing.T) {
	var feedItems []string
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch r.path {
		case "/feed.xml":
			return 200, "application/rss+xml", `<?xml version="1.0"?><rss><channel>` + strings.Join(feedItems, "") + `</channel></rss>`
		case "/track/shipments":
			return 200, "application/json", map[string]interface{}{"shipments": []interface{}{map[string]interface{}{"id": "JD1", "status": map[string]interface{}{"statusCode": "transit"}}}}
		case "/api/v1/workflows":
			if strings.Contains(r.query, "cursor=c2") {
				return 200, "application/json", map[string]interface{}{"data": []interface{}{map[string]interface{}{"id": "w2"}}}
			}
			return 200, "application/json", map[string]interface{}{"data": []interface{}{map[string]interface{}{"id": "w1"}}, "nextCursor": "c2"}
		}
		return 404, "application/json", map[string]interface{}{}
	})
	item := func(title, date string) string {
		return "<item><title>" + title + "</title><pubDate>" + date + "</pubDate></item>"
	}
	creds := NewStaticCredentials()
	creds.Set("dhlApi", "*", map[string]interface{}{"apiKey": "dk"})
	creds.Set("n8nApi", "*", map[string]interface{}{"apiKey": "nk", "baseUrl": f.srv.URL + "/api/v1"})
	h := newHarness(t, &Options{Credentials: creds, PollState: &memPollState{m: map[string]string{}}, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}},
		APIBaseURLs: map[string]string{"dhl": f.srv.URL}})

	rss := wfJSON(t, []map[string]interface{}{
		{"name": "Feed", "type": "n8n-nodes-base.rssFeedReadTrigger", "typeVersion": 1, "parameters": map[string]interface{}{"feedUrl": f.srv.URL + "/feed.xml",
			"pollTimes": map[string]interface{}{"item": []interface{}{map[string]interface{}{"mode": "everyX", "unit": "minutes", "value": 10}}}}},
		nd("Out", "noOp", 1, nil),
	}, "Feed->Out")
	h.fixedID = "rss-wf"
	feedItems = []string{item("old", "Mon, 02 Jan 2006 15:04:05 +0000")}
	if _, steps := h.run(t, rss, `{"__trigger":"Feed"}`); steps["Out"].Status != "SKIPPED" {
		t.Fatalf("first poll should only record the newest date: %+v", steps["Out"])
	}
	feedItems = append(feedItems, item("new", "Tue, 03 Jan 2006 15:04:05 +0000"))
	_, steps := h.run(t, rss, `{"__trigger":"Feed"}`)
	if got := itemsOf(t, steps["Out"], 0); len(got) != 1 || got[0]["title"] != "new" {
		t.Fatalf("second poll: %v", got)
	}
	h.fixedID = ""

	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		credNode(nd("DHL", "dhl", 1, map[string]interface{}{"trackingNumber": "JD1", "options": map[string]interface{}{}}), "dhlApi"),
		credNode(nd("N8N", "n8n", 1, map[string]interface{}{"filters": map[string]interface{}{"tags": "mcp"}, "requestOptions": map[string]interface{}{}}), "n8nApi"),
	}, "Start->DHL", "Start->N8N")
	_, steps = h.run(t, data, "")
	if got := itemsOf(t, steps["DHL"], 0); len(got) != 1 || got[0]["id"] != "JD1" {
		t.Fatalf("dhl: %v", got)
	}
	if got := itemsOf(t, steps["N8N"], 0); len(got) != 2 || got[1]["id"] != "w2" {
		t.Fatalf("n8n workflows (paginated): %v", got)
	}
	if r := f.find(t, "GET", "/api/v1/workflows"); !strings.Contains(r.query, "tags=mcp") {
		t.Fatalf("n8n filter: %s", r.query)
	}
}
