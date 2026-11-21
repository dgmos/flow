package n8n

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Google BigQuery (n8n-nodes-base.googleBigQuery), REST API v2. Version 2
// has database/executeQuery (a query job, polled until complete) and
// database/insert; version 1 has record/create and record/getAll.

func init() {
	googleScopes["bigquery"] = "https://www.googleapis.com/auth/bigquery https://www.googleapis.com/auth/cloud-platform"
	registerREST("googleBigQuery", &restService{
		defResource: "database",
		defOps:      map[string]string{"database": "executeQuery", "record": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			h, err := ec.googleAuth("bigquery", "googleBigQueryOAuth2Api")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("googleBigQuery", "https://bigquery.googleapis.com/bigquery"), headers: h}, nil
		},
		ops: map[string]*restOp{
			"database/executeQuery": {run: bqExecuteQuery},
			"database/insert":       {run: bqInsert},
			"record/create":         {run: bqInsert},
			"record/getAll":         {run: bqV1GetAll},
		},
	})
}

func bqCall(ec *execCtx, c *restClient, method, path string, q url.Values, body map[string]interface{}) (map[string]interface{}, error) {
	if q == nil {
		q = url.Values{}
	}
	r := &restRequest{method: method, path: path, query: q, body: body}
	if body == nil {
		r.body = map[string]interface{}{}
	}
	v, err := c.do(ec, r)
	return asMap(v), err
}

// bqAllRows pages getQueryResults/tabledata by pageToken and merges the rows.
func bqAllRows(ec *execCtx, c *restClient, path string, q url.Values) (map[string]interface{}, error) {
	if q.Get("maxResults") == "" {
		q.Set("maxResults", "1000")
	}
	var rows []interface{}
	var res map[string]interface{}
	for n := 0; n < 10000; n++ {
		var err error
		if res, err = bqCall(ec, c, "GET", path, q, nil); err != nil {
			return nil, err
		}
		rows = append(rows, asList(res["rows"])...)
		tok := toString(res["pageToken"])
		if tok == "" || tok == "<nil>" {
			break
		}
		q.Set("pageToken", tok)
	}
	if res == nil {
		res = map[string]interface{}{}
	}
	if rows == nil {
		rows = []interface{}{}
	}
	res["rows"] = rows
	return res, nil
}

