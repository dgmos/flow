package n8n

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/models"
)

// HandlerOptions configures the webhook/chat HTTP handler.
type HandlerOptions struct {
	// Namespace whose workflows are served.
	Namespace string
	// BaseURL is the public URL the handler is mounted at, exposed to
	// workflows as $json.webhookUrl. Optional.
	BaseURL string
	// ResponseTimeout bounds how long synchronous responses (lastNode,
	// responseNode, chat) wait for the workflow (default 2 minutes).
	ResponseTimeout time.Duration
	// MaxBodyBytes caps request bodies (default 16 MiB).
	MaxBodyBytes int64
}

// Handler serves n8n-style entry points for imported workflows:
//
//	{ANY}  /webhook/{path}            Webhook trigger (and Telegram/WhatsApp-style webhook triggers by webhookId)
//	POST   /chat/{workflowID}         Chat trigger: {"sessionId": "...", "chatInput": "..."}
//	POST   /form/{workflowID}         Form trigger: form fields as JSON or form-encoded
type Handler struct {
	eng   *engine.Engine
	opts  HandlerOptions
	n8n   *Options
	mu    sync.Mutex
	index map[string]routeTarget
	built time.Time
	// sessions are open MCP SSE streams.
	sessions mcpSessions
}

type routeTarget struct {
	workflowID string
	node       *models.Node
	kind       string
}

// NewHandler returns the HTTP handler for a namespace.
func NewHandler(eng *engine.Engine, n8nOpts *Options, opts HandlerOptions) *Handler {
	if opts.ResponseTimeout <= 0 {
		opts.ResponseTimeout = 2 * time.Minute
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 16 << 20
	}
	if n8nOpts == nil {
		n8nOpts = &Options{}
	}
	return &Handler{eng: eng, opts: opts, n8n: n8nOpts}
}

// webhook-style triggers whose request body is the item (no envelope).
var rawBodyTriggers = map[string]bool{
	"n8n-nodes-base.telegramTrigger": true,
	"n8n-nodes-base.whatsAppTrigger": true,
	"n8n-nodes-base.twilioTrigger":   true,
}

