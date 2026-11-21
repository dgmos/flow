package n8n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/models"
	"github.com/dgmos/flow/internal/node"
)

// ---------------------------------------------------------------- memory

type memStore struct {
	mu   sync.Mutex
	data map[string][]ChatMessage
}

var defaultMemory = &memStore{data: map[string][]ChatMessage{}}

func (m *memStore) Load(_ context.Context, key string) ([]ChatMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ChatMessage(nil), m.data[key]...), nil
}

func (m *memStore) Append(_ context.Context, key string, msgs ...ChatMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append(m.data[key], msgs...)
	if len(m.data[key]) > 200 {
		m.data[key] = m.data[key][len(m.data[key])-200:]
	}
	return nil
}

var memoryTypes = map[string]bool{
	"memoryBufferWindow": true, "memoryPostgresChat": true, "memoryRedisChat": true,
	"memoryMongoDbChat": true, "memoryXata": true, "memoryZep": true, "memoryMotorhead": true,
}

type chatMemory struct {
	store  ChatMemoryStore
	key    string
	window int
}

func (ec *execCtx) buildMemory(i int) (*chatMemory, error) {
	subs := aiSubNodes(ec.node.Config, "ai_memory")
	if len(subs) == 0 {
		return nil, nil
	}
	s := subs[0]
	p, err := ec.resolveSub(s, i)
	if err != nil {
		return nil, err
	}
	key := ""
	if toString(p["sessionIdType"]) == "customKey" || toString(p["sessionIdType"]) == "" && p["sessionKey"] != nil {
		key = toString(p["sessionKey"])
	}
	if key == "" {
		if it := ec.items(); i < len(it) {
			key = toString(it[i].JSON["sessionId"])
		}
	}
	if key == "" {
		return nil, permanentError{fmt.Sprintf("%s: no session ID found (expected $json.sessionId or a custom key)", s.name())}
	}
	window := int(toFloatDefault(p["contextWindowLength"], 5))
	store := ChatMemoryStore(defaultMemory)
	if ec.opts.ChatMemory != nil {
		store = ec.opts.ChatMemory
	}
	ns := engine.NamespaceFromContext(ec.ctx)
	return &chatMemory{store: store, key: ns + ":" + key, window: window}, nil
}

func (m *chatMemory) history(ctx context.Context) []chatMsg {
	if m == nil {
		return nil
	}
	msgs, _ := m.store.Load(ctx, m.key)
	if n := m.window * 2; n > 0 && len(msgs) > n {
		msgs = msgs[len(msgs)-n:]
	}
	out := make([]chatMsg, 0, len(msgs))
	for _, cm := range msgs {
		out = append(out, chatMsg{Role: cm.Role, Content: cm.Content})
	}
	return out
}

func (m *chatMemory) save(ctx context.Context, user, assistant string) {
	if m == nil {
		return
	}
	_ = m.store.Append(ctx, m.key, ChatMessage{Role: "user", Content: user}, ChatMessage{Role: "assistant", Content: assistant})
}

// ---------------------------------------------------------------- output parsers

type outputParser struct {
	kind    string // structured | list
	schema  map[string]interface{}
	autoFix bool
}

func (ec *execCtx) buildParser(i int) (*outputParser, error) {
	subs := aiSubNodes(ec.node.Config, "ai_outputParser")
	if len(subs) == 0 {
		return nil, nil
	}
	s := subs[0]
	autoFix := false
	if short(s.typ) == "outputParserAutofixing" {
		inner := aiSubNodes(s.config, "ai_outputParser")
		if len(inner) == 0 {
			return nil, permanentError{"auto-fixing output parser has no inner parser"}
		}
		s, autoFix = inner[0], true
	}
	p, err := ec.resolveSub(s, i)
	if err != nil {
		return nil, err
	}
	switch short(s.typ) {
	case "outputParserItemList":
		return &outputParser{kind: "list", autoFix: autoFix}, nil
	case "outputParserStructured":
		schema, err := schemaFromParams(p, "schemaType", "jsonSchemaExample", "inputSchema", "jsonSchema")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.name(), err)
		}
		return &outputParser{kind: "structured", schema: schema, autoFix: autoFix || toBool(p["autoFix"])}, nil
	}
	return nil, permanentError{fmt.Sprintf("output parser %s is not supported", s.typ)}
}

// schemaFromParams builds a JSON schema from n8n's schema parameters.
func schemaFromParams(p map[string]interface{}, typeKey, exampleKey, manualKey, legacyKey string) (map[string]interface{}, error) {
	st := toString(p[typeKey])
	if st == "manual" || st == "" && p[manualKey] != nil {
		var s map[string]interface{}
		if err := json.Unmarshal([]byte(toString(p[manualKey])), &s); err != nil {
			return nil, fmt.Errorf("input schema is not valid JSON: %v", err)
		}
		return s, nil
	}
	ex := toString(p[exampleKey])
	if ex == "" && legacyKey != "" {
		ex = toString(p[legacyKey])
		var s map[string]interface{}
		if json.Unmarshal([]byte(ex), &s) == nil && s["type"] != nil {
			return s, nil
		}
	}
	if ex == "" {
		ex = `{"state":"California","cities":["Los Angeles","San Francisco"]}`
	}
	var v interface{}
	if err := json.Unmarshal([]byte(ex), &v); err != nil {
		return nil, fmt.Errorf("JSON example is not valid JSON: %v", err)
	}
	return inferSchema(v), nil
}

