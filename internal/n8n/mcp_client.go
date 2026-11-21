package n8n

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgmos/flow/internal/node"
)

const mcpProtocolVersion = "2025-03-26"

// mcpTransport is one MCP client connection.
type mcpTransport interface {
	// call sends a JSON-RPC request and returns its result.
	call(ctx context.Context, method string, params interface{}) (json.RawMessage, error)
	notify(ctx context.Context, method string, params interface{}) error
	close()
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  interface{}     `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// mcpConfig describes how to reach an MCP server.
type mcpConfig struct {
	transport string // httpStreamable | sse | stdio
	url       string
	headers   map[string]string
	command   string
	args      []string
	env       []string
	timeout   time.Duration
}

// mcpConnect opens and initializes an MCP session.
func (ec *execCtx) mcpConnect(cfg mcpConfig) (mcpTransport, error) {
	if cfg.timeout <= 0 {
		cfg.timeout = 60 * time.Second
	}
	var t mcpTransport
	var err error
	switch cfg.transport {
	case "stdio":
		if ec.opts == nil || !ec.opts.AllowLocalProcesses {
			return nil, permanentError{"MCP: STDIO servers start local processes, which is disabled (set Options.AllowLocalProcesses)"}
		}
		t, err = startStdioMCP(ec.ctx, cfg)
	case "sse":
		t, err = ec.openSSEMCP(cfg)
	default:
		t = &httpMCP{ec: ec, cfg: cfg}
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ec.ctx, cfg.timeout)
	defer cancel()
	if _, err := t.call(ctx, "initialize", map[string]interface{}{"protocolVersion": mcpProtocolVersion, "capabilities": map[string]interface{}{},
		"clientInfo": map[string]interface{}{"name": "flow-n8n", "version": "1.0.0"}}); err != nil {
		t.close()
		return nil, fmt.Errorf("MCP initialize: %w", err)
	}
	if err := t.notify(ctx, "notifications/initialized", nil); err != nil {
		t.close()
		return nil, err
	}
	return t, nil
}

type mcpTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

func mcpListTools(ctx context.Context, t mcpTransport) ([]mcpTool, error) {
	var all []mcpTool
	cursor := ""
	for page := 0; page < 100; page++ {
		var params interface{}
		if cursor != "" {
			params = map[string]interface{}{"cursor": cursor}
		}
		raw, err := t.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools      []mcpTool `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("MCP tools/list: %w", err)
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return all, nil
}

