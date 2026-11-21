package n8n

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dgmos/flow/internal/engine"
)

// DataTable is one n8n Data Table: typed columns and rows. Rows hold the
// column values plus the system columns id, createdAt and updatedAt.
type DataTable struct {
	ID        string                   `json:"id"`
	Name      string                   `json:"name"`
	Columns   []DataTableColumn        `json:"columns"`
	Rows      []map[string]interface{} `json:"rows"`
	NextRowID int64                    `json:"nextRowId"`
	CreatedAt time.Time                `json:"createdAt"`
	UpdatedAt time.Time                `json:"updatedAt"`
}

// DataTableColumn is a column of a DataTable. Type is string, number,
// boolean or date.
type DataTableColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// DataTableStore persists n8n Data Tables per namespace. UpdateDataTable
// must apply fn atomically (e.g. with a row lock or compare-and-swap): fn
// receives the current table (nil when it does not exist) and returns the
// table to store, or nil to delete it.
type DataTableStore interface {
	ListDataTables(ctx context.Context, namespace string) ([]*DataTable, error)
	GetDataTable(ctx context.Context, namespace, id string) (*DataTable, error)
	UpdateDataTable(ctx context.Context, namespace, id string, fn func(t *DataTable) (*DataTable, error)) error
}

// MemoryDataTables is a process-local DataTableStore.
type MemoryDataTables struct {
	mu     sync.Mutex
	tables map[string]map[string]*DataTable
}

// NewMemoryDataTables returns an empty in-memory DataTableStore.
func NewMemoryDataTables() *MemoryDataTables {
	return &MemoryDataTables{tables: map[string]map[string]*DataTable{}}
}