func inferSchema(v interface{}) map[string]interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		props := map[string]interface{}{}
		var req []string
		for k, x := range t {
			props[k] = inferSchema(x)
			req = append(req, k)
		}
		sort.Strings(req)
		return map[string]interface{}{"type": "object", "properties": props, "required": req}
	case []interface{}:
		items := map[string]interface{}{"type": "string"}
		if len(t) > 0 {
			items = inferSchema(t[0])
		}
		return map[string]interface{}{"type": "array", "items": items}
	case float64:
		return map[string]interface{}{"type": "number"}
	case bool:
		return map[string]interface{}{"type": "boolean"}
	default:
		return map[string]interface{}{"type": "string"}
	}
}

func (p *outputParser) instructions() string {
	if p.kind == "list" {
		return "Return your answer as a list, one item per line, without numbering or extra text."
	}
	return "You must format your final answer as a JSON value that conforms to this JSON schema, with no extra text or markdown:\n" + mustJSON(p.schema)
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

func (p *outputParser) parse(text string) (interface{}, error) {
	if p.kind == "list" {
		var out []interface{}
		for _, l := range strings.Split(text, "\n") {
			l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "-*•0123456789. "))
			if l != "" {
				out = append(out, l)
			}
		}
		return out, nil
	}
	s := strings.TrimSpace(text)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		s = strings.TrimSpace(m[1])
	}
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return unwrapOutput(v), nil
	}
	// Extract the outermost JSON object/array.
	for _, pair := range [][2]string{{"{", "}"}, {"[", "]"}} {
		a, b := strings.Index(s, pair[0]), strings.LastIndex(s, pair[1])
		if a >= 0 && b > a {
			if json.Unmarshal([]byte(s[a:b+1]), &v) == nil {
				return unwrapOutput(v), nil
			}
		}
	}
	return nil, fmt.Errorf("model output is not valid JSON: %s", truncate(text, 200))
}

// unwrapOutput mirrors n8n, which wraps schemas in {output: ...}.
func unwrapOutput(v interface{}) interface{} {
	if m, ok := v.(map[string]interface{}); ok && len(m) == 1 {
		if o, ok := m["output"]; ok {
			return o
		}
	}
	return v
}

// ---------------------------------------------------------------- tools

type aiTool struct {
	def toolDef
	run func(ctx context.Context, args map[string]interface{}) (string, error)
}

var fromAIRe = regexp.MustCompile(`\$fromA[Ii]\(\s*(['"` + "`" + `])([^'"` + "`" + `]+)['"` + "`" + `]\s*(?:,\s*(['"` + "`" + `])((?:\\.|[^\\])*?)['"` + "`" + `]\s*)?(?:,\s*['"](\w+)['"]\s*)?`)