func (h *Handler) routes(ctx context.Context, force bool) (map[string]routeTarget, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !force && h.index != nil && time.Since(h.built) < 5*time.Second {
		return h.index, nil
	}
	wfs, err := h.eng.WorkflowDefinitions(ctx, h.opts.Namespace)
	if err != nil {
		return nil, err
	}
	sort.Slice(wfs, func(i, j int) bool { return wfs[i].ID < wfs[j].ID })
	idx := map[string]routeTarget{}
	for _, wf := range wfs {
		for i := range wf.Nodes {
			n := &wf.Nodes[i]
			p := nodeParams(n)
			switch {
			case n.Type == "n8n-nodes-base.webhook":
				path := strings.Trim(toString(p["path"]), "/")
				if path == "" {
					path = toString(n.Config["__webhookId"])
				}
				methods := []string{strings.ToUpper(toString(p["httpMethod"]))}
				if methods[0] == "" {
					methods[0] = "GET"
				}
				if toBool(p["multipleMethods"]) {
					methods = nil
					for _, m := range asList(p["httpMethod"]) {
						methods = append(methods, strings.ToUpper(toString(m)))
					}
				}
				for _, m := range methods {
					if _, taken := idx["webhook "+m+" "+path]; !taken {
						idx["webhook "+m+" "+path] = routeTarget{wf.ID, n, "webhook"}
					}
				}
			case serviceHooks[n.Type].handle != nil:
				id := serviceHookPath(n)
				for _, m := range serviceHooks[n.Type].allowed() {
					if _, taken := idx["webhook "+m+" "+id]; !taken {
						idx["webhook "+m+" "+id] = routeTarget{wf.ID, n, "service"}
					}
				}
			case rawBodyTriggers[n.Type]:
				id := toString(n.Config["__webhookId"])
				if id == "" {
					id = n.ID
				}
				idx["webhook POST "+id] = routeTarget{wf.ID, n, "raw"}
			case n.Type == "@n8n/n8n-nodes-langchain.chatTrigger":
				idx["chat "+wf.ID] = routeTarget{wf.ID, n, "chat"}
			case n.Type == "@n8n/n8n-nodes-langchain.mcpTrigger":
				p := nodeParams(n)
				path := strings.Trim(toString(p["path"]), "/")
				if path == "" {
					path = toString(n.Config["__webhookId"])
				}
				if path == "" {
					path = n.ID
				}
				if _, taken := idx["mcp "+path]; !taken {
					idx["mcp "+path] = routeTarget{wf.ID, n, "mcp"}
				}
			case n.Type == "n8n-nodes-base.formTrigger":
				idx["form "+wf.ID] = routeTarget{wf.ID, n, "form"}
			}
		}
	}
	h.index, h.built = idx, time.Now()
	return idx, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	kind, rest, _ := strings.Cut(path, "/")
	var key string
	switch kind {
	case "webhook", "webhook-test":
		key = "webhook " + r.Method + " " + rest
	case "chat":
		key = "chat " + rest
	case "form":
		key = "form " + rest
	case "resume":
		h.handleResume(w, r, rest)
		return
	case "mcp":
		h.serveMCP(w, r, rest)
		return
	default:
		httpError(w, http.StatusNotFound, "unknown endpoint")
		return
	}
	routes, err := h.routes(r.Context(), false)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	target, ok := routes[key]
	if !ok {
		// Newly registered workflows: refresh the index once.
		if routes, err = h.routes(r.Context(), true); err == nil {
			target, ok = routes[key]
		}
	}
	if !ok {
		httpError(w, http.StatusNotFound, fmt.Sprintf("The requested webhook %q is not registered.", rest))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, h.opts.MaxBodyBytes+1))
	if err != nil || int64(len(body)) > h.opts.MaxBodyBytes {
		httpError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	if target.kind == "service" {
		h.serveServiceHook(w, r, body, target)
		return
	}
	if err := h.authorize(r, target); err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Webhook"`)
		httpError(w, http.StatusUnauthorized, "Authorization data is wrong!")
		return
	}
	item := h.buildItem(r, body, target)
	items := []interface{}{item}
	if list, ok := item.([]interface{}); ok && target.kind == "raw" {
		items = list // e.g. Twilio Event Streams post an array of events
	}
	input, _ := json.Marshal(map[string]interface{}{TriggerKey: target.node.Name, "items": items})
	execID, err := h.eng.StartWorkflow(r.Context(), h.opts.Namespace, target.workflowID, string(input))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "could not start the workflow")
		return
	}
	h.respond(w, r, target, execID)
}

func (h *Handler) authorize(r *http.Request, t routeTarget) error {
	p := nodeParams(t.node)
	auth := toString(p["authentication"])
	if auth == "" || auth == "none" {
		return nil
	}
	credType := map[string]string{"basicAuth": "httpBasicAuth", "headerAuth": "httpHeaderAuth", "bearerAuth": "httpBearerAuth"}[auth]
	if credType == "" {
		return fmt.Errorf("unsupported webhook authentication %q", auth)
	}
	ec := &execCtx{ctx: r.Context(), opts: h.n8n, node: t.node, data: &execData{wf: &models.Workflow{ID: t.workflowID}}}
	c, err := ec.credential(credType)
	if err != nil {
		return err
	}
	eq := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	switch credType {
	case "httpBasicAuth":
		u, pw, ok := r.BasicAuth()
		if credString(c, "user") == "" && credString(c, "password") == "" {
			return errors.New("webhook credential is empty")
		}
		if !ok || !eq(u, credString(c, "user")) || !eq(pw, credString(c, "password")) {
			return errors.New("bad credentials")
		}
	case "httpHeaderAuth":
		if !eq(r.Header.Get(credString(c, "name")), credString(c, "value")) || credString(c, "value") == "" {
			return errors.New("bad credentials")
		}
	case "httpBearerAuth":
		tok := credString(c, "token")
		if tok == "" || !eq(r.Header.Get("Authorization"), "Bearer "+tok) {
			return errors.New("bad credentials")
		}
	}
	return nil
}