// ListDataTables implements DataTableStore.
func (m *MemoryDataTables) ListDataTables(_ context.Context, ns string) ([]*DataTable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*DataTable, 0, len(m.tables[ns]))
	for _, t := range m.tables[ns] {
		out = append(out, t.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// GetDataTable implements DataTableStore.
func (m *MemoryDataTables) GetDataTable(_ context.Context, ns, id string) (*DataTable, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tables[ns][id].clone(), nil
}

// UpdateDataTable implements DataTableStore.
func (m *MemoryDataTables) UpdateDataTable(_ context.Context, ns, id string, fn func(*DataTable) (*DataTable, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next, err := fn(m.tables[ns][id].clone())
	if err != nil {
		return err
	}
	if m.tables[ns] == nil {
		m.tables[ns] = map[string]*DataTable{}
	}
	if next == nil {
		delete(m.tables[ns], id)
	} else {
		m.tables[ns][id] = next.clone()
	}
	return nil
}

func (t *DataTable) clone() *DataTable {
	if t == nil {
		return nil
	}
	c := *t
	c.Columns = append([]DataTableColumn(nil), t.Columns...)
	c.Rows = make([]map[string]interface{}, len(t.Rows))
	for i, r := range t.Rows {
		c.Rows[i] = cloneJSON(r)
	}
	return &c
}

var defaultDataTables = NewMemoryDataTables()

func (ec *execCtx) dataTables() DataTableStore {
	if ec.opts != nil && ec.opts.DataTables != nil {
		return ec.opts.DataTables
	}
	return defaultDataTables
}

var dataTableSystemColumns = map[string]string{"id": "number", "createdAt": "date", "updatedAt": "date"}

func (t *DataTable) info() map[string]interface{} {
	cols := make([]interface{}, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = map[string]interface{}{"name": c.Name, "type": c.Type, "index": float64(i)}
	}
	return map[string]interface{}{"id": t.ID, "name": t.Name, "columns": cols,
		"createdAt": t.CreatedAt.UTC().Format(time.RFC3339Nano), "updatedAt": t.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func (t *DataTable) columnType(name string) string {
	if typ, ok := dataTableSystemColumns[name]; ok {
		return typ
	}
	for _, c := range t.Columns {
		if c.Name == name {
			return c.Type
		}
	}
	return ""
}

// row returns a row as n8n outputs it: system columns plus every column.
func (t *DataTable) row(r map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{"id": r["id"], "createdAt": r["createdAt"], "updatedAt": r["updatedAt"]}
	for _, c := range t.Columns {
		out[c.Name] = r[c.Name]
	}
	return out
}

// set writes values into a row, adding unknown columns (typed from the
// value) as n8n's auto-created schema would.
func (t *DataTable) set(r, values map[string]interface{}) error {
	for k, v := range values {
		if _, ok := dataTableSystemColumns[k]; ok {
			continue
		}
		typ := t.columnType(k)
		if typ == "" {
			typ = inferColumnType(v)
			t.Columns = append(t.Columns, DataTableColumn{Name: k, Type: typ})
		}
		cv, err := coerceColumn(typ, v)
		if err != nil {
			return permanentError{fmt.Sprintf("Data table: column %q: %v", k, err)}
		}
		r[k] = cv
	}
	return nil
}

func inferColumnType(v interface{}) string {
	switch v.(type) {
	case float64, int, int64:
		return "number"
	case bool:
		return "boolean"
	}
	return "string"
}

func coerceColumn(typ string, v interface{}) (interface{}, error) {
	if v == nil {
		return nil, nil
	}
	switch typ {
	case "number":
		switch t := v.(type) {
		case float64:
			return t, nil
		case string:
			if strings.TrimSpace(t) == "" {
				return nil, nil
			}
			f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
			if err != nil {
				return nil, fmt.Errorf("%q is not a number", t)
			}
			return f, nil
		case bool:
			if t {
				return 1.0, nil
			}
			return 0.0, nil
		}
		return toFloatDefault(v, 0), nil
	case "boolean":
		switch t := v.(type) {
		case bool:
			return t, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(t)) {
			case "true", "1", "yes":
				return true, nil
			case "false", "0", "no", "":
				return false, nil
			}
			return nil, fmt.Errorf("%q is not a boolean", t)
		}
		return toBool(v), nil
	case "date":
		if tm, ok := timeOf(v); ok {
			return tm.UTC().Format(time.RFC3339Nano), nil
		}
		return nil, fmt.Errorf("%v is not a date", v)
	}
	switch t := v.(type) {
	case string:
		return t, nil
	case map[string]interface{}, []interface{}:
		return mustJSON(t), nil
	}
	return toString(v), nil
}

// ---------------------------------------------------------------- filters

type dtCondition struct {
	column, op string
	value      interface{}
}

type dtFilter struct {
	all   bool
	conds []dtCondition
}

func (ec *execCtx) dataTableFilter(i int) (dtFilter, error) {
	match, _ := ec.str(i, "matchType", "anyCondition")
	f := dtFilter{all: match == "allConditions"}
	v, err := ec.param(i, "filters.conditions", nil)
	if err != nil {
		return f, err
	}
	for _, raw := range asList(v) {
		m, _ := raw.(map[string]interface{})
		if m == nil {
			continue
		}
		op := toString(m["condition"])
		if op == "" {
			op = "eq"
		}
		col := toString(m["keyName"])
		if col == "" {
			col = "id"
		}
		f.conds = append(f.conds, dtCondition{column: col, op: op, value: m["keyValue"]})
	}
	return f, nil
}

func (f dtFilter) match(t *DataTable, r map[string]interface{}) bool {
	if len(f.conds) == 0 {
		return true
	}
	for _, c := range f.conds {
		ok := c.match(t.columnType(c.column), r[c.column])
		if ok && !f.all {
			return true
		}
		if !ok && f.all {
			return false
		}
	}
	return f.all
}

func (c dtCondition) match(typ string, v interface{}) bool {
	switch c.op {
	case "isEmpty":
		return v == nil || v == ""
	case "isNotEmpty":
		return v != nil && v != ""
	case "isTrue":
		return v == true
	case "isFalse":
		return v == false
	}
	want, err := coerceColumn(typ, c.value)
	if err != nil {
		want = c.value
	}
	switch c.op {
	case "eq":
		return dtCompare(typ, v, want) == 0 && (v == nil) == (want == nil)
	case "neq":
		return dtCompare(typ, v, want) != 0 || (v == nil) != (want == nil)
	case "like", "ilike":
		s, p := toString(v), toString(c.value)
		if v == nil {
			return false
		}
		if c.op == "ilike" {
			s, p = strings.ToLower(s), strings.ToLower(p)
		}
		return likeMatch(s, p)
	case "gt", "gte", "lt", "lte":
		if v == nil || want == nil {
			return false
		}
		d := dtCompare(typ, v, want)
		return map[string]bool{"gt": d > 0, "gte": d >= 0, "lt": d < 0, "lte": d <= 0}[c.op]
	}
	return false
}

func dtCompare(typ string, a, b interface{}) int {
	switch typ {
	case "number":
		x, y := toFloatDefault(a, 0), toFloatDefault(b, 0)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case "date":
		ta, _ := timeOf(a)
		tb, _ := timeOf(b)
		return ta.Compare(tb)
	}
	return strings.Compare(toString(a), toString(b))
}

// likeMatch matches SQL LIKE patterns (% and _); a pattern without
// wildcards matches as "contains", as n8n's Contains condition does.
func likeMatch(s, p string) bool {
	if !strings.ContainsAny(p, "%_") {
		return strings.Contains(s, p)
	}
	var match func(s, p []rune) bool
	match = func(s, p []rune) bool {
		for len(p) > 0 {
			switch p[0] {
			case '%':
				for k := 0; k <= len(s); k++ {
					if match(s[k:], p[1:]) {
						return true
					}
				}
				return false
			case '_':
				if len(s) == 0 {
					return false
				}
			default:
				if len(s) == 0 || s[0] != p[0] {
					return false
				}
			}
			s, p = s[1:], p[1:]
		}
		return len(s) == 0
	}
	return match([]rune(s), []rune(p))
}

// ---------------------------------------------------------------- node

// dataTableRef resolves the node's data table locator to a table ID,
// creating the table on first use when it does not exist yet: imported
// templates reference tables from the author's n8n instance, so a table is
// matched by ID, then by name, and otherwise created with that name.
func (ec *execCtx) dataTableRef(i int, create bool) (string, error) {
	v, err := ec.param(i, "dataTableId", "")
	if err != nil {
		return "", err
	}
	id := rlValue(v)
	name := id
	mode := ""
	if m, ok := v.(map[string]interface{}); ok {
		mode = toString(m["mode"])
		if n := toString(m["cachedResultName"]); n != "" {
			name = n
		}
	}
	if strings.TrimSpace(id) == "" && strings.TrimSpace(name) == "" {
		return "", permanentError{"Data table: no data table selected"}
	}
	ns := engine.NamespaceFromContext(ec.ctx)
	store := ec.dataTables()
	if mode != "name" && id != "" {
		t, err := store.GetDataTable(ec.ctx, ns, id)
		if err != nil {
			return "", err
		}
		if t != nil {
			return id, nil
		}
	}
	tables, err := store.ListDataTables(ec.ctx, ns)
	if err != nil {
		return "", err
	}
	for _, t := range tables {
		if strings.EqualFold(t.Name, name) {
			return t.ID, nil
		}
	}
	if !create {
		return "", permanentError{fmt.Sprintf("Data table %q not found", name)}
	}
	newID := id
	if mode == "name" || newID == "" {
		newID = randomID()[:16]
	}
	err = store.UpdateDataTable(ec.ctx, ns, newID, func(t *DataTable) (*DataTable, error) {
		if t != nil {
			return t, nil
		}
		now := time.Now().UTC()
		return &DataTable{ID: newID, Name: name, NextRowID: 1, CreatedAt: now, UpdatedAt: now}, nil
	})
	return newID, err
}

func dataTableNode(ec *execCtx) ([][]Item, error) {
	resource, _ := ec.str(0, "resource", "row")
	op, _ := ec.str(0, "operation", map[string]string{"row": "insert", "table": "list"}[resource])
	if resource == "row" {
		if alias, ok := map[string]string{"getAll": "get", "append": "insert", "delete": "deleteRows"}[op]; ok {
			op = alias
		}
	}
	ns := engine.NamespaceFromContext(ec.ctx)
	store := ec.dataTables()
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		return ec.dataTableOp(store, ns, resource, op, i, it)
	})
}

func (ec *execCtx) dataTableValues(i int, it Item) (map[string]interface{}, error) {
	mode, _ := ec.str(i, "columns.mappingMode", "defineBelow")
	var data map[string]interface{}
	if mode == "autoMapInputData" {
		data = cloneJSON(it.JSON)
		for k := range dataTableSystemColumns {
			delete(data, k)
		}
	} else {
		v, err := ec.param(i, "columns.value", map[string]interface{}{})
		if err != nil {
			return nil, err
		}
		data, _ = v.(map[string]interface{})
	}
	for k, v := range data {
		if _, ok := v.([]interface{}); ok {
			return nil, permanentError{fmt.Sprintf("Data table: unexpected array input in column %q", k)}
		}
	}
	return data, nil
}

func (ec *execCtx) dataTableOp(store DataTableStore, ns, resource, op string, i int, it Item) ([]Item, error) {
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if resource == "table" {
		switch op {
		case "list":
			tables, err := store.ListDataTables(ec.ctx, ns)
			if err != nil {
				return nil, err
			}
			filter, _ := ec.str(i, "options.filterName", "")
			returnAll, _ := ec.boolean(i, "returnAll", false)
			limit, _ := ec.num(i, "limit", 50)
			var out []Item
			for _, t := range tables {
				if filter != "" && !strings.Contains(strings.ToLower(t.Name), strings.ToLower(filter)) {
					continue
				}
				if !returnAll && len(out) >= int(limit) {
					break
				}
				out = append(out, Item{JSON: t.info()})
			}
			return out, nil
		case "create":
			name, _ := ec.str(i, "tableName", "")
			if strings.TrimSpace(name) == "" {
				return nil, permanentError{"Data table: table name is required"}
			}
			ifNotExists, _ := ec.boolean(i, "options.createIfNotExists", true)
			tables, err := store.ListDataTables(ec.ctx, ns)
			if err != nil {
				return nil, err
			}
			for _, t := range tables {
				if t.Name == name {
					if ifNotExists {
						return one(t.info()), nil
					}
					return nil, permanentError{fmt.Sprintf("Data table %q already exists", name)}
				}
			}
			cv, _ := ec.param(i, "columns.column", nil)
			t := &DataTable{ID: randomID()[:16], Name: name, NextRowID: 1, CreatedAt: now, UpdatedAt: now}
			for _, raw := range asList(cv) {
				m, _ := raw.(map[string]interface{})
				if n := toString(m["name"]); n != "" {
					typ := toString(m["type"])
					if typ == "" {
						typ = "string"
					}
					t.Columns = append(t.Columns, DataTableColumn{Name: n, Type: typ})
				}
			}
			if err := store.UpdateDataTable(ec.ctx, ns, t.ID, func(*DataTable) (*DataTable, error) { return t, nil }); err != nil {
				return nil, err
			}
			return one(t.info()), nil
		}
		id, err := ec.dataTableRef(i, false)
		if err != nil {
			return nil, err
		}
		var res map[string]interface{}
		err = store.UpdateDataTable(ec.ctx, ns, id, func(t *DataTable) (*DataTable, error) {
			if t == nil {
				return nil, permanentError{"Data table not found"}
			}
			switch op {
			case "clear":
				res = map[string]interface{}{"success": true, "deletedCount": float64(len(t.Rows))}
				t.Rows, t.UpdatedAt = nil, now
				return t, nil
			case "update":
				name, _ := ec.str(i, "newName", "")
				t.Name, t.UpdatedAt = name, now
				res = map[string]interface{}{"success": true, "name": name}
				return t, nil
			case "delete":
				res = map[string]interface{}{"success": true, "deletedTableId": id}
				return nil, nil
			}
			return nil, unsupportedOp("Data table", resource, op)
		})
		if err != nil {
			return nil, err
		}
		return one(res), nil
	}

	id, err := ec.dataTableRef(i, true)
	if err != nil {
		return nil, err
	}
	switch op {
	case "get", "rowExists", "rowNotExists":
		t, err := store.GetDataTable(ec.ctx, ns, id)
		if err != nil || t == nil {
			return nil, err
		}
		f, err := ec.dataTableFilter(i)
		if err != nil {
			return nil, err
		}
		limit := 0
		if op == "get" {
			if all, _ := ec.boolean(i, "returnAll", false); !all {
				l, _ := ec.num(i, "limit", 50)
				limit = int(l)
			}
		} else {
			limit = 1
		}
		rows := t.Rows
		if op == "get" {
			rows = append([]map[string]interface{}(nil), rows...)
			col, dir := "", "DESC"
			if ob, _ := ec.boolean(i, "orderBy", false); ob {
				col, _ = ec.str(i, "orderByColumn", "createdAt")
				dir, _ = ec.str(i, "orderByDirection", "DESC")
			}
			if col != "" {
				typ := t.columnType(col)
				sort.SliceStable(rows, func(a, b int) bool {
					d := dtCompare(typ, rows[a][col], rows[b][col])
					if dir == "ASC" {
						return d < 0
					}
					return d > 0
				})
			}
		}
		var hits []Item
		for _, r := range rows {
			if f.match(t, r) {
				hits = append(hits, Item{JSON: t.row(r)})
				if limit > 0 && len(hits) >= limit {
					break
				}
			}
		}
		switch op {
		case "rowExists":
			if len(hits) > 0 {
				return []Item{it}, nil
			}
			return nil, nil
		case "rowNotExists":
			if len(hits) == 0 {
				return []Item{it}, nil
			}
			return nil, nil
		}
		return hits, nil
	case "insert":
		data, err := ec.dataTableValues(i, it)
		if err != nil {
			return nil, err
		}
		bulk, _ := ec.boolean(i, "options.optimizeBulk", false)
		var out map[string]interface{}
		err = store.UpdateDataTable(ec.ctx, ns, id, func(t *DataTable) (*DataTable, error) {
			if t == nil {
				return nil, permanentError{"Data table not found"}
			}
			r := map[string]interface{}{"id": float64(t.NextRowID), "createdAt": stamp, "updatedAt": stamp}
			if err := t.set(r, data); err != nil {
				return nil, err
			}
			t.NextRowID++
			t.Rows = append(t.Rows, r)
			t.UpdatedAt = now
			out = t.row(r)
			return t, nil
		})
		if err != nil {
			return nil, err
		}
		if bulk {
			return one(map[string]interface{}{"success": true}), nil
		}
		return one(out), nil
	case "update", "upsert", "deleteRows":
		f, err := ec.dataTableFilter(i)
		if err != nil {
			return nil, err
		}
		if len(f.conds) == 0 {
			return nil, permanentError{"Data table: at least one condition is required"}
		}
		var data map[string]interface{}
		if op != "deleteRows" {
			if data, err = ec.dataTableValues(i, it); err != nil {
				return nil, err
			}
		}
		dryRun, _ := ec.boolean(i, "options.dryRun", false)
		var out []Item
		err = store.UpdateDataTable(ec.ctx, ns, id, func(t *DataTable) (*DataTable, error) {
			if t == nil {
				return nil, permanentError{"Data table not found"}
			}
			kept := t.Rows[:0:0]
			for _, r := range t.Rows {
				if !f.match(t, r) {
					kept = append(kept, r)
					continue
				}
				before := t.row(r)
				if op == "deleteRows" {
					if dryRun {
						before["dryRunState"] = "before"
						out = append(out, Item{JSON: before})
						kept = append(kept, r)
					} else {
						out = append(out, Item{JSON: before})
					}
					continue
				}
				nr := cloneJSON(r)
				if err := t.set(nr, data); err != nil {
					return nil, err
				}
				nr["updatedAt"] = stamp
				if dryRun {
					before["dryRunState"] = "before"
					after := t.row(nr)
					after["dryRunState"] = "after"
					out = append(out, Item{JSON: before}, Item{JSON: after})
					kept = append(kept, r)
				} else {
					out = append(out, Item{JSON: t.row(nr)})
					kept = append(kept, nr)
				}
			}
			t.Rows = kept
			if op == "upsert" && len(out) == 0 {
				r := map[string]interface{}{"id": float64(t.NextRowID), "createdAt": stamp, "updatedAt": stamp}
				for _, c := range f.conds {
					if c.op == "eq" {
						if _, sys := dataTableSystemColumns[c.column]; !sys {
							if _, set := data[c.column]; !set {
								if err := t.set(r, map[string]interface{}{c.column: c.value}); err != nil {
									return nil, err
								}
							}
						}
					}
				}
				if err := t.set(r, data); err != nil {
					return nil, err
				}
				if dryRun {
					after := t.row(r)
					after["dryRunState"] = "after"
					out = append(out, Item{JSON: after})
					return t, nil
				}
				t.NextRowID++
				t.Rows = append(t.Rows, r)
				out = append(out, Item{JSON: t.row(r)})
			}
			if !dryRun {
				t.UpdatedAt = now
			}
			return t, nil
		})
		return out, err
	}
	return nil, unsupportedOp("Data table", resource, op)
}
