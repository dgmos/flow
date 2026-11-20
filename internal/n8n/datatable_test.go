package n8n

import (
	"context"
	"testing"
)

func TestDataTableNode(t *testing.T) {
	store := NewMemoryDataTables()
	h := newHarness(t, &Options{DataTables: store})
	table := map[string]interface{}{"__rl": true, "mode": "list", "value": "tblFromAuthor", "cachedResultName": "Leads"}
	cond := func(col, op string, v interface{}) map[string]interface{} {
		return map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"keyName": col, "condition": op, "keyValue": v}}}
	}
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		nd("Rows", "set", 3.4, map[string]interface{}{"mode": "raw", "jsonOutput": `{"rows":[{"email":"a@x.io","score":5},{"email":"b@x.io","score":"12"}]}`}),
		nd("Split", "splitOut", 1, map[string]interface{}{"fieldToSplitOut": "rows"}),
		nd("Insert", "dataTable", 1.1, map[string]interface{}{"dataTableId": table, "columns": map[string]interface{}{"mappingMode": "autoMapInputData", "value": nil}}),
		nd("High", "dataTable", 1.1, map[string]interface{}{"operation": "get", "dataTableId": table, "matchType": "allConditions", "filters": cond("score", "gt", "10")}),
		nd("Upsert", "dataTable", 1.1, map[string]interface{}{"operation": "upsert", "dataTableId": table, "filters": cond("email", "eq", "c@x.io"),
			"columns": map[string]interface{}{"mappingMode": "defineBelow", "value": map[string]interface{}{"score": 7}}}),
		nd("Update", "dataTable", 1.1, map[string]interface{}{"operation": "update", "dataTableId": table, "filters": cond("email", "ilike", "A@X"),
			"columns": map[string]interface{}{"mappingMode": "defineBelow", "value": map[string]interface{}{"score": 6}}}),
		nd("New", "dataTable", 1.1, map[string]interface{}{"operation": "rowNotExists", "dataTableId": table, "filters": cond("email", "eq", "={{ $json.email }}")}),
		nd("Delete", "dataTable", 1.1, map[string]interface{}{"operation": "deleteRows", "dataTableId": table, "filters": cond("email", "eq", "b@x.io")}),
		nd("All", "dataTable", 1.1, map[string]interface{}{"operation": "get", "dataTableId": table, "returnAll": true, "orderBy": true, "orderByColumn": "score", "orderByDirection": "ASC"}),
		nd("One", "limit", 1, map[string]interface{}{"maxItems": 1}),
		nd("Probe", "set", 3.4, map[string]interface{}{"mode": "raw", "jsonOutput": `{"email":"z@x.io"}`}),
	}, "Start->Rows", "Rows->Split", "Split->Insert", "Insert->One", "One->High", "High->Upsert", "Upsert->Update", "Update->Probe", "Probe->New", "New->Delete", "Delete->All")
	_, rep, _ := Import(data, ImportOptions{})
	if !rep.Runnable {
		t.Fatalf("not runnable: %+v", rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	if got := itemsOf(t, steps["Insert"], 0); len(got) != 2 || got[0]["id"] != 1.0 || got[1]["score"] != 12.0 || got[0]["createdAt"] == nil {
		t.Fatalf("insert: %v", got)
	}
	if got := itemsOf(t, steps["High"], 0); len(got) != 1 || got[0]["email"] != "b@x.io" {
		t.Fatalf("get with filter: %v", got)
	}
	if got := itemsOf(t, steps["Upsert"], 0); len(got) != 1 || got[0]["email"] != "c@x.io" || got[0]["score"] != 7.0 {
		t.Fatalf("upsert insert: %v", got)
	}
	if got := itemsOf(t, steps["Update"], 0); len(got) != 1 || got[0]["score"] != 6.0 {
		t.Fatalf("update: %v", got)
	}
	if got := itemsOf(t, steps["New"], 0); len(got) != 1 || got[0]["email"] != "z@x.io" {
		t.Fatalf("rowNotExists: %v", got)
	}
	got := itemsOf(t, steps["All"], 0)
	if len(got) != 2 || got[0]["email"] != "a@x.io" || got[1]["email"] != "c@x.io" {
		t.Fatalf("all after delete, by score: %v", got)
	}
	tables, _ := store.ListDataTables(context.Background(), "ns")
	if len(tables) != 1 || tables[0].Name != "Leads" || tables[0].ID != "tblFromAuthor" || len(tables[0].Columns) != 2 {
		t.Fatalf("auto-created table: %+v", tables)
	}
}

func TestLikeMatch(t *testing.T) {
	for _, c := range []struct {
		s, p string
		want bool
	}{{"hello world", "lo w", true}, {"hello", "h%o", true}, {"hello", "h_llo", true}, {"hello", "x%", false}, {"hello", "%", true}} {
		if likeMatch(c.s, c.p) != c.want {
			t.Errorf("likeMatch(%q, %q) != %v", c.s, c.p, c.want)
		}
	}
}
