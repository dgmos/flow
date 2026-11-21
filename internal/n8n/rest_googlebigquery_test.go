package n8n

import (
	"strings"
	"testing"
)

func TestGoogleBigQueryNode(t *testing.T) {
	schema := map[string]interface{}{"fields": []interface{}{
		map[string]interface{}{"name": "name", "type": "STRING"},
		map[string]interface{}{"name": "n", "type": "INTEGER"},
		map[string]interface{}{"name": "ts", "type": "TIMESTAMP"},
		map[string]interface{}{"name": "tags", "type": "STRING", "mode": "REPEATED"},
	}}
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer bq-tok" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "POST" && r.path == "/v2/projects/proj/jobs":
			if strings.Contains(r.body, "slow") {
				return 200, "application/json", map[string]interface{}{"jobReference": map[string]interface{}{"jobId": "j2", "location": "EU"}, "status": map[string]interface{}{"state": "RUNNING"}}
			}
			return 200, "application/json", map[string]interface{}{"jobReference": map[string]interface{}{"jobId": "j1", "location": "US"}, "status": map[string]interface{}{"state": "DONE"}}
		case r.method == "GET" && r.path == "/v2/projects/proj/queries/j1":
			if strings.Contains(r.query, "pageToken=p2") {
				return 200, "application/json", map[string]interface{}{"schema": schema, "jobComplete": true,
					"rows": []interface{}{map[string]interface{}{"f": []interface{}{map[string]interface{}{"v": "b"}, map[string]interface{}{"v": "2"}, map[string]interface{}{"v": nil}, map[string]interface{}{"v": []interface{}{}}}}}}
			}
			return 200, "application/json", map[string]interface{}{"schema": schema, "jobComplete": true, "pageToken": "p2",
				"rows": []interface{}{map[string]interface{}{"f": []interface{}{map[string]interface{}{"v": "a"}, map[string]interface{}{"v": "1"},
					map[string]interface{}{"v": "1.7040672E9"}, map[string]interface{}{"v": []interface{}{map[string]interface{}{"v": "x"}}}}}}}
		case r.method == "GET" && r.path == "/v2/projects/proj/queries/j2":
			return 200, "application/json", map[string]interface{}{"jobComplete": true, "totalRows": "0"}
		case r.method == "GET" && r.path == "/v2/projects/proj/datasets/ds/tables/tb":
			return 200, "application/json", map[string]interface{}{"schema": schema}
		case r.method == "POST" && r.path == "/v2/projects/proj/datasets/ds/tables/tb/insertAll":
			return 200, "application/json", map[string]interface{}{"kind": "bigquery#tableDataInsertAllResponse"}
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"googleBigQueryOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "bq-tok"}}}
	rl := func(v string) map[string]interface{} {
		return map[string]interface{}{"__rl": true, "mode": "id", "value": v}
	}

	// Default executeQuery: job done at once, results paged and simplified.
	out := restRunV(t, f, "googleBigQuery", "googleBigQuery", 2.1, map[string]interface{}{"projectId": rl("proj"), "sqlQuery": "SELECT 1",
		"options": map[string]interface{}{"defaultDataset": "ds", "location": "US"}}, cred)
	if len(out) != 2 || out[0]["name"] != "a" || out[0]["n"] != "1" || out[0]["ts"] == "1.7040672E9" || mustJSON(out[0]["tags"]) != `["x"]` || out[1]["name"] != "b" {
		t.Fatalf("executeQuery: %v", out)
	}
	b := jsonBody(t, f.find(t, "POST", "/jobs"))
	q := asMap(asMap(b["configuration"])["query"])
	if q["query"] != "SELECT 1" || q["useLegacySql"] != false || mustJSON(q["defaultDataset"]) != `{"datasetId":"ds","projectId":"proj"}` {
		t.Fatalf("job body: %v", b)
	}
	if r := f.find(t, "GET", "/queries/j1"); !strings.Contains(r.query, "location=US") || !strings.Contains(r.query, "maxResults=1000") || !strings.Contains(r.query, "timeoutMs=10000") {
		t.Fatalf("results query %q", r.query)
	}

	// A running job is polled; no rows gives {success: true}.
	out = restRunV(t, f, "googleBigQuery", "googleBigQuery", 2, map[string]interface{}{"projectId": rl("proj"), "sqlQuery": "slow"}, cred)
	if len(out) != 1 || out[0]["success"] != true || !strings.Contains(f.find(t, "GET", "/queries/j2").query, "location=EU") {
		t.Fatalf("polled query: %v", out)
	}

	// insert with defined fields.
	out = restRunV(t, f, "googleBigQuery", "googleBigQuery", 2.1, map[string]interface{}{"operation": "insert", "projectId": rl("proj"), "datasetId": rl("ds"), "tableId": rl("tb"),
		"dataMode": "define", "fieldsUi": map[string]interface{}{"values": []interface{}{map[string]interface{}{"fieldId": "name", "fieldValue": "z"}, map[string]interface{}{"fieldId": "n", "fieldValue": ""}}},
		"options": map[string]interface{}{"skipInvalidRows": true}}, cred)
	if len(out) != 1 || out[0]["kind"] == nil {
		t.Fatalf("insert: %v", out)
	}
	ib := jsonBody(t, f.find(t, "POST", "/insertAll"))
	if mustJSON(ib["rows"]) != `[{"json":{"n":null,"name":"z"}}]` || ib["skipInvalidRows"] != true || ib["traceId"] == nil {
		t.Fatalf("insert body: %v", ib)
	}
}