// fromAISchema collects $fromAI(key, description, type) calls.
func fromAISchema(params interface{}) (map[string]interface{}, []string) {
	props := map[string]interface{}{}
	var required []string
	var walk func(v interface{})
	walk = func(v interface{}) {
		switch t := v.(type) {
		case string:
			for _, m := range fromAIRe.FindAllStringSubmatch(t, -1) {
				key, desc, typ := m[2], m[4], m[5]
				if typ == "" || typ == "json" {
					typ = "string"
				}
				if _, seen := props[key]; !seen {
					required = append(required, key)
				}
				prop := map[string]interface{}{"type": typ}
				if desc != "" {
					prop["description"] = desc
				}
				props[key] = prop
			}
		case map[string]interface{}:
			for _, x := range t {
				walk(x)
			}
		case []interface{}:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(params)
	sort.Strings(required)
	return props, required
}

var toolNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func toolName(s string) string {
	s = strings.Trim(toolNameRe.ReplaceAllString(s, "_"), "_")
	if s == "" {
		s = "tool"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func objSchema(props map[string]interface{}, required []string) map[string]interface{} {
	if required == nil {
		required = []string{}
	}
	return map[string]interface{}{"type": "object", "properties": props, "required": required}
}

var querySchema = objSchema(map[string]interface{}{"query": map[string]interface{}{"type": "string", "description": "The input for the tool"}}, []string{"query"})

// toolCtx creates an execCtx for a tool sub-node whose expressions can call
// $fromAI with the model's arguments.
func (ec *execCtx) toolCtx(s subNode, args map[string]interface{}) (*execCtx, error) {
	n := &models.Node{ID: toString(s.config[cfgID]), Name: s.name(), Type: s.typ, Config: s.config}
	tc := &execCtx{ctx: ec.ctx, opts: ec.opts, node: n, params: s.params(), version: toFloatDefault(s.config[cfgVersion], 1), input: ec.input, data: ec.data}
	rt, err := tc.runtime()
	if err != nil {
		return nil, err
	}
	rt.fromAI = args
	return tc, nil
}

func (ec *execCtx) buildTools(i int) ([]aiTool, error) {
	var tools []aiTool
	for _, s := range aiSubNodes(ec.node.Config, "ai_tool") {
		if short(s.typ) == "mcpClientTool" && strings.HasPrefix(s.typ, "@n8n/") {
			ts, err := ec.mcpClientTools(s, i)
			if err != nil {
				return nil, err
			}
			tools = append(tools, ts...)
			continue
		}
		t, err := ec.buildTool(s, i)
		if err != nil {
			return nil, err
		}
		tools = append(tools, t)
	}
	return tools, nil
}

func (ec *execCtx) buildTool(s subNode, i int) (aiTool, error) {
	p := s.params()
	name := toolName(s.name())
	desc := toString(p["description"])
	if desc == "" {
		desc = toString(p["toolDescription"])
	}
	fromProps, fromReq := fromAISchema(p)
	st := short(s.typ)
	switch st {
	case "toolWorkflow":
		if n := toString(p["name"]); n != "" {
			name = toolName(n)
		}
		schema := querySchema
		if len(fromProps) > 0 {
			schema = objSchema(fromProps, fromReq)
		} else if toBool(p["specifyInputSchema"]) {
			sc, err := schemaFromParams(p, "schemaType", "jsonSchemaExample", "inputSchema", "")
			if err != nil {
				return aiTool{}, err
			}
			schema = sc
		}
		return aiTool{def: toolDef{Name: name, Description: desc, Schema: schema}, run: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return ec.runWorkflowTool(s, args, len(fromProps) > 0, toBool(p["specifyInputSchema"]), i)
		}}, nil
	case "toolCode":
		if n := toString(p["name"]); n != "" {
			name = toolName(n)
		}
		schema := objSchema(map[string]interface{}{"query": map[string]interface{}{"type": "string"}}, []string{"query"})
		if toBool(p["specifyInputSchema"]) {
			sc, err := schemaFromParams(p, "schemaType", "jsonSchemaExample", "inputSchema", "")
			if err != nil {
				return aiTool{}, err
			}
			schema = sc
		}
		return aiTool{def: toolDef{Name: name, Description: desc, Schema: schema}, run: func(ctx context.Context, args map[string]interface{}) (string, error) {
			tc, err := ec.toolCtx(s, args)
			if err != nil {
				return "", err
			}
			var q interface{} = args
			if !toBool(p["specifyInputSchema"]) {
				q = args["query"]
			}
			_ = tc.rt.vm.Set("query", tc.rt.vm.ToValue(q))
			code := toString(p["jsCode"])
			if code == "" {
				code = toString(p["code"])
			}
			v, err := tc.rt.runJS(code, ec.opts.codeTimeout())
			if err != nil {
				return "", err
			}
			if s, ok := v.(string); ok {
				return s, nil
			}
			return mustJSON(v), nil
		}}, nil
	case "toolCalculator":
		return aiTool{def: toolDef{Name: "calculator", Description: "Useful for getting the result of a math expression. The input must be a valid mathematical expression.", Schema: objSchema(map[string]interface{}{"input": map[string]interface{}{"type": "string"}}, []string{"input"})},
			run: func(ctx context.Context, args map[string]interface{}) (string, error) {
				tc, err := ec.toolCtx(s, args)
				if err != nil {
					return "", err
				}
				expr := strings.NewReplacer("^", "**").Replace(toString(args["input"]))
				if !regexp.MustCompile(`^[\d\s+\-*/().,%a-zA-Z_]*$`).MatchString(expr) {
					return "", errors.New("invalid math expression")
				}
				v, err := tc.rt.runJS("const {abs,acos,asin,atan,atan2,cbrt,ceil,cos,exp,floor,log,log10,log2,max,min,pow,random,round,sign,sin,sqrt,tan,trunc,PI,E} = Math;\nreturn ("+expr+");", ec.opts.exprTimeout())
				if err != nil {
					return "", err
				}
				return toString(v), nil
			}}, nil
	case "toolThink":
		return aiTool{def: toolDef{Name: "think", Description: firstNonEmpty(desc, "Use the tool to think about something. It will not obtain new information, just record the thought."), Schema: objSchema(map[string]interface{}{"input": map[string]interface{}{"type": "string", "description": "A thought to think about."}}, []string{"input"})},
			run: func(ctx context.Context, args map[string]interface{}) (string, error) {
				return toString(args["input"]), nil
			}}, nil
	case "toolWikipedia":
		return aiTool{def: toolDef{Name: "wikipedia", Description: "A tool for interacting with and fetching data from the Wikipedia API. The input should be a search query.", Schema: objSchema(map[string]interface{}{"input": map[string]interface{}{"type": "string"}}, []string{"input"})},
			run: func(ctx context.Context, args map[string]interface{}) (string, error) {
				return ec.wikipedia(ctx, toString(args["input"]))
			}}, nil
	case "toolSerpApi":
		return aiTool{def: toolDef{Name: "serpapi", Description: "A search engine. Useful for answering questions about current events. Input should be a search query.", Schema: objSchema(map[string]interface{}{"input": map[string]interface{}{"type": "string"}}, []string{"input"})},
			run: func(ctx context.Context, args map[string]interface{}) (string, error) {
				c, err := ec.subCredential(s, "serpApi")
				if err != nil {
					return "", err
				}
				u := "https://serpapi.com/search.json?" + url.Values{"q": {toString(args["input"])}, "api_key": {credString(c, "apiKey")}}.Encode()
				return ec.toolGET(ctx, u, func(m map[string]interface{}) string {
					if ab, ok := m["answer_box"]; ok {
						return mustJSON(ab)
					}
					res := asList(m["organic_results"])
					if len(res) > 5 {
						res = res[:5]
					}
					var parts []string
					for _, r := range res {
						o, _ := r.(map[string]interface{})
						parts = append(parts, fmt.Sprintf("%s\n%s\n%s", o["title"], o["snippet"], o["link"]))
					}
					return strings.Join(parts, "\n\n")
				})
			}}, nil
	case "toolHttpRequest", "httpRequestTool":
		return ec.httpTool(s, name, desc, fromProps, fromReq)
	case "agentTool":
		return ec.agentTool(s, name, desc, i), nil
	}
	if st == "toolVectorStore" || vectorStoreTypes[st] {
		return ec.vectorStoreTool(s, i)
	}
	if fn, ok := integrationToolBase(s.typ); ok {
		if isCommunityType(s.typ) {
			// Community tools mark AI-provided values as {placeholder}.
			for _, ph := range placeholders(p) {
				if _, ok := fromProps[ph]; !ok {
					fromProps[ph] = map[string]interface{}{"type": "string"}
					fromReq = append(fromReq, ph)
				}
			}
		}
		return ec.integrationTool(s, fn, name, desc, fromProps, fromReq), nil
	}
	return aiTool{}, permanentError{fmt.Sprintf("tool %s is not supported", s.typ)}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (ec *execCtx) toolGET(ctx context.Context, u string, format func(map[string]interface{}) string) (string, error) {
	client := outboundClient(ec.opts.Outbound)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", sanitizeErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := node.ReadLimited(resp.Body, ec.opts.Outbound.MaxResponseBytesOrDefault())
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("request failed with status %d", resp.StatusCode)
	}
	var m map[string]interface{}
	if json.Unmarshal(body, &m) != nil {
		return truncate(string(body), 20000), nil
	}
	return truncate(format(m), 20000), nil
}

func (ec *execCtx) wikipedia(ctx context.Context, q string) (string, error) {
	u := "https://en.wikipedia.org/w/api.php?" + url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {q}, "format": {"json"}, "srlimit": {"3"}}.Encode()
	return ec.toolGET(ctx, u, func(m map[string]interface{}) string {
		query, _ := m["query"].(map[string]interface{})
		var parts []string
		for _, r := range asList(query["search"]) {
			o, _ := r.(map[string]interface{})
			snippet := regexp.MustCompile(`<[^>]+>`).ReplaceAllString(toString(o["snippet"]), "")
			parts = append(parts, fmt.Sprintf("Page: %s\nSummary: %s", o["title"], snippet))
		}
		if len(parts) == 0 {
			return "No good Wikipedia Search Result was found"
		}
		return strings.Join(parts, "\n\n")
	})
}

var placeholderRe = regexp.MustCompile(`\{([a-zA-Z_][\w-]*)\}`)

// httpTool implements the HTTP Request tool (placeholder and $fromAI styles).
func (ec *execCtx) httpTool(s subNode, name, desc string, fromProps map[string]interface{}, fromReq []string) (aiTool, error) {
	p := s.params()
	props := map[string]interface{}{}
	var req []string
	for k, v := range fromProps {
		props[k] = v
	}
	req = append(req, fromReq...)
	defs := map[string]map[string]interface{}{}
	if pd, ok := p["placeholderDefinitions"].(map[string]interface{}); ok {
		for _, raw := range asList(pd["values"]) {
			d, _ := raw.(map[string]interface{})
			defs[toString(d["name"])] = d
		}
	}
	addPH := func(name string) {
		if _, ok := props[name]; ok {
			return
		}
		prop := map[string]interface{}{"type": "string"}
		if d, ok := defs[name]; ok {
			if t := toString(d["type"]); t != "" && t != "json" {
				prop["type"] = t
			}
			if dd := toString(d["description"]); dd != "" {
				prop["description"] = dd
			}
		}
		props[name] = prop
		req = append(req, name)
	}
	for _, key := range []string{"url", "jsonBody", "jsonQuery", "jsonHeaders", "body"} {
		for _, m := range placeholderRe.FindAllStringSubmatch(toString(p[key]), -1) {
			addPH(m[1])
		}
	}
	for _, group := range []string{"parametersQuery", "parametersHeaders", "parametersBody"} {
		g, _ := p[group].(map[string]interface{})
		for _, raw := range asList(g["values"]) {
			v, _ := raw.(map[string]interface{})
			if vp := toString(v["valueProvider"]); vp == "modelRequired" || vp == "modelOptional" {
				addPH(toString(v["name"]))
			}
		}
	}
	sort.Strings(req)
	return aiTool{def: toolDef{Name: name, Description: firstNonEmpty(desc, "Makes an HTTP request and returns the response data"), Schema: objSchema(props, req)},
		run: func(ctx context.Context, args map[string]interface{}) (string, error) {
			tc, err := ec.toolCtx(s, args)
			if err != nil {
				return "", err
			}
			subst := func(str string) string {
				return placeholderRe.ReplaceAllStringFunc(str, func(m string) string {
					k := m[1 : len(m)-1]
					if v, ok := args[k]; ok {
						if sv, ok := v.(string); ok {
							return sv
						}
						return mustJSON(v)
					}
					return m
				})
			}
			// Translate tool-style parameters into HTTP Request parameters.
			hp := map[string]interface{}{}
			for k, v := range tc.params {
				hp[k] = v
			}
			for _, key := range []string{"url", "jsonBody", "jsonQuery", "jsonHeaders", "body"} {
				if sv, ok := hp[key].(string); ok {
					hp[key] = subst(sv)
				}
			}
			for group, target := range map[string]string{"parametersQuery": "queryParameters", "parametersHeaders": "headerParameters", "parametersBody": "bodyParameters"} {
				g, _ := hp[group].(map[string]interface{})
				var list []interface{}
				for _, raw := range asList(g["values"]) {
					v, _ := raw.(map[string]interface{})
					val := v["value"]
					if vp := toString(v["valueProvider"]); vp == "modelRequired" || vp == "modelOptional" {
						val = args[toString(v["name"])]
					}
					list = append(list, map[string]interface{}{"name": v["name"], "value": val})
				}
				if len(list) > 0 {
					hp[target] = map[string]interface{}{"parameters": list}
				}
			}
			for _, k := range []string{"specifyQuery", "specifyHeaders", "specifyBody"} {
				if toString(hp[k]) == "model" {
					hp[k] = "keypair"
				}
			}
			tc.params = hp
			tc.version = 4.2
			spec, err := buildHTTPSpec(tc, 0)
			if err != nil {
				return "", err
			}
			spec.format = "text"
			client := outboundClient(ec.opts.Outbound)
			items, err := doHTTP(tc, client, ec.opts.Outbound.MaxResponseBytesOrDefault(), spec)
			if err != nil {
				return "", err
			}
			if len(items) == 1 {
				return truncate(toString(items[0].JSON["data"]), 20000), nil
			}
			return truncate(mustJSON(items), 20000), nil
		}}, nil
}

// runWorkflowTool executes a Call Workflow tool.
func (ec *execCtx) runWorkflowTool(s subNode, args map[string]interface{}, mapped, schemaArgs bool, i int) (string, error) {
	eng := engine.EngineFromContext(ec.ctx)
	if eng == nil {
		return "", errors.New("workflow tool: no engine in context")
	}
	tc, err := ec.toolCtx(s, args)
	if err != nil {
		return "", err
	}
	child, err := loadChildWorkflow(tc, eng, ec.opts)
	if err != nil {
		return "", err
	}
	var item map[string]interface{}
	if mapped {
		v, err := tc.param(i, "workflowInputs.value", map[string]interface{}{})
		if err != nil {
			return "", err
		}
		item, _ = v.(map[string]interface{})
	} else if schemaArgs {
		item = args
	} else {
		item = map[string]interface{}{"query": args["query"]}
	}
	if item == nil {
		item = map[string]interface{}{}
	}
	if f, err := tc.param(i, "fields.values", nil); err == nil {
		for _, raw := range asList(f) {
			fv, _ := raw.(map[string]interface{})
			typ := toString(fv["type"])
			if typ == "" {
				typ = "stringValue"
			}
			item[toString(fv["name"])] = fv[typ]
		}
	}
	res, err := runChildWorkflow(ec.ctx, eng, child, []Item{{JSON: item}}, ec.opts)
	if err != nil {
		return "", err
	}
	if len(res) == 1 {
		if r, ok := res[0].JSON["response"]; ok {
			if s, ok := r.(string); ok {
				return s, nil
			}
			return mustJSON(r), nil
		}
		return mustJSON(res[0].JSON), nil
	}
	list := make([]interface{}, len(res))
	for k, it := range res {
		list[k] = it.JSON
	}
	return mustJSON(list), nil
}

// ---------------------------------------------------------------- agent loop

type agentResult struct {
	text  string
	steps []interface{}
}

func (ec *execCtx) runAgent(ctx context.Context, model chatModel, base llmRequest, system, prompt string, tools []aiTool, mem *chatMemory, maxIter int) (*agentResult, error) {
	msgs := []chatMsg{}
	if system != "" {
		msgs = append(msgs, chatMsg{Role: "system", Content: system})
	}
	msgs = append(msgs, mem.history(ctx)...)
	msgs = append(msgs, chatMsg{Role: "user", Content: prompt})
	req := base
	byName := map[string]aiTool{}
	for _, t := range tools {
		req.Tools = append(req.Tools, t.def)
		byName[t.def.Name] = t
	}
	if maxIter <= 0 {
		maxIter = 10
	}
	res := &agentResult{}
	for iter := 0; iter <= maxIter; iter++ {
		req.Messages = msgs
		if iter == maxIter {
			req.Tools = nil // force a final answer
		}
		out, err := model.Chat(ctx, req)
		if err != nil {
			return nil, err
		}
		if len(out.ToolCalls) == 0 || iter == maxIter {
			res.text = out.Content
			return res, nil
		}
		msgs = append(msgs, chatMsg{Role: "assistant", Content: out.Content, ToolCalls: out.ToolCalls})
		for _, tc := range out.ToolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Args), &args); err != nil || args == nil {
				args = map[string]interface{}{"input": tc.Args, "query": tc.Args}
			}
			var observation string
			tool, ok := byName[tc.Name]
			if !ok {
				observation = fmt.Sprintf("Error: tool %q does not exist", tc.Name)
			} else if r, err := tool.run(ctx, args); err != nil {
				observation = "Error: " + err.Error()
			} else {
				observation = r
			}
			res.steps = append(res.steps, map[string]interface{}{"action": map[string]interface{}{"tool": tc.Name, "toolInput": args}, "observation": observation})
			msgs = append(msgs, chatMsg{Role: "tool", Content: observation, ToolCallID: tc.ID, ToolName: tc.Name})
		}
	}
	return res, nil
}