func bqExecuteQuery(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	sql, err := ec.str(i, "sqlQuery", "")
	if err != nil {
		return nil, err
	}
	pv, _ := ec.param(i, "projectId", "")
	project := rlValue(pv)
	opts := fieldsOf(ec, i, "options")
	raw, includeSchema := toBool(opts["rawOutput"]), toBool(opts["includeSchema"])
	maxResults, timeoutMs := 1000, 10000
	if n, ok := toFloat(opts["maxResults"]); ok && n > 0 {
		maxResults = int(n)
	}
	if n, ok := toFloat(opts["timeoutMs"]); ok && n > 0 {
		timeoutMs = int(n)
	}
	body := map[string]interface{}{}
	for k, v := range opts {
		switch k {
		case "rawOutput", "includeSchema", "maxResults", "timeoutMs":
		default:
			body[k] = v
		}
	}
	body["query"] = sql
	if ds := toString(body["defaultDataset"]); ds != "" {
		body["defaultDataset"] = map[string]interface{}{"datasetId": ds, "projectId": project}
	} else {
		delete(body, "defaultDataset")
	}
	if _, ok := body["useLegacySql"]; !ok {
		body["useLegacySql"] = false
	}
	if qp, ok := body["queryParameters"].(map[string]interface{}); ok {
		var params []interface{}
		for _, x := range asList(qp["namedParameters"]) {
			m := asMap(x)
			params = append(params, map[string]interface{}{"name": m["name"], "parameterType": map[string]interface{}{"type": "STRING"},
				"parameterValue": map[string]interface{}{"value": m["value"]}})
		}
		body["parameterMode"] = "NAMED"
		body["queryParameters"] = params
	}
	path := "/v2/projects/" + url.PathEscape(project)
	res, err := bqCall(ec, c, "POST", path+"/jobs", nil, map[string]interface{}{"configuration": map[string]interface{}{"query": body}})
	if err != nil {
		return nil, err
	}
	ref := asMap(res["jobReference"])
	jobID := toString(ref["jobId"])
	if jobID == "" || jobID == "<nil>" {
		return nil, permanentError{fmt.Sprintf("%s: No job ID returned, item %d", ec.node.Name, i)}
	}
	raw = raw || toBool(opts["dryRun"])
	loc := firstNonEmpty(toString(opts["location"]), toString(ref["location"]))
	if loc == "<nil>" {
		loc = ""
	}
	q := url.Values{"maxResults": {strconv.Itoa(maxResults)}, "timeoutMs": {strconv.Itoa(timeoutMs)}}
	if loc != "" {
		q.Set("location", loc)
	}
	done := toString(asMap(res["status"])["state"]) == "DONE"
	wait := time.Second
	for {
		qr, err := bqAllRows(ec, c, path+"/queries/"+url.PathEscape(jobID), q)
		if err != nil {
			return nil, err
		}
		if errs := asList(qr["errors"]); len(errs) > 0 && !done {
			var msgs []string
			for _, e := range errs {
				msgs = append(msgs, toString(asMap(e)["message"]))
			}
			return nil, permanentError{fmt.Sprintf("%s: Error(s) ocurring while executing query from item %d: %s", ec.node.Name, i, strings.Join(msgs, ", "))}
		}
		if done || toBool(qr["jobComplete"]) {
			if toBool(body["returnAsNumbers"]) && done {
				bqNumbers(qr)
			}
			return bqOutput(ec, qr, raw, includeSchema), nil
		}
		select {
		case <-ec.ctx.Done():
			return nil, ec.ctx.Err()
		case <-time.After(wait):
		}
		q.Del("pageToken")
		if wait < 30*time.Second {
			wait *= 2
		}
	}
}

// bqNumbers converts numeric columns to numbers (returnAsNumbers).
func bqNumbers(qr map[string]interface{}) {
	fields := asList(asMap(qr["schema"])["fields"])
	for _, row := range asList(qr["rows"]) {
		for k, cell := range asList(asMap(row)["f"]) {
			m := asMap(cell)
			if k >= len(fields) || m == nil {
				continue
			}
			switch toString(asMap(fields[k])["type"]) {
			case "INTEGER", "NUMERIC", "FLOAT", "BIGNUMERIC":
				if f, ok := toFloat(m["v"]); ok {
					m["v"] = f
				}
			}
		}
	}
}

// bqOutput turns query results into one item per row (or the raw response).
func bqOutput(ec *execCtx, qr map[string]interface{}, raw, includeSchema bool) []Item {
	if raw {
		return []Item{{JSON: qr}}
	}
	schema, hasSchema := qr["schema"].(map[string]interface{})
	rows, hasRows := qr["rows"].([]interface{})
	if hasRows && hasSchema {
		fields := asList(schema["fields"])
		out := make([]Item, 0, len(rows))
		for _, r := range bqSimplify(rows, fields, ec.version >= 2.1) {
			rec := asMap(r)
			if includeSchema {
				rec["_schema"] = fields
			}
			out = append(out, Item{JSON: rec})
		}
		return out
	}
	if hasSchema && includeSchema {
		return []Item{{JSON: map[string]interface{}{"success": true, "_schema": schema}}}
	}
	return []Item{{JSON: map[string]interface{}{"success": true}}}
}

func bqSimplify(rows, fields []interface{}, parseTS bool) []interface{} {
	out := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		rec := map[string]interface{}{}
		for k, cell := range asList(asMap(row)["f"]) {
			if k >= len(fields) {
				break
			}
			f := asMap(fields[k])
			v := asMap(cell)["v"]
			if toString(f["mode"]) == "REPEATED" {
				var vals []interface{}
				for _, x := range asList(v) {
					vals = append(vals, bqValue(f, asMap(x)["v"], parseTS))
				}
				if vals == nil {
					vals = []interface{}{}
				}
				rec[toString(f["name"])] = vals
			} else {
				rec[toString(f["name"])] = bqValue(f, v, parseTS)
			}
		}
		out = append(out, rec)
	}
	return out
}

