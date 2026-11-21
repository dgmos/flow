package n8n

import (
	"strings"
)

// Bright Data: the official declarative package (@brightdata/n8n-nodes-brightdata)
// and the older programmatic one (n8n-nodes-brightdata). Same API and
// parameters; the older one wraps snapshot downloads and dataset lists in
// {items: …}, flattens scrape results, ignores Markdown output and maps the
// "==" filter operator to "=".

func init() {
	registerREST("@brightdata/n8n-nodes-brightdata.brightData", brightDataService(false))
	registerREST("n8n-nodes-brightdata.brightData", brightDataService(true))
}

func brightDataService(legacy bool) *restService {
	items := func(_ *execCtx, _ int, v interface{}) (interface{}, error) {
		return map[string]interface{}{"items": v}, nil
	}
	if !legacy {
		items = nil
	}
	urlsBody := func(ec *execCtx, i int, r *restRequest) error {
		raw, _ := ec.param(i, "urls", "")
		v, err := restConv("json", raw)
		if err != nil || v == nil {
			return permanentError{ec.node.Name + ": Invalid JSON format for URLs"}
		}
		if m, ok := v.(map[string]interface{}); ok {
			r.body = m
		} else {
			r.raw, r.contentType = []byte(mustJSON(v)), "application/json"
		}
		return nil
	}
	ops := map[string]*restOp{
		"webUnlocker/request": {method: "POST", path: "/request", body: "zone::rl country::rl method url", build: func(ec *execCtx, i int, r *restRequest) error {
			brightDataDefaults(r)
			df, _ := ec.str(i, "data_format", "")
			format, _ := ec.str(i, "format", "raw")
			if !legacy && df == "markdown" {
				format = "raw"
			}
			r.body["format"] = format
			if !legacy && df != "" {
				r.body["data_format"] = df
			}
			if r.body["method"] == nil {
				r.body["method"] = "GET"
			}
			return nil
		}},
		"webUnlocker/WebSearch": {method: "POST", path: "/request", body: "zone::rl country::rl", build: func(ec *execCtx, i int, r *restRequest) error {
			brightDataDefaults(r)
			q, _ := ec.str(i, "query", "")
			page, _ := ec.num(i, "page", 1)
			if page < 1 {
				page = 1
			}
			r.body["url"] = "https://www.google.com/search?q=" + jsURIComponent(q) + "&start=" + toString(int64(page-1)*10) + "&brd_json=1"
			r.body["format"] = "raw"
			return nil
		}},
		"webScrapper/scrapeByUrl": {method: "POST", path: "/datasets/v3/scrape", query: "dataset_id::rl format include_errors:include_errors:bool", build: func(ec *execCtx, i int, r *restRequest) error {
			if r.query.Get("format") == "" {
				r.query.Set("format", "json")
			}
			if r.query.Get("include_errors") == "" {
				r.query.Set("include_errors", "false")
			}
			return urlsBody(ec, i, r)
		}},
		"webScrapper/triggerCollectionByUrl":  {method: "POST", path: "/datasets/v3/trigger", query: "dataset_id::rl endpoint notify", build: urlsBody},
		"webScrapper/monitorProgressSnapshot": {path: "/datasets/v3/progress/{snapshot_id}"},
		"webScrapper/downloadSnapshot": {path: "/datasets/v3/snapshot/{snapshot_id}", post: items, build: func(ec *execCtx, i int, r *restRequest) error {
			f, _ := ec.str(i, "format", "json")
			r.query.Set("format", f)
			split, _ := ec.boolean(i, "split_into_parts", false)
			if split || legacy {
				b, _ := ec.num(i, "batch_size", 1000)
				p, _ := ec.num(i, "part", 1)
				r.query.Set("batch_size", toString(int64(b)))
				r.query.Set("part", toString(int64(p)))
			}
			if legacy {
				c, _ := ec.boolean(i, "compress", false)
				r.query.Set("compress", toString(c))
			}
			return nil
		}},
		"webScrapper/getSnapshots": {path: "/datasets/v3/snapshots", query: "dataset_id::rl status skip limit from_date to_date", build: func(ec *execCtx, i int, r *restRequest) error {
			for k, def := range map[string]string{"status": "ready", "skip": "0", "limit": "50"} {
				if r.query.Get(k) == "" {
					r.query.Set(k, def)
				}
			}
			return nil
		}},
		"webScrapper/deliverSnapshot":            {method: "POST", path: "/datasets/v3/deliver/{snapshot_id}", query: "notify", build: brightDataDeliver},
		"marketplaceDataset/listDatasets":        {path: "/datasets/list", post: items},
		"marketplaceDataset/listSnapshots":       {path: "/datasets/snapshots"},
		"marketplaceDataset/getSnapshotMetadata": {path: "/datasets/snapshots/{snapshot_id}"},
		"marketplaceDataset/getSnapshotParts":    {path: "/datasets/snapshots/{snapshot_id}/parts"},
		"marketplaceDataset/getDatasetMetadata":  {path: "/datasets/{dataset_id}/metadata"},
		"marketplaceDataset/getSnapshotContent": {path: "/datasets/snapshots/{snapshot_id}/download", post: items, build: func(ec *execCtx, i int, r *restRequest) error {
			f, _ := ec.str(i, "format", "json")
			c, _ := ec.boolean(i, "compress", false)
			b, _ := ec.num(i, "batch_size", 1000)
			p, _ := ec.num(i, "part", 1)
			r.query.Set("format", f)
			r.query.Set("compress", toString(c))
			r.query.Set("batch_size", toString(int64(b)))
			r.query.Set("part", toString(int64(p)))
			return nil
		}},
		"marketplaceDataset/filterDataset": {method: "POST", path: "/datasets/filter", build: func(ec *execCtx, i int, r *restRequest) error {
			ds, _ := ec.param(i, "dataset_id", "")
			limit, _ := ec.num(i, "records_limit", 100)
			body := map[string]interface{}{"dataset_id": rlValue(ds), "records_limit": limit}
			switch ft, _ := ec.str(i, "filter_type", "filter_single"); ft {
			case "filter_single":
				name, _ := ec.str(i, "field_name", "")
				op, _ := ec.str(i, "field_operator", "==")
				val, _ := ec.param(i, "field_value", "")
				if legacy && op == "==" {
					op = "="
				}
				body["filter"] = map[string]interface{}{"name": name, "operator": op, "value": val}
			case "filters_group":
				raw, _ := ec.param(i, "filters_group", "")
				v, err := restConv("json", raw)
				if err != nil {
					return permanentError{ec.node.Name + ": Invalid JSON format for filters group"}
				}
				body["filter"] = v
			default:
				if legacy && ft == "csv_filter" {
					body["filter"], _ = ec.str(i, "csv_filter", "")
				} else if legacy && ft == "json_filter" {
					raw, _ := ec.param(i, "json_filter", "")
					v, err := restConv("json", raw)
					if err != nil {
						return permanentError{ec.node.Name + ": Invalid JSON format for JSON filter"}
					}
					body["filter"] = map[string]interface{}{"operator": asMap(v)["operator"], "filters": asMap(v)["filters"]}
				} else {
					body["filter"] = map[string]interface{}{}
				}
			}
			r.body = body
			return nil
		}},
	}
	if legacy {
		ops["marketplaceDataset/listSnapshots"].query = "dataset_id::rl view_id status"
		ops["marketplaceDataset/deliverSnapshot"] = &restOp{method: "POST", path: "/datasets/snapshots/{snapshot_id}/deliver", build: brightDataDeliver}
		ops["webScrapper/scrapeByUrl"].post = func(_ *execCtx, _ int, v interface{}) (interface{}, error) {
			if l, ok := v.([]interface{}); ok {
				return l, nil
			}
			if s, ok := asMap(v)["data"].(string); ok {
				return map[string]interface{}{"data": s}, nil
			}
			return []interface{}{}, nil
		}
		delete(ops, "webUnlocker/WebSearch")
	} else {
		ops["marketplaceDataset/deliverSnapshot"] = &restOp{method: "POST", path: "/datasets/snapshots/{snapshot_id}/deliver", build: brightDataDeliver}
	}
	return &restService{
		defResource: "webUnlocker",
		defOps:      map[string]string{"webUnlocker": "request", "webScrapper": "scrapeByUrl", "marketplaceDataset": "listDatasets"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("brightdataApi")
			if err != nil {
				return nil, err
			}
			return bearerClient(ec.apiBase("brightData", "https://api.brightdata.com"), c, "token"), nil
		},
		ops: ops,
	}
}

