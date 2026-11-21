package n8n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Table-driven REST connectors. Most n8n service nodes map a
// resource/operation pair to one HTTP call: a method and path, some query
// parameters, a JSON body built from node parameters and a list key in the
// response. A restService describes those calls as data; the engine resolves
// parameters, authenticates, paginates and turns responses into items.
//
// Field lists use a small notation, space separated:
//
//	name               parameter "name" → field "name"
//	name:full_name     parameter "name" → field "full_name"
//	dueDate:due_date:ms  with a conversion (see restConv)
//	a.b:x.y            dotted paths on both sides
//	@additionalFields  following names are read inside that collection
//	@                  back to top-level parameters
//	*                  copy the collection's remaining fields unchanged
//	*snake             … with snake_case names
//	=k:v               a constant field (v is JSON when it parses)
//
// Empty values (unset, "", empty list) are skipped.

type restService struct {
	defResource string
	defOps      map[string]string
	// client authenticates the calls of item i.
	client func(ec *execCtx, i int) (*restClient, error)
	ops    map[string]*restOp // "resource/operation"
	// pager and listKey are the defaults of list operations.
	pager   restPager
	listKey string
	// aliases maps alternative operation keys (older node versions) to ops keys.
	aliases map[string]string
}

type restClient struct {
	base    string
	headers map[string]string
	query   url.Values
}

type restOp struct {
	method string
	// path is appended to the client's base URL; {param} is replaced by the
	// path-escaped value of a parameter (resource locators are unwrapped),
	// {param?} may be empty, {+param} is inserted unescaped.
	path  string
	query string // field list for the query string
	body  string // field list for the JSON body
	// form sends the body as application/x-www-form-urlencoded.
	form bool
	// wrap nests the body: {"product": {...}}.
	wrap string
	// out is the dotted path of the result in the response.
	out string
	// list returns many records, honoring returnAll/limit and paginating.
	list    bool
	pager   restPager
	listKey string // overrides out for list pages
	// sizeParam receives the page size (min(limit, maxSize)).
	sizeParam string
	maxSize   int
	// success outputs {"success": true} instead of the response.
	success bool
	// build may adjust the request after the field lists are applied.
	build func(ec *execCtx, i int, r *restRequest) error
	// post transforms the decoded result (an object or a list).
	post func(ec *execCtx, i int, v interface{}) (interface{}, error)
	// run replaces the whole operation.
	run func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error)
}

// restRequest is a request being built.
type restRequest struct {
	method string
	path   string
	query  url.Values
	body   map[string]interface{}
	// raw, when set, is sent instead of body.
	raw         []byte
	contentType string
	headers     map[string]string
}

// restPage is the state of a paginated list call.
type restPage struct {
	url   string // set to fetch an absolute URL next
	query url.Values
	n     int // pages fetched so far
	got   int // records so far
	last  []interface{}
}

// restPager decides whether to fetch another page and prepares it.
type restPager func(p *restPage, res interface{}, hdr http.Header) bool

var restServices = map[string]*restService{}

func registerREST(nodeType string, s *restService) {
	if !strings.Contains(nodeType, ".") {
		nodeType = "n8n-nodes-base." + nodeType
	}
	restServices[nodeType] = s
}

// run is the node function of a REST service.
func (s *restService) run(ec *execCtx) ([][]Item, error) {
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOpBy(i, s.defResource, s.defOps)
		op := s.op(r, o)
		if op == nil {
			return nil, unsupportedOp(ec.node.Name, r, o)
		}
		c, err := s.client(ec, i)
		if err != nil {
			return nil, err
		}
		if op.run != nil {
			return op.run(ec, i, it, c)
		}
		return s.exec(ec, i, op, c)
	})
}

func (s *restService) op(r, o string) *restOp {
	k := r + "/" + o
	if a, ok := s.aliases[k]; ok {
		k = a
	}
	return s.ops[k]
}

func (s *restService) supports(r, o string) bool { return s.op(r, o) != nil }