// promptFor returns the user prompt of an agent/chain node for item i.
func (ec *execCtx) promptFor(i int, textKey string) (string, error) {
	pt, _ := ec.str(i, "promptType", "")
	if pt == "define" || pt == "" && ec.hasParam(textKey) {
		return ec.str(i, textKey, "")
	}
	items := ec.items()
	if i < len(items) {
		for _, k := range []string{"chatInput", "input", "text"} {
			if v, ok := items[i].JSON[k]; ok {
				return toString(v), nil
			}
		}
	}
	return "", permanentError{fmt.Sprintf("%s: no prompt specified (expected $json.chatInput or a defined prompt)", ec.node.Name)}
}

func (ec *execCtx) hasParam(path string) bool {
	_, ok := ec.raw(path)
	return ok
}

func agentNode(ec *execCtx) ([][]Item, error) {
	var out []Item
	for i := range ec.items() {
		j, err := ec.agentItem(i)
		if err != nil {
			return nil, err
		}
		out = append(out, Item{JSON: j, PairedItem: paired(i)})
	}
	return [][]Item{out}, nil
}

// agentItem runs the AI Agent for item i.
func (ec *execCtx) agentItem(i int) (map[string]interface{}, error) {
	model, base, err := ec.buildModel(i)
	if err != nil {
		return nil, err
	}
	prompt, err := ec.promptFor(i, "text")
	if err != nil {
		return nil, err
	}
	system, _ := ec.str(i, "options.systemMessage", "You are a helpful assistant")
	maxIter, _ := ec.num(i, "options.maxIterations", 10)
	tools, err := ec.buildTools(i)
	if err != nil {
		return nil, err
	}
	mem, err := ec.buildMemory(i)
	if err != nil {
		return nil, err
	}
	var parser *outputParser
	if ok, _ := ec.boolean(i, "hasOutputParser", false); ok || len(aiSubNodes(ec.node.Config, "ai_outputParser")) > 0 {
		if parser, err = ec.buildParser(i); err != nil {
			return nil, err
		}
	}
	if parser != nil {
		system += "\n\n" + parser.instructions()
	}
	res, err := ec.runAgent(ec.ctx, model, base, system, prompt, tools, mem, int(maxIter))
	if err != nil {
		return nil, err
	}
	j := map[string]interface{}{"output": res.text}
	if parser != nil {
		v, err := parseWithFix(ec.ctx, model, base, parser, res.text)
		if err != nil {
			return nil, err
		}
		j["output"] = v
	}
	if ret, _ := ec.boolean(i, "options.returnIntermediateSteps", false); ret {
		j["intermediateSteps"] = res.steps
	}
	mem.save(ec.ctx, prompt, res.text)
	return j, nil
}

