package n8n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/models"
	"github.com/dgmos/flow/internal/node"
)

// Options configures the n8n runtime.
type Options struct {
	// Timezone is the default zone for $now/DateTime when the workflow has no
	// settings.timezone. Empty means the host's local zone.
	Timezone string
	// Env is exposed as $env. Empty by default: n8n blocks env access for
	// untrusted workflows and so do we.
	Env map[string]string
	// ExpressionTimeout bounds each expression evaluation (default 5s).
	ExpressionTimeout time.Duration
	// CodeTimeout bounds each Code node run (default 60s).
	CodeTimeout time.Duration
	// Credentials resolves n8n credential references. Nil means no
	// credentials are available.
	Credentials CredentialProvider
	// Outbound restricts HTTP destinations of n8n nodes (SSRF protection).
	Outbound node.OutboundPolicy
	// ChatMemory stores conversation history for AI memory nodes. Nil uses a
	// process-local in-memory store.
	ChatMemory ChatMemoryStore
	// WorkflowResolver maps an n8n workflow ID referenced by Execute
	// Workflow / tool nodes to a flow workflow ID in the same namespace.
	// Nil means IDs are used as-is.
	WorkflowResolver func(n8nWorkflowID string) string
	// APIBaseURLs overrides external API base URLs by service name
	// ("telegram", "sheets", "gmail", "drive", "googleOAuth"), e.g. to route
	// through an egress proxy or a test server.
	APIBaseURLs map[string]string
	// PollState stores polling trigger cursors (Gmail/Sheets/Drive
	// triggers). Nil uses process memory; provide a durable store so polls
	// resume correctly after restarts and across instances.
	PollState PollStateStore
	// ResumeBaseURL is the public URL the n8n handler is mounted at (e.g.
	// "https://app.example.com/n8n"). With ResumeSecret it enables
	// $execution.resumeUrl, send-and-wait approvals and multi-page forms.
	ResumeBaseURL string
	// ResumeSecret signs resume URLs (HMAC-SHA256). Keep it secret.
	ResumeSecret string
	// FileSystemRoot enables the Read/Write Files nodes, confined to this
	// directory. Empty (default) disables file access.
	FileSystemRoot string
	// AllowLocalProcesses lets imported workflows start local processes:
	// Execute Command, Python Code and STDIO MCP servers. Off by default;
	// enable only for trusted workflows on isolated workers.
	AllowLocalProcesses bool
	// PythonPath is the interpreter for Python Code nodes (default python3).
	PythonPath string
	// DataTables stores n8n Data Tables. Nil uses a process-local in-memory
	// store; provide a durable one for production.
	DataTables DataTableStore
}

func (o *Options) exprTimeout() time.Duration {
	if o == nil || o.ExpressionTimeout <= 0 {
		return 5 * time.Second
	}
	return o.ExpressionTimeout
}

func (o *Options) codeTimeout() time.Duration {
	if o == nil || o.CodeTimeout <= 0 {
		return 60 * time.Second
	}
	return o.CodeTimeout
}

func (o *Options) env() map[string]string {
	if o == nil || o.Env == nil {
		return map[string]string{}
	}
	return o.Env
}

func (o *Options) timezone(wf *models.Workflow) string {
	if wf != nil {
		if s, ok := wf.Metadata["settings"].(map[string]interface{}); ok {
			if tz, ok := s["timezone"].(string); ok && tz != "" && tz != "DEFAULT" {
				return tz
			}
		}
	}
	if o != nil && o.Timezone != "" {
		return o.Timezone
	}
	return time.Local.String()
}

// Node config keys written by the importer.
const (
	cfgParams      = "parameters"
	cfgVersion     = "typeVersion"
	cfgCredentials = "credentials"
	cfgSettings    = "settings"
	cfgID          = "__id"
	cfgName        = "__name"
)

func nodeParams(n *models.Node) map[string]interface{} {
	if n == nil || n.Config == nil {
		return map[string]interface{}{}
	}
	p, _ := n.Config[cfgParams].(map[string]interface{})
	if p == nil {
		return map[string]interface{}{}
	}
	return p
}

// nodeFunc implements one n8n node type. It returns items per output.
type nodeFunc func(ec *execCtx) ([][]Item, error)