func (s *restService) exec(ec *execCtx, i int, op *restOp, c *restClient) ([]Item, error) {
	req, err := op.request(ec, i)
	if err != nil {
		return nil, err
	}
	if !op.list {
		v, err := c.do(ec, req)
		if err != nil {
			return nil, err
		}
		if op.success {
			return []Item{{JSON: map[string]interface{}{"success": true}}}, nil
		}
		if op.out != "" {
			v = jpath(v, op.out)
		}
		if op.post != nil {
			if v, err = op.post(ec, i, v); err != nil {
				return nil, err
			}
		}
		return restItems(v), nil
	}
	all, _ := ec.boolean(i, "returnAll", false)
	limit := -1
	if !all {
		n, _ := ec.num(i, "limit", 50)
		limit = int(n)
	}
	pager, key := op.pager, firstNonEmpty(op.listKey, op.out)
	if pager == nil {
		pager = s.pager
	}
	if key == "" {
		key = s.listKey
	}
	if op.sizeParam != "" {
		size := op.maxSize
		if limit > 0 && (size == 0 || limit < size) {
			size = limit
		}
		if size > 0 {
			req.query.Set(op.sizeParam, strconv.Itoa(size))
		}
	}
	page := &restPage{query: req.query}
	var records []interface{}
	for ; page.n < 1000; page.n++ {
		v, hdr, err := c.doH(ec, req, page.url)
		if err != nil {
			return nil, err
		}
		var batch []interface{}
		if key != "" {
			batch = asList(jpath(v, key))
		} else if l, ok := v.([]interface{}); ok {
			batch = l
		} else {
			batch = firstList(v)
		}
		records = append(records, batch...)
		page.got, page.last = len(records), batch
		if limit >= 0 && len(records) >= limit {
			records = records[:limit]
			break
		}
		if pager == nil || len(batch) == 0 || !pager(page, v, hdr) {
			break
		}
		req.query = page.query
	}
	var out interface{} = records
	if op.post != nil {
		if out, err = op.post(ec, i, out); err != nil {
			return nil, err
		}
	}
	return restItems(out), nil
}

// restItems turns a result into items: one per list element, otherwise one.
func restItems(v interface{}) []Item {
	switch t := v.(type) {
	case []interface{}:
		out := make([]Item, 0, len(t))
		for _, x := range t {
			if m, ok := x.(map[string]interface{}); ok {
				out = append(out, Item{JSON: m})
			} else {
				out = append(out, Item{JSON: map[string]interface{}{"value": x}})
			}
		}
		return out
	case map[string]interface{}:
		return []Item{{JSON: t}}
	case nil:
		return []Item{{JSON: map[string]interface{}{"success": true}}}
	}
	return []Item{{JSON: map[string]interface{}{"value": v}}}
}

// firstList returns the only list field of an object response.
func firstList(v interface{}) []interface{} {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	var found []interface{}
	n := 0
	for _, x := range m {
		if l, ok := x.([]interface{}); ok {
			found = l
			n++
		}
	}
	if n == 1 {
		return found
	}
	return nil
}

func (op *restOp) request(ec *execCtx, i int) (*restRequest, error) {
	req := &restRequest{method: op.method, query: url.Values{}, body: map[string]interface{}{}, headers: map[string]string{}}
	if req.method == "" {
		req.method = http.MethodGet
	}
	path, err := ec.restPath(i, op.path)
	if err != nil {
		return nil, err
	}
	req.path = path
	q := map[string]interface{}{}
	if err := ec.restFields(i, op.query, q); err != nil {
		return nil, err
	}
	for k, v := range q {
		for _, s := range queryValues(v) {
			req.query.Add(k, s)
		}
	}
	if err := ec.restFields(i, op.body, req.body); err != nil {
		return nil, err
	}
	if op.build != nil {
		if err := op.build(ec, i, req); err != nil {
			return nil, err
		}
	}
	if op.form && req.raw == nil {
		f := url.Values{}
		for k, v := range req.body {
			for _, s := range queryValues(v) {
				f.Add(k, s)
			}
		}
		req.raw, req.contentType = []byte(f.Encode()), "application/x-www-form-urlencoded"
	}
	if op.wrap != "" && req.raw == nil {
		req.body = map[string]interface{}{op.wrap: req.body}
	}
	return req, nil
}

func queryValues(v interface{}) []string {
	switch t := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, toString(x))
		}
		return out
	case map[string]interface{}:
		return []string{mustJSON(t)}
	}
	return []string{toString(v)}
}

var restPathRe = regexp.MustCompile(`\{(\+?)([A-Za-z0-9_.]+)(\??)\}`)

func (ec *execCtx) restPath(i int, p string) (string, error) {
	var err error
	out := restPathRe.ReplaceAllStringFunc(p, func(m string) string {
		g := restPathRe.FindStringSubmatch(m)
		v, e := ec.param(i, g[2], "")
		if e != nil {
			err = e
			return ""
		}
		s := strings.TrimSpace(rlValue(v))
		if s == "" && g[3] == "" && err == nil {
			err = permanentError{fmt.Sprintf("%s: parameter %q is required", ec.node.Name, g[2])}
		}
		if g[1] == "+" {
			return s
		}
		return url.PathEscape(s)
	})
	return out, err
}