// agentToolDefaultPrompt is the AI Agent Tool's default prompt parameter.
const agentToolDefaultPrompt = "={{ $fromAI('Prompt__User_Message_', ``, 'string') }}"

// agentTool exposes an AI Agent Tool sub-node (an agent with its own model,
// tools and memory) as a tool of its parent agent.
func (ec *execCtx) agentTool(s subNode, name, desc string, i int) aiTool {
	if _, ok := s.params()["text"]; !ok {
		cfg := cloneJSON(s.config)
		p := cloneJSON(s.params())
		p["text"] = agentToolDefaultPrompt
		cfg[cfgParams] = p
		s = subNode{typ: s.typ, config: cfg}
	}
	props, req := fromAISchema(s.params())
	schema := querySchema
	if len(props) > 0 {
		schema = objSchema(props, req)
	}
	return aiTool{def: toolDef{Name: name, Description: firstNonEmpty(desc, "Delegates the task to a specialized AI agent."), Schema: schema},
		run: func(ctx context.Context, args map[string]interface{}) (string, error) {
			tc, err := ec.toolCtx(s, args)
			if err != nil {
				return "", err
			}
			p := cloneJSON(tc.params)
			p["promptType"] = "define"
			if len(props) == 0 && strings.TrimSpace(toString(p["text"])) == "" {
				p["text"] = toString(args["query"])
			}
			tc.params = p
			j, err := tc.agentItem(i)
			if err != nil {
				return "", err
			}
			if s, ok := j["output"].(string); ok {
				return s, nil
			}
			return mustJSON(j["output"]), nil
		}}
}