func (h *Handler) buildItem(r *http.Request, body []byte, t routeTarget) interface{} {
	parsed := parseBody(r, body)
	switch t.kind {
	case "raw":
		return parsed
	case "chat":
		m, _ := parsed.(map[string]interface{})
		if m == nil {
			m = map[string]interface{}{"chatInput": string(body)}
		}
		if _, ok := m["action"]; !ok {
			m["action"] = "sendMessage"
		}
		if _, ok := m["sessionId"]; !ok {
			m["sessionId"] = r.Header.Get("X-Session-Id")
		}
		return m
	case "form":
		m, _ := parsed.(map[string]interface{})
		if m == nil {
			m = map[string]interface{}{}
		}
		m["submittedAt"] = time.Now().Format(time.RFC3339)
		m["formMode"] = "production"
		return m
	}
	headers := map[string]interface{}{}
	for k := range r.Header {
		headers[strings.ToLower(k)] = r.Header.Get(k)
	}
	query := map[string]interface{}{}
	for k, v := range r.URL.Query() {
		if len(v) == 1 {
			query[k] = v[0]
		} else {
			query[k] = v
		}
	}
	return map[string]interface{}{
		"headers":       headers,
		"params":        map[string]interface{}{},
		"query":         query,
		"body":          parsed,
		"webhookUrl":    strings.TrimRight(h.opts.BaseURL, "/") + r.URL.Path,
		"executionMode": "production",
	}
}

func parseBody(r *http.Request, body []byte) interface{} {
	if len(body) == 0 {
		return map[string]interface{}{}
	}
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		vals, err := url.ParseQuery(string(body))
		if err == nil {
			m := map[string]interface{}{}
			for k, v := range vals {
				if len(v) == 1 {
					m[k] = v[0]
				} else {
					m[k] = v
				}
			}
			return m
		}
	}
	var v interface{}
	if json.Unmarshal(body, &v) == nil {
		return v
	}
	return string(body)
}