// restFields applies a field list to dst.
func (ec *execCtx) restFields(i int, spec string, dst map[string]interface{}) error {
	if spec == "" {
		return nil
	}
	prefix := ""
	used := map[string]bool{}
	for _, tok := range strings.Fields(spec) {
		switch {
		case strings.HasPrefix(tok, "@"):
			prefix = strings.TrimPrefix(tok, "@")
			continue
		case strings.HasPrefix(tok, "="):
			k, v, _ := strings.Cut(tok[1:], ":")
			var x interface{}
			if json.Unmarshal([]byte(v), &x) != nil {
				x = v
			}
			restSet(dst, k, x)
			continue
		case tok == "*" || tok == "*snake":
			if prefix == "" {
				continue
			}
			for k, v := range fieldsOf(ec, i, prefix) {
				if used[prefix+"."+k] || restEmpty(v) {
					continue
				}
				if tok == "*snake" {
					k = snakeCase(k)
				}
				dst[k] = v
			}
			continue
		}
		parts := strings.SplitN(tok, ":", 3)
		src := parts[0]
		if prefix != "" {
			src = prefix + "." + src
		}
		used[src] = true
		dstKey := parts[0]
		if len(parts) > 1 && parts[1] != "" {
			dstKey = parts[1]
		}
		v, err := ec.param(i, src, nil)
		if err != nil {
			return err
		}
		if restEmpty(v) {
			continue
		}
		if len(parts) > 2 {
			if v, err = restConv(parts[2], v); err != nil {
				return permanentError{fmt.Sprintf("%s: %s: %v", ec.node.Name, src, err)}
			}
			if restEmpty(v) {
				continue
			}
		}
		restSet(dst, dstKey, v)
	}
	return nil
}

func restEmpty(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []interface{}:
		return len(t) == 0
	case map[string]interface{}:
		if _, rl := t["__rl"]; rl {
			return toString(t["value"]) == ""
		}
		return len(t) == 0
	}
	return false
}