func parseWithFix(ctx context.Context, model chatModel, base llmRequest, p *outputParser, text string) (interface{}, error) {
	v, err := p.parse(text)
	if err == nil || !p.autoFix {
		return v, err
	}
	req := base
	req.JSONMode = true
	req.Messages = []chatMsg{{Role: "user", Content: "Fix this output so that it is valid and matches the instructions.\nInstructions: " + p.instructions() + "\nOutput:\n" + text}}
	out, ferr := model.Chat(ctx, req)
	if ferr != nil {
		return nil, err
	}
	return p.parse(out.Content)
}

// chainLlmNode implements the Basic LLM Chain.
func chainLlmNode(ec *execCtx) ([][]Item, error) {
	var out []Item
	for i := range ec.items() {
		model, base, err := ec.buildModel(i)
		if err != nil {
			return nil, err
		}
		key := "text"
		if !ec.hasParam("text") && ec.hasParam("prompt") {
			key = "prompt"
		}
		prompt, err := ec.promptFor(i, key)
		if err != nil {
			return nil, err
		}
		var msgs []chatMsg
		if mv, err := ec.param(i, "messages.messageValues", nil); err == nil {
			for _, raw := range asList(mv) {
				m, _ := raw.(map[string]interface{})
				role := map[string]string{"SystemMessagePromptTemplate": "system", "AIMessagePromptTemplate": "assistant"}[toString(m["type"])]
				if role == "" {
					role = "user"
				}
				msgs = append(msgs, chatMsg{Role: role, Content: toString(m["message"])})
			}
		}
		var parser *outputParser
		if ok, _ := ec.boolean(i, "hasOutputParser", false); ok || len(aiSubNodes(ec.node.Config, "ai_outputParser")) > 0 {
			if parser, err = ec.buildParser(i); err != nil {
				return nil, err
			}
		}
		if parser != nil {
			msgs = append([]chatMsg{{Role: "system", Content: parser.instructions()}}, msgs...)
		}
		req := base
		req.Messages = append(msgs, chatMsg{Role: "user", Content: prompt})
		res, err := model.Chat(ec.ctx, req)
		if err != nil {
			return nil, err
		}
		j := map[string]interface{}{"text": res.Content}
		if parser != nil {
			v, err := parseWithFix(ec.ctx, model, base, parser, res.Content)
			if err != nil {
				return nil, err
			}
			j = map[string]interface{}{"output": v}
		}
		out = append(out, Item{JSON: j, PairedItem: paired(i)})
	}
	return [][]Item{out}, nil
}