// respond implements the trigger's response mode.
func (h *Handler) respond(w http.ResponseWriter, r *http.Request, t routeTarget, execID string) {
	p := nodeParams(t.node)
	mode := toString(p["responseMode"])
	opts, _ := p["options"].(map[string]interface{})
	if t.kind == "chat" || t.kind == "form" {
		mode = toString(opts["responseMode"])
		if mode == "" || mode == "responseNodes" {
			mode = "lastNode"
		}
	}
	if t.kind == "raw" {
		mode = "onReceived"
	}
	if t.kind == "form" {
		h.formFlow(w, r, execID)
		return
	}
	if mode == "" {
		mode = "onReceived"
	}
	if mode == "onReceived" {
		code := validStatus(int(toFloatDefault(opts["responseCode"], 200)))
		if toString(opts["responseData"]) == "noData" {
			w.WriteHeader(code)
			return
		}
		writeJSON(w, code, map[string]interface{}{"message": "Workflow was started"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.opts.ResponseTimeout)
	defer cancel()
	wait := 25 * time.Millisecond
	for {
		exec, wf, steps, err := h.eng.ExecutionSnapshot(ctx, h.opts.Namespace, execID)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if mode == "responseNode" {
			for _, s := range steps {
				if s.Status != models.TaskCompleted {
					continue
				}
				var env struct {
					Resp map[string]interface{} `json:"__response"`
				}
				if json.Unmarshal([]byte(s.Output), &env) == nil && env.Resp != nil {
					writeResponseNode(w, env.Resp)
					return
				}
			}
		}
		if exec.Status != models.TaskRunning {
			if exec.Status == models.TaskFailed {
				httpError(w, http.StatusInternalServerError, "Error in workflow: "+redactSecrets(exec.ErrorMessage))
				return
			}
			if mode == "responseNode" {
				httpError(w, http.StatusInternalServerError, "No Respond to Webhook node was executed")
				return
			}
			items := lastNodeItems(wf, steps)
			if t.kind == "chat" {
				out := map[string]interface{}{"output": ""}
				if msgs := chatMessages(steps); len(msgs) > 0 {
					// Respond to Chat nodes: each message, and all of them as output.
					parts := make([]string, len(msgs))
					for k, m := range msgs {
						parts[k] = toString(m)
					}
					out["output"], out["messages"] = strings.Join(parts, "\n\n"), msgs
					writeJSON(w, http.StatusOK, out)
					return
				}
				if len(items) > 0 {
					for _, k := range []string{"output", "text", "response"} {
						if v, ok := items[0].JSON[k]; ok {
							out["output"] = v
							break
						}
					}
				}
				writeJSON(w, http.StatusOK, out)
				return
			}
			switch toString(opts["responseData"]) {
			case "allEntries":
				list := make([]interface{}, len(items))
				for i, it := range items {
					list[i] = it.JSON
				}
				writeJSON(w, http.StatusOK, list)
			case "noData":
				w.WriteHeader(http.StatusOK)
			default:
				var first interface{} = map[string]interface{}{}
				if len(items) > 0 {
					first = items[0].JSON
				}
				writeJSON(w, http.StatusOK, first)
			}
			return
		}
		select {
		case <-ctx.Done():
			httpError(w, http.StatusGatewayTimeout, "workflow did not respond in time")
			return
		case <-time.After(wait):
			if wait < time.Second {
				wait *= 2
			}
		}
	}
}

// chatMessages collects the messages of Respond to Chat nodes in the order
// their steps finished.
func chatMessages(steps []*models.ExecutionStep) []interface{} {
	var done []*models.ExecutionStep
	for _, s := range steps {
		if s.Status == models.TaskCompleted && strings.Contains(s.Output, chatMessagesKey) {
			done = append(done, s)
		}
	}
	sort.SliceStable(done, func(a, b int) bool { return finishedLater(done[b], done[a]) })
	var out []interface{}
	for _, s := range done {
		var env map[string]interface{}
		if json.Unmarshal([]byte(s.Output), &env) == nil {
			out = append(out, asList(env[chatMessagesKey])...)
		}
	}
	return out
}

// lastNodeItems returns the output of the node that finished last with data.
func lastNodeItems(wf *models.Workflow, steps []*models.ExecutionStep) []Item {
	var best *models.ExecutionStep
	for _, s := range steps {
		if s.Status != models.TaskCompleted {
			continue
		}
		if len(OutputItems(s.Output)) == 0 {
			continue
		}
		if best == nil || finishedLater(s, best) {
			best = s
		}
	}
	if best == nil {
		return nil
	}
	return OutputItems(best.Output)
}

func finishedLater(a, b *models.ExecutionStep) bool {
	at, bt := a.UpdatedAt, b.UpdatedAt
	if a.FinishedAt != nil {
		at = *a.FinishedAt
	}
	if b.FinishedAt != nil {
		bt = *b.FinishedAt
	}
	return at.After(bt)
}

// blockedResponseHeaders may not be set by (untrusted) workflows: they
// would affect the host's origin (cookies, HSTS, CORS credentials).
var blockedResponseHeaders = map[string]bool{
	"Set-Cookie": true, "Set-Cookie2": true, "Clear-Site-Data": true, "Strict-Transport-Security": true,
	"Public-Key-Pins": true, "Access-Control-Allow-Credentials": true, "Content-Length": true, "Transfer-Encoding": true, "Connection": true,
}

func validStatus(code int) int {
	if code < 100 || code > 599 {
		return http.StatusOK
	}
	return code
}

func writeResponseNode(w http.ResponseWriter, resp map[string]interface{}) {
	if hs, ok := resp["headers"].(map[string]interface{}); ok {
		for k, v := range hs {
			if blockedResponseHeaders[http.CanonicalHeaderKey(k)] || strings.ContainsAny(k+toString(v), "\r\n") {
				continue
			}
			w.Header().Set(k, toString(v))
		}
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	code := validStatus(int(toFloatDefault(resp["statusCode"], 200)))
	if b64, ok := resp["binary"].(string); ok {
		data, _ := base64.StdEncoding.DecodeString(b64)
		w.WriteHeader(code)
		_, _ = w.Write(data)
		return
	}
	body := resp["body"]
	if toBool(resp["text"]) {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, toString(body))
		return
	}
	if body == nil {
		w.WriteHeader(code)
		return
	}
	writeJSON(w, code, body)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]interface{}{"code": code, "message": msg})
}