func bqValue(f map[string]interface{}, v interface{}, parseTS bool) interface{} {
	switch toString(f["type"]) {
	case "RECORD":
		return bqSimplify([]interface{}{v}, asList(f["fields"]), parseTS)
	case "JSON":
		if x, err := restConv("json", v); err == nil && x != nil {
			return x
		}
	case "TIMESTAMP":
		if s, ok := toFloat(v); ok && parseTS && v != nil {
			sec := int64(s)
			t := time.Unix(sec, int64((s-float64(sec))*1e9)).Round(time.Millisecond).Local()
			return t.Format("2006-01-02T15:04:05.000Z07:00")
		}
	}
	return v
}

// bqInsert inserts all input items with tabledata.insertAll (n8n runs this
// operation once for all items, in batches).
func bqInsert(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	if i > 0 {
		return nil, nil
	}
	path, err := ec.restPath(0, "/v2/projects/{projectId}/datasets/{datasetId}/tables/{tableId}")
	if err != nil {
		return nil, err
	}
	items := ec.items()
	opts := fieldsOf(ec, 0, "options")
	body := map[string]interface{}{}
	batch := 100
	var rows []interface{}
	if ec.version < 2 {
		// v1: options per item, columns is a comma-separated list of keys.
		for k := range items {
			for kk, v := range fieldsOf(ec, k, "options") {
				body[kk] = v
			}
			cols, _ := ec.str(k, "columns", "")
			rec := map[string]interface{}{}
			for _, col := range strings.Split(cols, ",") {
				col = strings.TrimSpace(col)
				if v, ok := items[k].JSON[col]; ok {
					rec[col] = v
				}
			}
			rows = append(rows, map[string]interface{}{"json": rec})
		}
		batch = len(rows)
	} else {
		for k, v := range opts {
			if k == "batchSize" {
				if n, ok := toFloat(v); ok && n > 0 {
					batch = int(n)
				}
				continue
			}
			body[k] = v
		}
		tbl, err := bqCall(ec, c, "GET", path, nil, nil)
		if err != nil {
			return nil, err
		}
		schema, ok := tbl["schema"].(map[string]interface{})
		if !ok {
			return nil, permanentError{ec.node.Name + ": The destination table has no defined schema"}
		}
		fields := asList(schema["fields"])
		mode, _ := ec.str(0, "dataMode", "autoMap")
		for k := range items {
			rec := map[string]interface{}{}
			switch mode {
			case "autoMap":
				for _, f := range fields {
					name := toString(asMap(f)["name"])
					if v, ok := items[k].JSON[name]; ok {
						rec[name] = v
					}
				}
			case "define":
				v, err := ec.param(k, "fieldsUi.values", nil)
				if err != nil {
					return nil, err
				}
				for _, x := range asList(v) {
					m := asMap(x)
					rec[toString(m["fieldId"])] = m["fieldValue"]
				}
			}
			checked, err := bqCheckSchema(ec, fields, rec)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", k, err)
			}
			rows = append(rows, map[string]interface{}{"json": checked})
		}
	}
	if toString(body["traceId"]) == "" {
		body["traceId"] = bqUUID()
	}
	if batch < 1 {
		batch = 1
	}
	var out []Item
	for start := 0; start < len(rows); start += batch {
		end := start + batch
		if end > len(rows) {
			end = len(rows)
		}
		body["rows"] = rows[start:end]
		res, err := bqCall(ec, c, "POST", path+"/insertAll", nil, body)
		if err != nil {
			return nil, err
		}
		if errs := asList(res["insertErrors"]); len(errs) > 0 && !toBool(body["skipInvalidRows"]) && ec.version >= 2 {
			var msgs, failed, stopped []string
			for _, e := range errs {
				m := asMap(e)
				idx, _ := toFloat(m["index"])
				var bad []string
				for _, x := range asList(m["errors"]) {
					if toString(asMap(x)["reason"]) != "stopped" {
						bad = append(bad, toString(asMap(x)["message"]))
					}
				}
				n := strconv.Itoa(int(idx) + start)
				if len(bad) > 0 {
					msgs = append(msgs, "Row "+n+" failed with error: "+strings.Join(bad, ", "))
					failed = append(failed, n)
				} else {
					stopped = append(stopped, n)
				}
			}
			msg := "Problem inserting item(s) [" + strings.Join(failed, ", ") + "]"
			if len(stopped) > 0 {
				msg += ", nothing was inserted item(s) [" + strings.Join(stopped, ", ") + "]"
			}
			return nil, permanentError{ec.node.Name + ": " + msg + ": " + strings.Join(msgs, "\n, ")}
		}
		out = append(out, restItems(res)...)
	}
	return out, nil
}

