package n8n

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"hash"
	"net/http"
	"strings"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/models"
)

// Service webhook triggers (Slack, Stripe, GitHub, Shopify, …): the service
// posts events to /webhook/{webhookId}; each trigger verifies the request,
// answers handshakes, filters events and builds n8n's output items.

// hookRequest is one incoming service webhook call.
type hookRequest struct {
	ctx    context.Context
	r      *http.Request
	body   []byte
	parsed interface{}
	params map[string]interface{}
	// state holds what registration stored (webhook id, signing secret).
	state map[string]interface{}
	cred  func(credType string) (map[string]interface{}, error)
	// ec makes API calls with the node's credentials and outbound policy.
	ec *execCtx
}

// hookResult says how to answer and whether to start an execution.
type hookResult struct {
	run         bool
	items       []interface{}
	status      int
	contentType string
	body        []byte
}

// errHookUnauthorized rejects a request whose signature does not verify.
var errHookUnauthorized = errors.New("webhook signature verification failed")

type serviceHook struct {
	// methods the endpoint accepts (default POST).
	methods []string
	handle  func(q *hookRequest) (hookResult, error)
	// credTypes are the node's credential types, used for verification and
	// registration.
	credTypes []string
}

var serviceHooks = map[string]serviceHook{}

func (q *hookRequest) json() map[string]interface{} {
	m, _ := q.parsed.(map[string]interface{})
	if m == nil {
		m = map[string]interface{}{}
	}
	return m
}

func (q *hookRequest) param(name string, def interface{}) interface{} {
	if v, ok := q.params[name]; ok && v != nil {
		return v
	}
	return def
}

func (q *hookRequest) str(name, def string) string { return toString(q.param(name, def)) }

// events returns the selected event names of a list parameter; "*" (or an
// empty selection when allEmpty) selects everything.
func (q *hookRequest) events(name string, allEmpty bool) func(string) bool {
	var list []string
	switch v := q.param(name, nil).(type) {
	case []interface{}:
		for _, x := range v {
			list = append(list, toString(x))
		}
	case string:
		if v != "" {
			list = []string{v}
		}
	}
	return func(ev string) bool {
		if len(list) == 0 {
			return allEmpty
		}
		for _, x := range list {
			if x == "*" || x == ev {
				return true
			}
		}
		return false
	}
}

// fullItem is n8n's generic {body, headers, query} webhook item.
func (q *hookRequest) fullItem() map[string]interface{} {
	headers := map[string]interface{}{}
	for k := range q.r.Header {
		headers[strings.ToLower(k)] = q.r.Header.Get(k)
	}
	query := map[string]interface{}{}
	for k, v := range q.r.URL.Query() {
		query[k] = v[0]
	}
	return map[string]interface{}{"body": q.parsed, "headers": headers, "query": query}
}

func hmacSum(newHash func() hash.Hash, secret string, msg []byte) []byte {
	m := hmac.New(newHash, []byte(secret))
	m.Write(msg)
	return m.Sum(nil)
}

// ok starts an execution with one item per JSON value.
func ok(items ...interface{}) hookResult {
	wrapped := make([]interface{}, len(items))
	for i, it := range items {
		wrapped[i] = map[string]interface{}{"json": it}
	}
	return hookResult{run: true, items: wrapped}
}

func reply(status int, contentType string, body []byte) hookResult {
	return hookResult{status: status, contentType: contentType, body: body}
}

func jsonReply(v interface{}) hookResult {
	b, _ := json.Marshal(v)
	return hookResult{status: http.StatusOK, contentType: "application/json", body: b}
}

// hookStateKey is where a trigger's registration data is kept (in the
// poll-state store).
func hookStateKey(namespace, workflowID, nodeID string) string {
	return "hook/" + namespace + "/" + workflowID + "/" + nodeID
}

func (h *Handler) serveServiceHook(w http.ResponseWriter, r *http.Request, body []byte, t routeTarget) {
	spec := serviceHooks[t.node.Type]
	ctx := engine.WithExecutionContext(r.Context(), h.opts.Namespace, "")
	ec := &execCtx{ctx: ctx, opts: h.n8n, node: t.node, params: nodeParams(t.node), data: &execData{wf: &models.Workflow{ID: t.workflowID}}}
	q := &hookRequest{ctx: ctx, r: r, body: body, parsed: parseBody(r, body), params: nodeParams(t.node), state: map[string]interface{}{},
		cred: ec.credential, ec: ec}
	if raw, seen, err := ec.pollStore().GetPollState(ctx, hookStateKey(h.opts.Namespace, t.workflowID, t.node.ID)); err == nil && seen {
		_ = json.Unmarshal([]byte(raw), &q.state)
	}
	q.state["__version"] = toFloatDefault(t.node.Config[cfgVersion], 1)
	q.state["__nodeId"] = t.node.ID
	q.state["__url"] = strings.TrimRight(h.opts.BaseURL, "/") + "/webhook/" + serviceHookPath(t.node)
	res, err := spec.handle(q)
	if err != nil {
		if errors.Is(err, errHookUnauthorized) {
			httpError(w, http.StatusUnauthorized, err.Error())
			return
		}
		httpError(w, http.StatusBadRequest, redactSecrets(err.Error()))
		return
	}
	if res.run {
		input, _ := json.Marshal(map[string]interface{}{TriggerKey: t.node.Name, "items": res.items})
		if _, err := h.eng.StartWorkflow(r.Context(), h.opts.Namespace, t.workflowID, string(input)); err != nil {
			httpError(w, http.StatusInternalServerError, "could not start the workflow")
			return
		}
	}
	if res.status == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{"message": "Workflow was started"})
		return
	}
	if res.contentType != "" {
		w.Header().Set("Content-Type", res.contentType)
	}
	w.WriteHeader(res.status)
	_, _ = w.Write(res.body)
}

func serviceHookPath(n *models.Node) string {
	if id := toString(n.Config["__webhookId"]); id != "" {
		return id
	}
	return n.ID
}

func (s serviceHook) allowed() []string {
	if len(s.methods) == 0 {
		return []string{http.MethodPost}
	}
	return s.methods
}
