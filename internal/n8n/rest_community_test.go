package n8n

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// Test helpers of the community-node connectors: a fake API that records
// all headers (community APIs authenticate with custom headers) and a runner
// that can feed the node an item with a binary file.

type cmtyReq struct {
	method, path string
	query        url.Values
	body         string
	header       http.Header
}

func (r cmtyReq) json(t *testing.T) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return m
}

type cmtyFake struct {
	mu   sync.Mutex
	reqs []cmtyReq
	srv  *httptest.Server
}

// newCmtyFake serves handle's (status, content type, body); a string body
// is written as is, anything else as JSON.
func newCmtyFake(t *testing.T, handle func(r cmtyReq) (int, string, interface{})) *cmtyFake {
	f := &cmtyFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec := cmtyReq{r.Method, r.URL.Path, r.URL.Query(), string(b), r.Header.Clone()}
		f.mu.Lock()
		f.reqs = append(f.reqs, rec)
		f.mu.Unlock()
		code, ct, body := handle(rec)
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(code)
		if s, ok := body.(string); ok {
			_, _ = io.WriteString(w, s)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// find returns the last request with this method and path.
func (f *cmtyFake) find(t *testing.T, method, path string) cmtyReq {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := len(f.reqs) - 1; k >= 0; k-- {
		if r := f.reqs[k]; r.method == method && r.path == path {
			return r
		}
	}
	t.Fatalf("no %s %s; got %+v", method, path, f.reqs)
	return cmtyReq{}
}

func (f *cmtyFake) count(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.method == method && r.path == path {
			n++
		}
	}
	return n
}

// cmtyInput is an input file for cmtyRun.
type cmtyInput struct {
	name, mimeType, content string
}

// cmtyRun runs Start -> [file] -> node and returns the node's output items.
// bases maps APIBaseURLs keys to paths on the fake server ("" = root).
func cmtyRun(t *testing.T, f *cmtyFake, bases map[string]string, nodeType string, params map[string]interface{}, creds map[string]map[string]interface{}, file *cmtyInput) []Item {
	t.Helper()
	sc := NewStaticCredentials()
	var credType string
	for k, v := range creds {
		sc.Set(k, "*", v)
		credType = k
	}
	api := map[string]string{}
	for k, p := range bases {
		api[k] = f.srv.URL + p
	}
	h := newHarness(t, &Options{Credentials: sc, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: api})
	svc := map[string]interface{}{"name": "Svc", "type": nodeType, "typeVersion": 1, "parameters": params}
	if _, ok := params["__onError"]; ok {
		delete(params, "__onError")
		svc["onError"] = "continueRegularOutput"
	}
	nodes := []map[string]interface{}{nd("Start", "manualTrigger", 1, nil), credNode(svc, credType)}
	links := []string{"Start->Svc"}
	if file != nil {
		nodes = append(nodes,
			nd("Seed", "set", 3.4, map[string]interface{}{"assignments": map[string]interface{}{"assignments": []interface{}{
				assign("v", "string", base64.StdEncoding.EncodeToString([]byte(file.content)))}}}),
			nd("Bin", "convertToFile", 1.1, map[string]interface{}{"operation": "toBinary", "sourceProperty": "v",
				"options": map[string]interface{}{"fileName": file.name, "mimeType": file.mimeType}}))
		links = []string{"Start->Seed", "Seed->Bin", "Bin->Svc"}
	}
	data := wfJSON(t, nodes, links...)
	_, rep, err := Import(data, ImportOptions{})
	if err != nil || !rep.Runnable {
		t.Fatalf("not runnable: %v %+v", err, rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	if steps["Svc"].Error != "" {
		t.Fatalf("node failed: %s", steps["Svc"].Error)
	}
	return decodeOutput(steps["Svc"].Output)[0]
}

// cmtyErr runs the node with "continue on error" and returns the error
// message it outputs ("" when it succeeds).
func cmtyErr(t *testing.T, f *cmtyFake, bases map[string]string, nodeType string, params map[string]interface{}, creds map[string]map[string]interface{}) string {
	t.Helper()
	params = cloneJSON(params)
	params["__onError"] = true
	out := cmtyRun(t, f, bases, nodeType, params, creds, nil)
	if len(out) == 0 {
		return ""
	}
	return toString(out[0].JSON["error"])
}

// cmtyRunnable reports whether the importer accepts the node's parameters.
func cmtyRunnable(t *testing.T, nodeType string, params map[string]interface{}) bool {
	t.Helper()
	data := wfJSON(t, []map[string]interface{}{nd("Start", "manualTrigger", 1, nil),
		{"name": "Svc", "type": nodeType, "typeVersion": 1, "parameters": params}}, "Start->Svc")
	_, rep, _ := Import(data, ImportOptions{})
	return rep.Runnable
}

func TestCommunityHelpers(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Disposition", `attachment; filename="report final.pdf"`)
	if n := cmtyHeaderFileName(h); n != "report final.pdf" {
		t.Fatalf("filename %q", n)
	}
	h.Set("Content-Disposition", `attachment; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf`)
	if n := cmtyHeaderFileName(h); n != "résumé.pdf" {
		t.Fatalf("filename* %q", n)
	}
	if got := firecrawlURLs("a.com, https://b.com/x"); len(got) != 2 || got[0] != "https://a.com" {
		t.Fatalf("urls %v", got)
	}
	if got := mustJSON(firecrawlSchema(map[string]interface{}{"n": 1.0, "l": []interface{}{"x"}})); !strings.Contains(got, `"n":{"type":"integer"}`) || !strings.Contains(got, `"items":{"type":"string"}`) {
		t.Fatalf("schema %s", got)
	}
}