// bqCheckSchema validates and coerces a record against the table schema.
func bqCheckSchema(ec *execCtx, fields []interface{}, rec map[string]interface{}) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for k, v := range rec {
		out[k] = v
	}
	for _, x := range fields {
		f := asMap(x)
		name, typ := toString(f["name"]), toString(f["type"])
		v, present := out[name]
		if toString(f["mode"]) == "REQUIRED" && (!present || v == nil) {
			return nil, permanentError{fmt.Sprintf("%s: The property '%s' is required, please define it in the 'Fields to Send'", ec.node.Name, name)}
		}
		if typ != "STRING" && v == "" {
			out[name] = nil
			v = nil
		}
		switch typ {
		case "JSON":
			if _, isStr := v.(string); !isStr && v != nil {
				out[name] = mustJSON(v)
			}
		case "RECORD":
			switch v.(type) {
			case map[string]interface{}, []interface{}, nil:
			default:
				p, err := restConv("json", v)
				if err != nil {
					return nil, permanentError{fmt.Sprintf("%s: The property '%s' is a RECORD type, but the value is nor an object nor a valid JSON string", ec.node.Name, name)}
				}
				out[name] = p
			}
		}
	}
	return out, nil
}

func bqUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// bqV1GetAll lists table rows (version 1), simplified to name → value.
func bqV1GetAll(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	path, err := ec.restPath(i, "/v2/projects/{projectId}/datasets/{datasetId}/tables/{tableId}")
	if err != nil {
		return nil, err
	}
	simple, _ := ec.boolean(0, "simple", true)
	var fields []string
	if simple {
		tbl, err := bqCall(ec, c, "GET", path, nil, nil)
		if err != nil {
			return nil, err
		}
		for _, f := range asList(asMap(tbl["schema"])["fields"]) {
			fields = append(fields, toString(asMap(f)["name"]))
		}
	}
	q := url.Values{}
	for k, v := range fieldsOf(ec, i, "options") {
		if !restEmpty(v) {
			q.Set(k, toString(v))
		}
	}
	if s := q.Get("selectedFields"); s != "" {
		fields = strings.Split(s, ",")
	}
	var rows []interface{}
	if all, _ := ec.boolean(0, "returnAll", false); all {
		res, err := bqAllRows(ec, c, path+"/data", q)
		if err != nil {
			return nil, err
		}
		rows = asList(res["rows"])
	} else {
		n, _ := ec.num(i, "limit", 50)
		q.Set("maxResults", strconv.Itoa(int(n)))
		res, err := bqCall(ec, c, "GET", path+"/data", q, nil)
		if err != nil {
			return nil, err
		}
		rows = asList(res["rows"])
	}
	if !simple {
		return restItems(rows), nil
	}
	out := make([]interface{}, 0, len(rows))
	for _, r := range rows {
		cells := asList(asMap(r)["f"])
		rec := map[string]interface{}{}
		for k, name := range fields {
			if k < len(cells) {
				rec[name] = asMap(cells[k])["v"]
			}
		}
		out = append(out, rec)
	}
	return restItems(out), nil
}