// mcpResultText renders a tools/call result for an AI agent.
func mcpResultText(raw json.RawMessage) (string, bool) {
	var res struct {
		Content []map[string]interface{} `json:"content"`
		IsError bool                     `json:"isError"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return string(raw), false
	}
	var parts []string
	for _, c := range res.Content {
		if toString(c["type"]) == "text" {
			parts = append(parts, toString(c["text"]))
		} else {
			parts = append(parts, mustJSON(c))
		}
	}
	return strings.Join(parts, "\n"), res.IsError
}

// withMCP runs fn on a fresh session (connections are not kept between
// tool calls, so sessions never outlive a step).
func (ec *execCtx) withMCP(cfg mcpConfig, fn func(ctx context.Context, t mcpTransport) error) error {
	if cfg.timeout <= 0 {
		cfg.timeout = 60 * time.Second
	}
	t, err := ec.mcpConnect(cfg)
	if err != nil {
		return err
	}
	defer t.close()
	ctx, cancel := context.WithTimeout(ec.ctx, cfg.timeout)
	defer cancel()
	return fn(ctx, t)
}

// ---------------------------------------------------------------- Streamable HTTP

type httpMCP struct {
	ec      *execCtx
	cfg     mcpConfig
	session string
	seq     int64
}

func (h *httpMCP) post(ctx context.Context, msg rpcMsg) (json.RawMessage, error) {
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, "POST", h.cfg.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range h.cfg.headers {
		req.Header.Set(k, v)
	}
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}
	resp, err := outboundClient(h.ec.opts.Outbound).Do(req)
	if err != nil {
		return nil, sanitizeErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		h.session = s
	}
	if resp.StatusCode == http.StatusAccepted || msg.ID == nil {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		b, _ := node.ReadLimited(resp.Body, 4096)
		return nil, fmt.Errorf("MCP server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	limit := h.ec.opts.Outbound.MaxResponseBytesOrDefault()
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var out json.RawMessage
		var rerr error
		err := readSSE(io.LimitReader(resp.Body, limit), func(event, data string) bool {
			var m rpcMsg
			if json.Unmarshal([]byte(data), &m) != nil || fmt.Sprint(m.ID) != fmt.Sprint(msg.ID) {
				return true
			}
			if m.Error != nil {
				rerr = m.Error
			}
			out = m.Result
			return false
		})
		if rerr != nil {
			return nil, rerr
		}
		if out == nil && err != nil {
			return nil, err
		}
		return out, nil
	}
	b, err := node.ReadLimited(resp.Body, limit)
	if err != nil {
		return nil, err
	}
	var m rpcMsg
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("MCP: invalid response: %w", err)
	}
	if m.Error != nil {
		return nil, m.Error
	}
	return m.Result, nil
}

func (h *httpMCP) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := atomic.AddInt64(&h.seq, 1)
	return h.post(ctx, rpcMsg{JSONRPC: "2.0", ID: id, Method: method, Params: params})
}

func (h *httpMCP) notify(ctx context.Context, method string, params interface{}) error {
	_, err := h.post(ctx, rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
	return err
}

func (h *httpMCP) close() {
	if h.session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "DELETE", h.cfg.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", h.session)
	for k, v := range h.cfg.headers {
		req.Header.Set(k, v)
	}
	if resp, err := outboundClient(h.ec.opts.Outbound).Do(req); err == nil {
		_ = resp.Body.Close()
	}
}

// readSSE calls fn for each server-sent event until fn returns false or the
// stream ends.
func readSSE(r io.Reader, fn func(event, data string) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	event, data := "", []string{}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 || event != "" {
				if !fn(firstNonEmpty(event, "message"), strings.Join(data, "\n")) {
					return nil
				}
			}
			event, data = "", nil
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[6:])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// ---------------------------------------------------------------- SSE (legacy)

type sseMCP struct {
	ec       *execCtx
	cfg      mcpConfig
	postURL  string
	cancel   context.CancelFunc
	mu       sync.Mutex
	pending  map[string]chan rpcMsg
	seq      int64
	done     chan struct{}
	streamEr error
}

func (ec *execCtx) openSSEMCP(cfg mcpConfig) (*sseMCP, error) {
	ctx, cancel := context.WithCancel(ec.ctx)
	req, err := http.NewRequestWithContext(ctx, "GET", cfg.url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range cfg.headers {
		req.Header.Set(k, v)
	}
	client := *outboundClient(ec.opts.Outbound)
	client.Timeout = 0 // the stream stays open; ctx bounds it
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, sanitizeErr(err)
	}
	if resp.StatusCode >= 400 {
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("MCP SSE endpoint returned %d", resp.StatusCode)
	}
	s := &sseMCP{ec: ec, cfg: cfg, cancel: cancel, pending: map[string]chan rpcMsg{}, done: make(chan struct{})}
	endpoint := make(chan string, 1)
	go func() {
		defer close(s.done)
		defer func() { _ = resp.Body.Close() }()
		err := readSSE(resp.Body, func(event, data string) bool {
			switch event {
			case "endpoint":
				select {
				case endpoint <- strings.TrimSpace(data):
				default:
				}
			case "message":
				var m rpcMsg
				if json.Unmarshal([]byte(data), &m) == nil && m.ID != nil {
					s.mu.Lock()
					ch := s.pending[fmt.Sprint(m.ID)]
					s.mu.Unlock()
					if ch != nil {
						ch <- m
					}
				}
			}
			return true
		})
		s.mu.Lock()
		s.streamEr = err
		s.mu.Unlock()
	}()
	select {
	case e := <-endpoint:
		base, _ := url.Parse(cfg.url)
		ref, err := url.Parse(e)
		if err != nil || base == nil {
			s.close()
			return nil, errors.New("MCP SSE: invalid endpoint event")
		}
		post := base.ResolveReference(ref)
		if post.Host != base.Host {
			s.close()
			return nil, errors.New("MCP SSE: the server announced a messages endpoint on another host")
		}
		s.postURL = post.String()
	case <-s.done:
		cancel()
		return nil, errors.New("MCP SSE: stream closed before the endpoint event")
	case <-time.After(cfg.timeout):
		s.close()
		return nil, errors.New("MCP SSE: no endpoint event")
	}
	return s, nil
}

func (s *sseMCP) send(ctx context.Context, msg rpcMsg) error {
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, "POST", s.postURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.cfg.headers {
		req.Header.Set(k, v)
	}
	resp, err := outboundClient(s.ec.opts.Outbound).Do(req)
	if err != nil {
		return sanitizeErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		b, _ := node.ReadLimited(resp.Body, 4096)
		return fmt.Errorf("MCP server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (s *sseMCP) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := atomic.AddInt64(&s.seq, 1)
	key := fmt.Sprint(id)
	ch := make(chan rpcMsg, 1)
	s.mu.Lock()
	s.pending[key] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, key); s.mu.Unlock() }()
	if err := s.send(ctx, rpcMsg{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-s.done:
		return nil, errors.New("MCP SSE: stream closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *sseMCP) notify(ctx context.Context, method string, params interface{}) error {
	return s.send(ctx, rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *sseMCP) close() { s.cancel() }

// ---------------------------------------------------------------- STDIO

type stdioMCP struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	pending map[string]chan rpcMsg
	seq     int64
	done    chan struct{}
}

func startStdioMCP(ctx context.Context, cfg mcpConfig) (*stdioMCP, error) {
	if cfg.command == "" {
		return nil, permanentError{"MCP: command is empty"}
	}
	cmd := exec.CommandContext(ctx, cfg.command, cfg.args...)
	cmd.Env = append(os.Environ(), cfg.env...)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("MCP: start %s: %w", cfg.command, err)
	}
	s := &stdioMCP{cmd: cmd, stdin: in, pending: map[string]chan rpcMsg{}, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		for sc.Scan() {
			var m rpcMsg
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == nil {
				continue
			}
			s.mu.Lock()
			ch := s.pending[fmt.Sprint(m.ID)]
			s.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}()
	return s, nil
}

func (s *stdioMCP) write(msg rpcMsg) error {
	b, _ := json.Marshal(msg)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.stdin.Write(append(b, '\n'))
	return err
}

func (s *stdioMCP) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := atomic.AddInt64(&s.seq, 1)
	key := fmt.Sprint(id)
	ch := make(chan rpcMsg, 1)
	s.mu.Lock()
	s.pending[key] = ch
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.pending, key); s.mu.Unlock() }()
	if err := s.write(rpcMsg{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		return nil, err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-s.done:
		return nil, errors.New("MCP: server process exited")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *stdioMCP) notify(_ context.Context, method string, params interface{}) error {
	return s.write(rpcMsg{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *stdioMCP) close() {
	_ = s.stdin.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
}

// ---------------------------------------------------------------- node adapters

// mcpToolsFromSubNode builds AI tools for an MCP Client Tool sub-node
// (official node; and the community node used as a tool).
func (ec *execCtx) mcpClientTools(s subNode, i int) ([]aiTool, error) {
	p, err := ec.resolveSub(s, i)
	if err != nil {
		return nil, err
	}
	cfg, err := ec.mcpConfigOfficial(s, p)
	if err != nil {
		return nil, err
	}
	var listed []mcpTool
	if err := ec.withMCP(cfg, func(ctx context.Context, t mcpTransport) error {
		var err error
		listed, err = mcpListTools(ctx, t)
		return err
	}); err != nil {
		return nil, fmt.Errorf("%s: %w", s.name(), err)
	}
	selected := map[string]bool{}
	for _, n := range asList(p["includeTools"]) {
		selected[toString(n)] = true
	}
	excluded := map[string]bool{}
	for _, n := range asList(p["excludeTools"]) {
		excluded[toString(n)] = true
	}
	include := toString(p["include"])
	var tools []aiTool
	for _, tl := range listed {
		if (include == "selected" && !selected[tl.Name]) || (include == "except" && excluded[tl.Name]) {
			continue
		}
		tl := tl
		schema := tl.InputSchema
		if schema == nil {
			schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}
		tools = append(tools, aiTool{def: toolDef{Name: toolName(tl.Name), Description: tl.Description, Schema: schema},
			run: func(ctx context.Context, args map[string]interface{}) (string, error) {
				var text string
				err := ec.withMCP(cfg, func(ctx context.Context, t mcpTransport) error {
					raw, err := t.call(ctx, "tools/call", map[string]interface{}{"name": tl.Name, "arguments": args})
					if err != nil {
						return err
					}
					var isErr bool
					text, isErr = mcpResultText(raw)
					if isErr {
						text = "Error: " + text
					}
					return nil
				})
				return text, err
			}})
	}
	return tools, nil
}

// mcpConfigOfficial reads the official MCP Client Tool's connection.
func (ec *execCtx) mcpConfigOfficial(s subNode, p map[string]interface{}) (mcpConfig, error) {
	ver := toFloatDefault(s.config[cfgVersion], 1)
	cfg := mcpConfig{headers: map[string]string{}, transport: "sse", url: toString(p["sseEndpoint"])}
	if ver >= 1.1 {
		cfg.url = firstNonEmpty(toString(p["endpointUrl"]), cfg.url)
		def := "sse"
		if ver >= 1.2 {
			def = "httpStreamable"
		}
		cfg.transport = firstNonEmpty(toString(p["serverTransport"]), def)
	}
	if cfg.url == "" {
		return cfg, permanentError{s.name() + ": MCP endpoint URL is empty"}
	}
	if t := toFloatDefault(asMap(p["options"])["timeout"], 0); t > 0 {
		cfg.timeout = time.Duration(t) * time.Millisecond
	}
	switch toString(p["authentication"]) {
	case "bearerAuth":
		c, err := ec.subCredential(s, "httpBearerAuth")
		if err != nil {
			return cfg, err
		}
		cfg.headers["Authorization"] = "Bearer " + credString(c, "token")
	case "headerAuth":
		c, err := ec.subCredential(s, "httpHeaderAuth")
		if err != nil {
			return cfg, err
		}
		cfg.headers[credString(c, "name")] = credString(c, "value")
	case "multipleHeadersAuth":
		c, err := ec.subCredential(s, "httpMultipleHeadersAuth")
		if err != nil {
			return cfg, err
		}
		for _, h := range asList(asMap(c["headers"])["values"]) {
			hm := asMap(h)
			cfg.headers[toString(hm["name"])] = toString(hm["value"])
		}
	}
	return cfg, nil
}

// mcpCommunityNode is the community "MCP Client" node (n8n-nodes-mcp):
// list/execute tools, resources and prompts over STDIO, SSE or HTTP.
func mcpCommunityNode(ec *execCtx) ([][]Item, error) {
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		cfg, err := ec.mcpConfigCommunity(i)
		if err != nil {
			return nil, err
		}
		op, _ := ec.str(i, "operation", "listTools")
		var out []Item
		err = ec.withMCP(cfg, func(ctx context.Context, t mcpTransport) error {
			switch op {
			case "listTools":
				tools, err := mcpListTools(ctx, t)
				if err != nil {
					return err
				}
				list := make([]interface{}, len(tools))
				for k, tl := range tools {
					list[k] = map[string]interface{}{"name": tl.Name, "description": tl.Description, "schema": tl.InputSchema}
				}
				out = one(map[string]interface{}{"tools": list})
			case "executeTool":
				name, _ := ec.str(i, "toolName", "")
				pv, err := ec.param(i, "toolParameters", "{}")
				if err != nil {
					return err
				}
				args := asMap(parseJSONParam(pv))
				if args == nil {
					args = map[string]interface{}{}
				}
				raw, err := t.call(ctx, "tools/call", map[string]interface{}{"name": name, "arguments": args})
				if err != nil {
					return err
				}
				var res map[string]interface{}
				_ = json.Unmarshal(raw, &res)
				out = one(map[string]interface{}{"result": res})
			case "listResources", "listPrompts", "listResourceTemplates":
				method := map[string]string{"listResources": "resources/list", "listPrompts": "prompts/list", "listResourceTemplates": "resources/templates/list"}[op]
				raw, err := t.call(ctx, method, nil)
				if err != nil {
					return err
				}
				var res map[string]interface{}
				_ = json.Unmarshal(raw, &res)
				out = one(res)
			case "readResource":
				uri, _ := ec.str(i, "resourceUri", "")
				raw, err := t.call(ctx, "resources/read", map[string]interface{}{"uri": uri})
				if err != nil {
					return err
				}
				var res map[string]interface{}
				_ = json.Unmarshal(raw, &res)
				out = one(res)
			case "getPrompt":
				name, _ := ec.str(i, "promptName", "")
				raw, err := t.call(ctx, "prompts/get", map[string]interface{}{"name": name})
				if err != nil {
					return err
				}
				var res map[string]interface{}
				_ = json.Unmarshal(raw, &res)
				out = one(res)
			default:
				return permanentError{fmt.Sprintf("MCP Client: operation %q is not supported", op)}
			}
			return nil
		})
		return out, err
	})
}

func (ec *execCtx) mcpConfigCommunity(i int) (mcpConfig, error) {
	kind, _ := ec.str(i, "connectionType", "cmd")
	cfg := mcpConfig{headers: map[string]string{}}
	parseHeaders := func(s string) {
		for _, line := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' }) {
			if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) != "" {
				cfg.headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			} else if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) != "" {
				cfg.headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	switch kind {
	case "sse":
		c, err := ec.credential("mcpClientSseApi")
		if err != nil {
			return cfg, err
		}
		cfg.transport, cfg.url = "sse", credString(c, "sseUrl")
		parseHeaders(credString(c, "headers"))
	case "http":
		c, err := ec.credential("mcpClientHttpApi")
		if err != nil {
			return cfg, err
		}
		cfg.transport, cfg.url = "httpStreamable", credString(c, "httpStreamUrl")
		parseHeaders(credString(c, "headers"))
	default:
		c, err := ec.credential("mcpClientApi")
		if err != nil {
			return cfg, err
		}
		cfg.transport, cfg.command = "stdio", credString(c, "command")
		cfg.args = strings.Fields(credString(c, "args"))
		for _, e := range strings.FieldsFunc(credString(c, "environments"), func(r rune) bool { return r == ',' || r == '\n' }) {
			if strings.Contains(e, "=") {
				cfg.env = append(cfg.env, strings.TrimSpace(e))
			}
		}
	}
	if cfg.transport != "stdio" && cfg.url == "" {
		return cfg, permanentError{"MCP Client: server URL is empty"}
	}
	return cfg, nil
}

// mcpClientNode is the official MCP Client node: it calls one tool of an
// MCP server per item.
func mcpClientNode(ec *execCtx) ([][]Item, error) {
	s := subNode{typ: ec.node.Type, config: ec.node.Config}
	p := ec.params
	if toString(p["serverTransport"]) == "" {
		p = cloneJSON(p)
		p["serverTransport"] = "httpStreamable"
	}
	cfg, err := ec.mcpConfigOfficial(s, p)
	if err != nil {
		return nil, err
	}
	if toString(p["authentication"]) == "mcpOAuth2Api" {
		c, err := ec.credential("mcpOAuth2Api")
		if err != nil {
			return nil, err
		}
		cfg.headers["Authorization"] = "Bearer " + accessToken(c)
	}
	mode, _ := ec.str(0, "inputMode", "manual")
	var out []Item
	err = ec.withMCP(cfg, func(ctx context.Context, t mcpTransport) error {
		for i := range ec.items() {
			tool, err := ec.rlString(i, "tool")
			if err != nil {
				return err
			}
			var args map[string]interface{}
			if mode == "json" {
				v, _ := ec.param(i, "jsonInput", "{}")
				if args, err = jsonObjectParam(v); err != nil {
					return permanentError{"MCP Client: JSON input: " + err.Error()}
				}
			} else {
				v, _ := ec.param(i, "parameters.value", nil)
				args = asMap(v)
			}
			if args == nil {
				args = map[string]interface{}{}
			}
			raw, err := t.call(ctx, "tools/call", map[string]interface{}{"name": tool, "arguments": args})
			if err != nil {
				return fmt.Errorf("item %d: %w", i, err)
			}
			var res struct {
				Content           []map[string]interface{} `json:"content"`
				StructuredContent map[string]interface{}   `json:"structuredContent"`
				IsError           bool                     `json:"isError"`
			}
			_ = json.Unmarshal(raw, &res)
			if res.IsError && ec.version >= 1.1 {
				msg := fmt.Sprintf("Tool %q returned an error", tool)
				for _, c := range res.Content {
					if c["type"] == "text" {
						msg = toString(c["text"])
						break
					}
				}
				return permanentError{fmt.Sprintf("item %d: %s", i, msg)}
			}
			toBin := true
			if v, ok := asMap(p["options"])["convertToBinary"]; ok {
				toBin = toBool(v)
			}
			it := Item{JSON: map[string]interface{}{}, PairedItem: paired(i)}
			var content []interface{}
			for _, c := range res.Content {
				switch typ := toString(c["type"]); {
				case typ == "text":
					c["text"] = parseJSONParam(c["text"])
					content = append(content, c)
				case toBin && (typ == "image" || typ == "audio"):
					data, err := base64.StdEncoding.DecodeString(toString(c["data"]))
					if err != nil {
						return err
					}
					if it.Binary == nil {
						it.Binary = map[string]interface{}{}
					}
					it.Binary[fmt.Sprintf("data_%d", len(it.Binary))] = binaryData(data, toString(c["mimeType"]), "")
				default:
					content = append(content, c)
				}
			}
			if res.StructuredContent != nil {
				it.JSON["structuredContent"] = res.StructuredContent
			}
			if len(content) > 0 {
				it.JSON["content"] = content
			}
			out = append(out, it)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return [][]Item{out}, nil
}