// handleResume resumes the waiting step of an execution through a signed
// URL (Wait on webhook, send-and-wait approvals, form pages).
func (h *Handler) handleResume(w http.ResponseWriter, r *http.Request, execID string) {
	nodeID := r.URL.Query().Get("node")
	approved := r.URL.Query().Get("approved")
	if approved != "" && approved != "true" && approved != "false" {
		httpError(w, http.StatusBadRequest, "invalid approval value")
		return
	}
	if !validResumeSignature(h.n8n.ResumeSecret, h.opts.Namespace, execID, signedNode(nodeID, approved), r.URL.Query().Get("sig")) {
		httpError(w, http.StatusForbidden, "invalid or missing resume signature")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, h.opts.MaxBodyBytes+1))
	if err != nil || int64(len(body)) > h.opts.MaxBodyBytes {
		httpError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	exec, wf, steps, err := h.eng.ExecutionSnapshot(r.Context(), h.opts.Namespace, execID)
	if err != nil {
		httpError(w, http.StatusNotFound, "execution not found")
		return
	}
	if exec.Status != models.TaskRunning {
		httpError(w, http.StatusConflict, "execution is no longer running")
		return
	}
	var step *models.ExecutionStep
	for _, s := range steps {
		if s.Status == models.TaskWaiting && (nodeID == "" || s.NodeID == nodeID) {
			step = s
			break
		}
	}
	if step == nil {
		httpError(w, http.StatusConflict, "execution is not waiting for this response (already answered?)")
		return
	}
	var n *models.Node
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == step.NodeID {
			n = &wf.Nodes[i]
		}
	}
	if n == nil {
		httpError(w, http.StatusInternalServerError, "waiting node not found")
		return
	}
	p := nodeParams(n)
	// Execution-level links ($execution.resumeUrl) only resume Wait nodes;
	// approvals and forms always need their node-scoped link.
	if nodeID == "" && n.Type != "n8n-nodes-base.wait" {
		httpError(w, http.StatusForbidden, "this link cannot resume this step")
		return
	}
	var item map[string]interface{}
	kind := "wait"
	switch {
	case toString(p["operation"]) == "sendAndWait" && (toString(p["responseType"]) == "freeText" || toString(p["responseType"]) == "customForm"):
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, responseFormHTML(p, r.URL.String()))
			return
		}
		kind = "approval"
		m, _ := parseBody(r, body).(map[string]interface{})
		if m == nil {
			m = map[string]interface{}{}
		}
		item = map[string]interface{}{"data": m}
	case toString(p["operation"]) == "sendAndWait":
		if approved == "" {
			httpError(w, http.StatusBadRequest, "missing approval value")
			return
		}
		if r.Method != http.MethodPost {
			// Confirmation page: link scanners and prefetchers issue GETs
			// and must not answer on the user's behalf.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			label := "Approve"
			if approved == "false" {
				label = "Decline"
			}
			_, _ = io.WriteString(w, `<!doctype html><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>Confirm</title>`+
				`<form method=post style="font-family:sans-serif;margin:40px auto;max-width:420px"><p>Confirm your response:</p><button type=submit>`+label+`</button></form>`)
			return
		}
		kind = "approval"
		item = map[string]interface{}{"data": map[string]interface{}{"approved": approved == "true"}}
	case isFormPage(n):
		kind = "form"
		m, _ := parseBody(r, body).(map[string]interface{})
		if m == nil {
			m = map[string]interface{}{}
		}
		item = m
	default:
		q := map[string]interface{}{}
		for k, v := range r.URL.Query() {
			if k != "sig" {
				q[k] = v[0]
			}
		}
		headers := map[string]interface{}{}
		for k := range r.Header {
			headers[strings.ToLower(k)] = r.Header.Get(k)
		}
		item = map[string]interface{}{"headers": headers, "params": map[string]interface{}{}, "query": q, "body": parseBody(r, body)}
	}
	out, _ := encodeOutput([][]Item{{{JSON: item, PairedItem: paired(0)}}}, nil)
	if err := h.eng.CompleteStep(r.Context(), step, out); err != nil {
		if errors.Is(err, engine.ErrStepConflict) {
			httpError(w, http.StatusConflict, "this response was already recorded")
			return
		}
		httpError(w, http.StatusInternalServerError, "could not resume the workflow")
		return
	}
	switch kind {
	case "approval":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html><meta charset=utf-8><title>Response recorded</title><p style=\"font-family:sans-serif\">Got it, thanks. Your response has been recorded.</p>")
	case "form":
		h.formFlow(w, r, execID)
	default:
		writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Workflow was resumed"})
	}
}