// restConv converts a parameter value:
//
//	rl     resource locator → its value
//	str, int, num, bool
//	json   parse a JSON string
//	csv    "a, b" → ["a","b"];  csvint → [1,2]
//	list   a single value → [value]
//	ms, sec  date → Unix milliseconds / seconds
//	date   → YYYY-MM-DD;  iso → RFC 3339
//	fixed:<name>  fixedCollection {name: [{…}]} → [{…}]
//	kv:<name>     fixedCollection of {key|name, value} pairs → object
func restConv(conv string, v interface{}) (interface{}, error) {
	name, arg, _ := strings.Cut(conv, "=")
	switch name {
	case "rl":
		return rlValue(v), nil
	case "str":
		return rlValue(v), nil
	case "int":
		f, ok := toFloat(v)
		if !ok {
			return nil, fmt.Errorf("%v is not a number", v)
		}
		return int64(f), nil
	case "num":
		f, ok := toFloat(v)
		if !ok {
			return nil, fmt.Errorf("%v is not a number", v)
		}
		return f, nil
	case "bool":
		return toBool(v), nil
	case "json":
		if s, ok := v.(string); ok {
			if strings.TrimSpace(s) == "" {
				return nil, nil
			}
			var x interface{}
			if err := json.Unmarshal([]byte(s), &x); err != nil {
				return nil, fmt.Errorf("invalid JSON")
			}
			return x, nil
		}
		return v, nil
	case "csv", "csvint":
		var parts []interface{}
		if l, ok := v.([]interface{}); ok {
			parts = l
		} else {
			for _, s := range splitList(toString(v)) {
				parts = append(parts, s)
			}
		}
		if name == "csvint" {
			for k, p := range parts {
				if f, ok := toFloat(p); ok {
					parts[k] = int64(f)
				}
			}
		}
		return parts, nil
	case "list":
		if l, ok := v.([]interface{}); ok {
			return l, nil
		}
		return []interface{}{v}, nil
	case "ms", "sec", "date", "iso":
		t, ok := timeOf(v)
		if !ok {
			if f, isNum := toFloat(v); isNum {
				return f, nil
			}
			return nil, fmt.Errorf("invalid date %v", v)
		}
		switch name {
		case "ms":
			return t.UnixMilli(), nil
		case "sec":
			return t.Unix(), nil
		case "date":
			return t.Format("2006-01-02"), nil
		}
		return t.UTC().Format(time.RFC3339), nil
	case "fixed":
		return asList(asMap(v)[arg]), nil
	case "kv":
		out := map[string]interface{}{}
		for _, x := range asList(asMap(v)[arg]) {
			m := asMap(x)
			k := toString(pick(m, "key", "name", "field", "fieldId", "property"))
			if k != "" {
				out[k] = m["value"]
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown conversion %q", conv)
}

func pick(m map[string]interface{}, keys ...string) interface{} {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return nil
}

// restSet writes a field; dots in the key nest objects.
func restSet(m map[string]interface{}, key string, v interface{}) {
	if !strings.Contains(key, ".") {
		m[key] = v
		return
	}
	setPath(m, key, v, true)
}

// jpath reads a dotted path (list indices allowed) from a decoded value.
func jpath(v interface{}, path string) interface{} {
	if path == "" || path == "." {
		return v
	}
	for _, p := range strings.Split(path, ".") {
		switch t := v.(type) {
		case map[string]interface{}:
			v = t[p]
		case []interface{}:
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n >= len(t) {
				return nil
			}
			v = t[n]
		default:
			return nil
		}
	}
	return v
}

// do sends a request and decodes the JSON response (nil when empty).
func (c *restClient) do(ec *execCtx, r *restRequest) (interface{}, error) {
	v, _, err := c.doH(ec, r, "")
	return v, err
}

func (c *restClient) doH(ec *execCtx, r *restRequest, abs string) (interface{}, http.Header, error) {
	u := abs
	if u == "" {
		u = c.base + r.path
		q := url.Values{}
		for k, v := range c.query {
			q[k] = v
		}
		for k, v := range r.query {
			q[k] = v
		}
		if len(q) > 0 {
			sep := "?"
			if strings.Contains(u, "?") {
				sep = "&"
			}
			u += sep + q.Encode()
		}
	}
	body, ct := r.raw, r.contentType
	if body == nil && r.method != http.MethodGet && r.method != http.MethodHead &&
		(len(r.body) > 0 || r.method == http.MethodPost || r.method == http.MethodPut || r.method == http.MethodPatch) {
		body, _ = json.Marshal(r.body)
		ct = "application/json"
	}
	h := map[string]string{"Accept": "application/json"}
	for k, v := range c.headers {
		h[k] = v
	}
	for k, v := range r.headers {
		h[k] = v
	}
	res, err := ec.doRequest(r.method, u, h, body, ct)
	if err != nil {
		return nil, res.header, err
	}
	trimmed := bytes.TrimSpace(res.body)
	if len(trimmed) == 0 {
		return nil, res.header, nil
	}
	var v interface{}
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return map[string]interface{}{"data": string(res.body)}, res.header, nil
	}
	return v, res.header, nil
}

// ---------------------------------------------------------------- pagers

// pageNumber increments a page query parameter (starting at start).
func pageNumber(param string, start int) restPager {
	return func(p *restPage, _ interface{}, _ http.Header) bool {
		p.query.Set(param, strconv.Itoa(start+p.n+1))
		return true
	}
}

// cursorPager copies a cursor from the response into a query parameter.
func cursorPager(respPath, param string) restPager {
	return func(p *restPage, res interface{}, _ http.Header) bool {
		c := toString(jpath(res, respPath))
		if c == "" || c == "<nil>" || p.query.Get(param) == c {
			return false
		}
		p.query.Set(param, c)
		return true
	}
}

// nextURLPager follows an absolute next-page URL in the response.
func nextURLPager(respPath string) restPager {
	return func(p *restPage, res interface{}, _ http.Header) bool {
		u := toString(jpath(res, respPath))
		if u == "" || u == "<nil>" {
			return false
		}
		p.url = u
		return true
	}
}

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="?next"?`)

// linkHeaderPager follows RFC 8288 Link: <…>; rel="next".
func linkHeaderPager(p *restPage, _ interface{}, hdr http.Header) bool {
	m := linkNextRe.FindStringSubmatch(hdr.Get("Link"))
	if m == nil {
		return false
	}
	p.url = m[1]
	return true
}

// lastIDPager sets param to the id of the last record while hasMore is true.
func lastIDPager(hasMore, param string) restPager {
	return func(p *restPage, res interface{}, _ http.Header) bool {
		if !toBool(jpath(res, hasMore)) || len(p.last) == 0 {
			return false
		}
		p.query.Set(param, toString(asMap(p.last[len(p.last)-1])["id"]))
		return true
	}
}

// ---------------------------------------------------------------- clients

// restCred returns the credential selected by the node's authentication
// parameter: byAuth maps its values to credential types, def is the
// default type.
func (ec *execCtx) restCred(i int, def string, byAuth map[string]string) (map[string]interface{}, string, error) {
	t := def
	if a, _ := ec.str(i, "authentication", ""); a != "" {
		if x, ok := byAuth[a]; ok {
			t = x
		}
	}
	c, err := ec.credential(t)
	return c, t, err
}

// bearerClient authenticates with "Authorization: Bearer <token>", the
// token being an OAuth2 access token or one of the API key fields.
func bearerClient(base string, c map[string]interface{}, keyFields ...string) *restClient {
	tok := accessToken(c)
	if k := credString(c, keyFields...); k != "" && len(keyFields) > 0 {
		tok = firstNonEmpty(k, tok)
	}
	return &restClient{base: base, headers: map[string]string{"Authorization": "Bearer " + tok}}
}