// brightDataDefaults applies the zone/country defaults of the node.
func brightDataDefaults(r *restRequest) {
	if r.body["zone"] == nil {
		r.body["zone"] = "n8n_unlocker"
	}
	if r.body["country"] == nil {
		r.body["country"] = "us"
	}
}

// jsURIComponent escapes like JavaScript's encodeURIComponent.
func jsURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex2(c)))
		}
	}
	return b.String()
}

func hex2(c byte) string {
	const h = "0123456789abcdef"
	return string([]byte{h[c>>4], h[c&15]})
}

// brightDataDeliver builds {deliver: {...}} for the chosen storage.
func brightDataDeliver(ec *execCtx, i int, r *restRequest) error {
	s := func(k string) string { v, _ := ec.str(i, k, ""); return v }
	typ := firstNonEmpty(s("deliver_type"), "s3")
	d := map[string]interface{}{"type": typ}
	creds := map[string]interface{}{}
	file := func() {
		d["filename"] = map[string]interface{}{"template": s("filename_template"), "extension": firstNonEmpty(s("filename_extension"), "json")}
	}
	opt := func(dst map[string]interface{}, keys ...string) {
		for _, k := range keys {
			if v := s(k); v != "" {
				dst[k] = v
			}
		}
	}
	switch typ {
	case "webhook":
		d["endpoint"] = s("endpoint")
		file()
	case "s3":
		d["bucket"] = s("bucket")
		file()
		creds["aws-access-key"], creds["aws-secret-key"] = s("aws-access-key"), s("aws-secret-key")
		opt(creds, "role_arn", "external_id")
		opt(d, "region", "directory")
	case "ali_oss":
		d["bucket"] = s("bucket")
		file()
		creds["access-key"], creds["secret-key"] = s("access-key"), s("secret-key")
		opt(d, "region", "directory")
	case "gcs_pubsub", "gcs":
		file()
		creds["client_email"], creds["private_key"] = s("client_email"), s("private_key")
		if typ == "gcs" {
			d["bucket"] = s("bucket")
			opt(d, "directory")
		} else {
			d["topic_id"] = s("topic_id")
			opt(d, "attributes")
		}
	case "azure":
		d["container"] = s("container")
		file()
		creds["account"], creds["key"], creds["sas_token"] = s("account"), s("key"), s("sas_token")
		opt(d, "directory")
	case "sftp":
		d["host"], d["path"] = s("host"), s("path")
		port, _ := ec.num(i, "port", 22)
		d["port"] = port
		file()
		creds["username"], creds["password"], creds["ssh_key"] = s("username"), s("password"), s("ssh_key")
		opt(creds, "passphrase")
		opt(d, "directory")
	case "snowflake":
		d["database"], d["schema"], d["stage"] = s("database"), s("schema"), s("stage")
		file()
		opt(d, "role", "warehouse", "directory")
	}
	if len(creds) > 0 {
		d["credentials"] = creds
	}
	r.body = map[string]interface{}{"deliver": d}
	return nil
}