// formFlow waits for the next form page or the end of the execution and
// describes it as JSON for the host's form UI:
//
//	{"page": {...fields}, "resumeUrl": "..."}   next page to render and POST to resumeUrl
//	{"completion": {"title", "message", ...}}   final screen
func (h *Handler) formFlow(w http.ResponseWriter, r *http.Request, execID string) {
	ctx, cancel := context.WithTimeout(r.Context(), h.opts.ResponseTimeout)
	defer cancel()
	wait := 25 * time.Millisecond
	for {
		exec, wf, steps, err := h.eng.ExecutionSnapshot(ctx, h.opts.Namespace, execID)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, s := range steps {
			if s.Status == models.TaskWaiting {
				for i := range wf.Nodes {
					if wf.Nodes[i].ID == s.NodeID && isFormPage(&wf.Nodes[i]) {
						writeJSON(w, http.StatusOK, map[string]interface{}{"page": formPageDefinition(nodeParams(&wf.Nodes[i])), "resumeUrl": h.n8n.resumeURL(h.opts.Namespace, execID, s.NodeID)})
						return
					}
				}
			}
		}
		if exec.Status != models.TaskRunning {
			if exec.Status == models.TaskFailed {
				httpError(w, http.StatusInternalServerError, "Error in workflow: "+redactSecrets(exec.ErrorMessage))
				return
			}
			comp := map[string]interface{}{"title": "Form submitted", "message": "Your response has been recorded"}
			for _, s := range steps {
				var env struct {
					Comp map[string]interface{} `json:"__formCompletion"`
				}
				if json.Unmarshal([]byte(s.Output), &env) == nil && env.Comp != nil {
					comp = env.Comp
				}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"completion": comp})
			return
		}
		select {
		case <-ctx.Done():
			httpError(w, http.StatusGatewayTimeout, "workflow did not respond in time")
			return
		case <-time.After(wait):
			if wait < time.Second {
				wait *= 2
			}
		}
	}
}

// WebhookKeys lists the HTTP routes a workflow's triggers claim
// ("METHOD path"), so hosts can reject conflicting installs.
func WebhookKeys(wf *models.Workflow) []string {
	var keys []string
	for i := range wf.Nodes {
		n := &wf.Nodes[i]
		p := nodeParams(n)
		switch {
		case n.Type == "n8n-nodes-base.webhook":
			path := strings.Trim(toString(p["path"]), "/")
			if path == "" {
				path = toString(n.Config["__webhookId"])
			}
			methods := []string{strings.ToUpper(toString(p["httpMethod"]))}
			if methods[0] == "" {
				methods[0] = "GET"
			}
			if toBool(p["multipleMethods"]) {
				methods = nil
				for _, m := range asList(p["httpMethod"]) {
					methods = append(methods, strings.ToUpper(toString(m)))
				}
			}
			for _, m := range methods {
				keys = append(keys, m+" "+path)
			}
		case rawBodyTriggers[n.Type]:
			id := toString(n.Config["__webhookId"])
			if id == "" {
				id = n.ID
			}
			keys = append(keys, "POST "+id)
		}
	}
	return keys
}

// isFormPage reports whether a waiting node shows a form page: a Form node
// or a Wait node resumed by a form submission.
func isFormPage(n *models.Node) bool {
	return n.Type == "n8n-nodes-base.form" || n.Type == "n8n-nodes-base.wait" && toString(nodeParams(n)["resume"]) == "form"
}
