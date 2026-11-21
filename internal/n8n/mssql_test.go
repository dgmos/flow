package n8n

import (
	"database/sql"
	"strings"
	"testing"
)

func TestMSSQLBind(t *testing.T) {
	q, args, err := mssqlBind("SELECT * FROM t WHERE a = $1 AND b = $10 AND c = '$5'", []interface{}{"x", 2, 3, 4, 5, 6, 7, 8, 9, "ten"})
	if err != nil || q != "SELECT * FROM t WHERE a = @p1 AND b = @p10 AND c = '@p5'" || len(args) != 10 || args[9].(sql.NamedArg).Name != "p10" {
		t.Fatalf("q=%q args=%v err=%v", q, args, err)
	}
	if _, _, err := mssqlBind("SELECT $2", []interface{}{"a"}); err == nil || !strings.Contains(err.Error(), "references $2") {
		t.Fatalf("unbound parameter: %v", err)
	}
	if _, _, err := mssqlBind("SELECT 'costs $3'", nil); err != nil {
		t.Fatalf("quoted $3 is text: %v", err)
	}
}

func TestMSSQLImportChecks(t *testing.T) {
	for _, c := range []struct {
		op       string
		runnable bool
	}{{"executeQuery", true}, {"insert", false}, {"", false}} {
		p := map[string]interface{}{"query": "SELECT 1"}
		if c.op != "" {
			p["operation"] = c.op
		}
		data := wfJSON(t, []map[string]interface{}{nd("Start", "manualTrigger", 1, nil), nd("SQL", "microsoftSql", 1.1, p)}, "Start->SQL")
		if _, rep, _ := Import(data, ImportOptions{}); rep.Runnable != c.runnable {
			t.Errorf("%q: runnable=%v (%+v)", c.op, rep.Runnable, rep.Blocking)
		}
	}
}