// execCtx is everything a node implementation needs.
type execCtx struct {
	ctx     context.Context
	opts    *Options
	node    *models.Node
	params  map[string]interface{}
	version float64
	input   NodeInput
	data    *execData
	rt      *Runtime
	// extra is merged into the output envelope (e.g. "__response").
	extra map[string]interface{}
}

// items returns the main input items.
func (ec *execCtx) items() []Item { return ec.input.Main() }

// runtime returns the JS runtime bound to the main input.
func (ec *execCtx) runtime() (*Runtime, error) {
	if ec.rt != nil {
		return ec.rt, nil
	}
	rt, err := newRuntime(ec.data, ec.node, ec.input, ec.items())
	if err != nil {
		return nil, err
	}
	rt.ctx = ec.ctx
	rt.ec = ec
	ec.rt = rt
	return rt, nil
}

// raw returns the unresolved parameter at a dotted path.
func (ec *execCtx) raw(path string) (interface{}, bool) {
	var cur interface{} = ec.params
	for _, p := range strings.Split(path, ".") {
		switch t := cur.(type) {
		case map[string]interface{}:
			v, ok := t[p]
			if !ok {
				return nil, false
			}
			cur = v
		case []interface{}:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// param resolves a parameter for item i (expressions evaluated), returning
// def when the parameter is absent.
func (ec *execCtx) param(i int, path string, def interface{}) (interface{}, error) {
	v, ok := ec.raw(path)
	if !ok {
		return def, nil
	}
	if !containsExpr(v) {
		return v, nil
	}
	rt, err := ec.runtime()
	if err != nil {
		return nil, err
	}
	rt.setItem(i)
	out, err := rt.resolve(v)
	if err != nil {
		return nil, fmt.Errorf("parameter %q: %w", path, err)
	}
	return out, nil
}

func (ec *execCtx) str(i int, path, def string) (string, error) {
	v, err := ec.param(i, path, def)
	if err != nil {
		return "", err
	}
	return toString(v), nil
}

func (ec *execCtx) num(i int, path string, def float64) (float64, error) {
	v, err := ec.param(i, path, def)
	if err != nil {
		return 0, err
	}
	if f, ok := toFloat(v); ok {
		return f, nil
	}
	return def, nil
}

func (ec *execCtx) boolean(i int, path string, def bool) (bool, error) {
	v, err := ec.param(i, path, def)
	if err != nil {
		return false, err
	}
	return toBool(v), nil
}

// containsExpr reports whether a raw parameter contains any expression.
func containsExpr(v interface{}) bool {
	switch t := v.(type) {
	case string:
		return strings.HasPrefix(t, "=")
	case map[string]interface{}:
		for _, x := range t {
			if containsExpr(x) {
				return true
			}
		}
	case []interface{}:
		for _, x := range t {
			if containsExpr(x) {
				return true
			}
		}
	}
	return false
}

// executor adapts a nodeFunc to engine.NodeExecutor.
type executor struct {
	typ  string
	fn   nodeFunc
	opts *Options
}

var _ engine.InputBuilder = (*executor)(nil)

func (x *executor) BuildInput(inputs []engine.PortInput) (string, error) { return buildInput(inputs) }

func (x *executor) Validate(config map[string]interface{}) error {
	if config == nil {
		return fmt.Errorf("%s: missing config", x.typ)
	}
	return nil
}

func (x *executor) Execute(ctx context.Context, config map[string]interface{}, input string) (string, error) {
	data, err := loadExecData(ctx, x.opts)
	if err != nil {
		return "", err
	}
	n := nodeFromConfig(data, config)
	ec := &execCtx{
		ctx:     ctx,
		opts:    x.opts,
		node:    n,
		params:  cloneParams(nodeParams(n)),
		version: toFloatDefault(config[cfgVersion], 1),
		input:   parseNodeInput(input),
		data:    data,
	}
	if ec.input.Raw != "" || len(ec.input.Inputs) == 0 {
		// Start node: only the selected trigger runs (like n8n); other
		// entry nodes emit nothing so their branches are skipped.
		selected, payload := selectTrigger(data.wf, ec.input.Raw)
		if config[cfgEntry] == true && selected != "" && selected != n.ID && selected != n.Name {
			return encodeOutput(nil, nil)
		}
		ec.input = NodeInput{Inputs: [][]Item{triggerItems(payload)}}
	}
	settings, _ := config[cfgSettings].(map[string]interface{})
	if toBool(settings["executeOnce"]) && len(ec.input.Inputs) > 0 && len(ec.input.Inputs[0]) > 1 {
		ec.input.Inputs[0] = ec.input.Inputs[0][:1]
	}

	var outputs [][]Item
	if mode := onErrorMode(settings); mode != "stopWorkflow" && perItemErrorTypes[short(x.typ)] && len(ec.items()) > 1 {
		// Like n8n: one failing item must not discard the others.
		outputs, err = x.runPerItem(ec, mode)
		if err == nil {
			return encodeOutput(outputs, ec.extra)
		}
	} else {
		outputs, err = x.fn(ec)
	}
	if errors.Is(err, errWaitSignal) {
		return "", errWaitSignal // the engine marks the step WAITING
	}
	var timer *engine.WaitUntilError
	if errors.As(err, &timer) {
		return "", timer
	}
	if err != nil && ctx.Err() != nil {
		return "", err // shutdown or lease loss: never turn this into data
	}
	if err != nil {
		switch onErrorMode(settings) {
		case "continueRegularOutput":
			outputs = [][]Item{{{JSON: map[string]interface{}{"error": err.Error()}, PairedItem: paired(0)}}}
		case "continueErrorOutput":
			n := numOutputs(x.typ, ec)
			outputs = make([][]Item, n+1)
			for i, it := range ec.items() {
				j := cloneJSON(it.JSON)
				j["error"] = map[string]interface{}{"message": err.Error()}
				outputs[n] = append(outputs[n], Item{JSON: j, PairedItem: paired(i)})
			}
			if len(outputs[n]) == 0 {
				outputs[n] = []Item{{JSON: map[string]interface{}{"error": map[string]interface{}{"message": err.Error()}}}}
			}
		default:
			return "", err
		}
	}
	if toBool(settings["alwaysOutputData"]) && allEmpty(outputs) {
		if len(outputs) == 0 {
			outputs = [][]Item{nil}
		}
		outputs[0] = []Item{{JSON: map[string]interface{}{}}}
	}
	return encodeOutput(outputs, ec.extra)
}

func onErrorMode(settings map[string]interface{}) string {
	if s, ok := settings["onError"].(string); ok && s != "" {
		return s
	}
	if toBool(settings["continueOnFail"]) {
		return "continueRegularOutput"
	}
	return "stopWorkflow"
}

// numOutputs is the number of regular outputs, used to place the error
// output after them.
func numOutputs(typ string, ec *execCtx) int {
	switch short(typ) {
	case "if", "filter":
		return 2
	case "guardrails":
		if op, _ := ec.str(0, "operation", "classify"); op == "classify" {
			return 2
		}
		return 1
	case "switch":
		if rules, ok := ec.raw("rules.values"); ok {
			if arr, ok := rules.([]interface{}); ok {
				n := len(arr)
				if fb, _ := ec.raw("options.fallbackOutput"); fb == "extra" {
					n++
				}
				return n
			}
		}
		return 4
	}
	return 1
}

func allEmpty(outputs [][]Item) bool {
	for _, o := range outputs {
		if len(o) > 0 {
			return false
		}
	}
	return true
}

// TriggerKey selects which trigger starts an execution:
// {"__trigger": "<node name or id>", "items": [...]}.
const TriggerKey = "__trigger"

// selectTrigger returns the trigger chosen for this execution (by the input
// or the workflow default) and the payload for its items.
func selectTrigger(wf *models.Workflow, raw string) (string, string) {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) == nil {
		if t, ok := m[TriggerKey]; ok {
			var name string
			_ = json.Unmarshal(t, &name)
			if items, ok := m["items"]; ok {
				return name, string(items)
			}
			delete(m, TriggerKey)
			b, _ := json.Marshal(m)
			return name, string(b)
		}
	}
	return toString(n8nMeta(wf)["defaultTrigger"]), raw
}

func nodeFromConfig(d *execData, config map[string]interface{}) *models.Node {
	id, _ := config[cfgID].(string)
	if n, ok := d.byID[id]; ok {
		return n
	}
	name, _ := config[cfgName].(string)
	if n, ok := d.byName[name]; ok {
		return n
	}
	return &models.Node{ID: id, Name: name, Config: config}
}

// triggerItems turns an execution input into items: an array of objects
// becomes one item each, an object becomes one item, anything else is
// wrapped as {data: value}. Empty input yields one empty item.
func triggerItems(raw string) []Item {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return []Item{{JSON: map[string]interface{}{}}}
	}
	items, err := decodeItems(json.RawMessage(raw))
	if err != nil || len(items) == 0 {
		return []Item{{JSON: map[string]interface{}{}}}
	}
	for i := range items {
		if items[i].JSON == nil {
			items[i].JSON = map[string]interface{}{}
		}
	}
	return items
}

func short(typ string) string {
	if i := strings.LastIndex(typ, "."); i >= 0 {
		return typ[i+1:]
	}
	return typ
}

// ---------------------------------------------------------------- converters

func toString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case json.Number:
		return t.String()
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func toFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	}
	return 0, false
}