// openAiMessageNode implements the OpenAI node's "Message a Model" operation.
func openAiMessageNode(ec *execCtx) ([][]Item, error) {
	res, _ := ec.str(0, "resource", "text")
	op, _ := ec.str(0, "operation", "message")
	if res != "text" || op != "message" {
		return nil, permanentError{fmt.Sprintf("OpenAI node: %s/%s is not supported", res, op)}
	}
	c, err := ec.credential("openAiApi")
	if err != nil {
		return nil, err
	}
	var out []Item
	for i := range ec.items() {
		modelID, err := ec.param(i, "modelId", "gpt-4o-mini")
		if err != nil {
			return nil, err
		}
		model := &openAIModel{client: outboundClient(ec.opts.Outbound), limit: ec.opts.Outbound.MaxResponseBytesOrDefault(),
			baseURL: credBaseURL(c, "https://api.openai.com/v1"), headers: map[string]string{"Authorization": "Bearer " + credString(c, "apiKey")}, model: rlValue(modelID)}
		var msgs []chatMsg
		if sys, _ := ec.str(i, "options.instructions", ""); sys != "" {
			msgs = append(msgs, chatMsg{Role: "system", Content: sys})
		}
		mv, err := ec.param(i, "messages.values", nil)
		if err != nil {
			return nil, err
		}
		for _, raw := range asList(mv) {
			m, _ := raw.(map[string]interface{})
			role := toString(m["role"])
			if role == "" {
				role = "user"
			}
			msgs = append(msgs, chatMsg{Role: role, Content: toString(m["content"])})
		}
		var base llmRequest
		if t, err := ec.num(i, "options.temperature", -1); err == nil && t >= 0 {
			base.Temperature = &t
		}
		if n, _ := ec.num(i, "options.maxTokens", 0); n > 0 {
			base.MaxTokens = int(n)
		}
		jsonOut, _ := ec.boolean(i, "jsonOutput", false)
		base.JSONMode = jsonOut
		tools, err := ec.buildTools(i)
		if err != nil {
			return nil, err
		}
		var content interface{}
		if len(tools) > 0 {
			system, prompt := "", ""
			for _, m := range msgs {
				if m.Role == "system" {
					system += m.Content
				} else {
					prompt += m.Content + "\n"
				}
			}
			r, err := ec.runAgent(ec.ctx, model, base, system, strings.TrimSpace(prompt), tools, nil, 10)
			if err != nil {
				return nil, err
			}
			content = r.text
		} else {
			base.Messages = msgs
			r, err := model.Chat(ec.ctx, base)
			if err != nil {
				return nil, err
			}
			content = r.Content
		}
		if jsonOut {
			var v interface{}
			if json.Unmarshal([]byte(toString(content)), &v) == nil {
				content = v
			}
		}
		out = append(out, Item{JSON: map[string]interface{}{
			"index": 0.0, "message": map[string]interface{}{"role": "assistant", "content": content, "refusal": nil},
			"logprobs": nil, "finish_reason": "stop",
		}, PairedItem: paired(i)})
	}
	return [][]Item{out}, nil
}

// classify asks the model to pick categories and returns their indexes.
func classify(ctx context.Context, model chatModel, base llmRequest, system, text string, cats []string, descs []string, multi bool) ([]int, error) {
	var b strings.Builder
	for k, c := range cats {
		fmt.Fprintf(&b, "- %s", c)
		if descs[k] != "" {
			fmt.Fprintf(&b, ": %s", descs[k])
		}
		b.WriteString("\n")
	}
	rule := "exactly one category"
	if multi {
		rule = "all categories that apply"
	}
	req := base
	req.JSONMode = true
	req.Messages = []chatMsg{
		{Role: "system", Content: system + "\nCategories:\n" + b.String() + "\nChoose " + rule + `. Respond only with JSON: {"categories": ["<category>", ...]}. Use an empty list if none apply.`},
		{Role: "user", Content: text},
	}
	out, err := model.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	p := &outputParser{kind: "structured"}
	v, err := p.parse(out.Content)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]interface{})
	var idx []int
	for _, c := range asList(m["categories"]) {
		for k, name := range cats {
			if strings.EqualFold(strings.TrimSpace(toString(c)), name) {
				idx = append(idx, k)
			}
		}
	}
	return idx, nil
}

func textClassifierNode(ec *execCtx) ([][]Item, error) {
	cr, _ := ec.raw("categories.categories")
	cats := asList(cr)
	fallback, _ := ec.str(0, "options.fallback", "discard")
	out := make([][]Item, len(cats)+1)
	for i, it := range ec.items() {
		model, base, err := ec.buildModel(i)
		if err != nil {
			return nil, err
		}
		text, err := ec.str(i, "inputText", "")
		if err != nil {
			return nil, err
		}
		var names, descs []string
		for k := range cats {
			c, _ := ec.param(i, fmt.Sprintf("categories.categories.%d", k), nil)
			m, _ := c.(map[string]interface{})
			names = append(names, toString(m["category"]))
			descs = append(descs, toString(m["description"]))
		}
		multi, _ := ec.boolean(i, "options.multiClass", false)
		system, _ := ec.str(i, "options.systemPromptTemplate", "Please classify the text provided by the user into one of the following categories.")
		idx, err := classify(ec.ctx, model, base, system, text, names, descs, multi)
		if err != nil {
			return nil, err
		}
		it.PairedItem = paired(i)
		for _, k := range idx {
			out[k] = append(out[k], it)
		}
		if len(idx) == 0 && fallback == "other" {
			out[len(cats)] = append(out[len(cats)], it)
		}
	}
	if fallback != "other" {
		out = out[:len(cats)]
	}
	return out, nil
}

func sentimentNode(ec *execCtx) ([][]Item, error) {
	catStr, _ := ec.str(0, "options.categories", "Positive, Neutral, Negative")
	cats := splitList(catStr)
	out := make([][]Item, len(cats))
	for i, it := range ec.items() {
		model, base, err := ec.buildModel(i)
		if err != nil {
			return nil, err
		}
		text, err := ec.str(i, "inputText", "")
		if err != nil {
			return nil, err
		}
		idx, err := classify(ec.ctx, model, base, "You are highly intelligent and accurate sentiment analyzer. Analyze the sentiment of the provided text.", text, cats, make([]string, len(cats)), false)
		if err != nil {
			return nil, err
		}
		if len(idx) == 0 {
			continue
		}
		j := cloneJSON(it.JSON)
		if d, _ := ec.boolean(i, "options.includeDetailedResults", false); d {
			j["sentimentAnalysis"] = map[string]interface{}{"category": cats[idx[0]]}
		}
		out[idx[0]] = append(out[idx[0]], Item{JSON: j, PairedItem: paired(i)})
	}
	return out, nil
}