func toFloatDefault(v interface{}, d float64) float64 {
	if f, ok := toFloat(v); ok {
		return f
	}
	return d
}

func toBool(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "1" || s == "yes" || s == "on"
	case float64:
		return t != 0
	}
	return false
}

// cloneParams copies the top level of a node's parameters so node
// implementations can adjust them without mutating the shared definition
// (loop bodies re-run the same node).
func cloneParams(p map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

// perItemErrorTypes are nodes that process items independently, so with
// "continue on error" each item can succeed or fail on its own.
var perItemErrorTypes = map[string]bool{
	"httpRequest": true, "set": true, "dataTable": true, "telegram": true, "gmail": true, "googleDrive": true, "googleCalendar": true,
	"googleDocs": true, "slack": true, "discord": true, "whatsApp": true, "notion": true, "airtable": true,
	"wordpress": true, "twitter": true, "linkedIn": true, "microsoftOutlook": true, "facebookGraphApi": true,
	"supabase": true, "redis": true, "postgres": true, "mySql": true, "openAi": true, "agent": true,
	"chainLlm": true, "informationExtractor": true, "emailSend": true, "extractFromFile": true,
	"markdown": true, "xml": true, "editImage": true, "dateTime": true, "rssFeedRead": true,
}

// runPerItem executes the node once per input item, turning failures into
// error items (regular output) or error-output items.
func (x *executor) runPerItem(ec *execCtx, mode string) ([][]Item, error) {
	items := ec.items()
	var outputs [][]Item
	errPort := -1
	if mode == "continueErrorOutput" {
		errPort = numOutputs(x.typ, ec)
	}
	add := func(port int, it Item) {
		for len(outputs) <= port {
			outputs = append(outputs, nil)
		}
		outputs[port] = append(outputs[port], it)
	}
	for i, it := range items {
		sub := *ec
		sub.rt = nil
		sub.extra = nil
		sub.params = cloneParams(ec.params)
		src, off, _ := locate(ec.input, 0, i)
		src.Count, src.Offset = 1, off
		sub.input = NodeInput{Inputs: [][]Item{{it}}, Sources: [][]Source{{src}}}
		outs, err := x.fn(&sub)
		if err != nil {
			if errors.Is(err, errWaitSignal) || ec.ctx.Err() != nil {
				return nil, err
			}
			var timer *engine.WaitUntilError
			if errors.As(err, &timer) {
				return nil, err
			}
			if errPort >= 0 {
				j := cloneJSON(it.JSON)
				j["error"] = map[string]interface{}{"message": err.Error()}
				add(errPort, Item{JSON: j, PairedItem: paired(i)})
			} else {
				add(0, Item{JSON: map[string]interface{}{"error": err.Error()}, PairedItem: paired(i)})
			}
			continue
		}
		for port, list := range outs {
			for _, r := range list {
				r.PairedItem = paired(i)
				add(port, r)
			}
		}
		if sub.extra != nil {
			ec.extra = sub.extra
		}
	}
	return outputs, nil
}