func informationExtractorNode(ec *execCtx) ([][]Item, error) {
	var out []Item
	for i := range ec.items() {
		model, base, err := ec.buildModel(i)
		if err != nil {
			return nil, err
		}
		text, err := ec.str(i, "text", "")
		if err != nil {
			return nil, err
		}
		var schema map[string]interface{}
		st, _ := ec.str(i, "schemaType", "fromAttributes")
		if st == "fromAttributes" {
			ar, _ := ec.param(i, "attributes.attributes", nil)
			props := map[string]interface{}{}
			var req []string
			for _, raw := range asList(ar) {
				a, _ := raw.(map[string]interface{})
				typ := toString(a["type"])
				if typ == "" || typ == "date" {
					typ = "string"
				}
				props[toString(a["name"])] = map[string]interface{}{"type": typ, "description": toString(a["description"])}
				if toBool(a["required"]) {
					req = append(req, toString(a["name"]))
				}
			}
			schema = objSchema(props, req)
		} else {
			p := map[string]interface{}{}
			for _, k := range []string{"schemaType", "jsonSchemaExample", "inputSchema"} {
				v, _ := ec.param(i, k, nil)
				p[k] = v
			}
			if schema, err = schemaFromParams(p, "schemaType", "jsonSchemaExample", "inputSchema", ""); err != nil {
				return nil, err
			}
		}
		system, _ := ec.str(i, "options.systemPromptTemplate", "You are an expert extraction algorithm. Only extract relevant information from the text. If you do not know the value of an attribute asked to extract, you may omit the attribute's value.")
		parser := &outputParser{kind: "structured", schema: schema, autoFix: true}
		req := base
		req.JSONMode = true
		req.Messages = []chatMsg{{Role: "system", Content: system + "\n\n" + parser.instructions()}, {Role: "user", Content: text}}
		res, err := model.Chat(ec.ctx, req)
		if err != nil {
			return nil, err
		}
		v, err := parseWithFix(ec.ctx, model, base, parser, res.Content)
		if err != nil {
			return nil, err
		}
		out = append(out, Item{JSON: map[string]interface{}{"output": v}, PairedItem: paired(i)})
	}
	return [][]Item{out}, nil
}

// integrationToolBase maps "<node>Tool" to the regular node's implementation.
func integrationToolBase(typ string) (nodeFunc, bool) {
	if !strings.HasSuffix(typ, "Tool") {
		return nil, false
	}
	base := strings.TrimSuffix(typ, "Tool")
	if strings.HasPrefix(typ, "@n8n/") && !vendorToolBases[short(base)] {
		return nil, false
	}
	if fn, ok := nodeTypes()[base]; ok {
		return fn, true
	}
	fn, ok := extraNodeTypes(&Options{})[base]
	return fn, ok
}

// isCommunityType reports whether a node type comes from a community
// package rather than n8n itself.
func isCommunityType(typ string) bool {
	return !strings.HasPrefix(typ, "n8n-nodes-base.") && !strings.HasPrefix(typ, "@n8n/")
}

// vendorToolBases are AI vendor nodes that n8n also offers as tools
// (e.g. googleGeminiTool).
var vendorToolBases = map[string]bool{"googleGemini": true, "anthropic": true, "openAi": true}

// integrationTool exposes a regular integration node (e.g. Gmail) as an AI
// tool; its $fromAI() parameters become the tool's arguments.
func (ec *execCtx) integrationTool(s subNode, fn nodeFunc, name, desc string, props map[string]interface{}, req []string) aiTool {
	if desc == "" {
		desc = "Use the " + s.name() + " tool"
	}
	return aiTool{def: toolDef{Name: name, Description: desc, Schema: objSchema(props, req)},
		run: func(ctx context.Context, args map[string]interface{}) (string, error) {
			if isCommunityType(s.typ) {
				s = withPlaceholders(s, args)
			}
			tc, err := ec.toolCtx(s, args)
			if err != nil {
				return "", err
			}
			if fb, ok := extraNodeTypes(ec.opts)[strings.TrimSuffix(s.typ, "Tool")]; ok {
				fn = fb // bind options-dependent implementations
			}
			outs, err := fn(tc)
			if err != nil {
				return "", err
			}
			var list []interface{}
			for _, o := range outs {
				for _, it := range o {
					list = append(list, it.JSON)
				}
			}
			if len(list) == 1 {
				return truncate(mustJSON(list[0]), 20000), nil
			}
			return truncate(mustJSON(list), 20000), nil
		}}
}

// aiSubNodeSupported reports whether a sub-node type can be folded into a
// supported root node.
func aiSubNodeSupported(typ string) bool {
	if _, ok := integrationToolBase(typ); ok {
		return true
	}
	if ragSubNodeSupported(typ) {
		return true
	}
	s := short(typ)
	if modelSubNodeSupported(typ) || memoryTypes[s] {
		return true
	}
	switch s {
	case "outputParserStructured", "outputParserAutofixing", "outputParserItemList",
		"toolWorkflow", "toolCode", "toolCalculator", "toolThink", "toolWikipedia", "toolSerpApi", "toolHttpRequest", "httpRequestTool", "agentTool":
		return true
	case "mcpClientTool":
		return strings.HasPrefix(typ, "@n8n/")
	}
	return false
}
