package n8n

import (
	"encoding/base64"
	"net/http"
	"sync"
	"testing"

	"github.com/dgmos/flow/internal/node"
)

// communityRun runs a full node type (community packages included) at a
// type version against the fake API. When file is not empty the node
// receives an item with binary property "data" holding it (named fileName).
// creds may be nil for nodes without credentials.
func communityRun(t *testing.T, f *fakeAPI, apiKey, nodeType string, version float64, params map[string]interface{}, creds map[string]map[string]interface{}, file, fileName string) []Item {
	t.Helper()
	sc := NewStaticCredentials()
	svc := map[string]interface{}{"name": "Svc", "type": nodeType, "typeVersion": version, "parameters": params}
	for k, v := range creds {
		sc.Set(k, "*", v)
		svc = credNode(svc, k)
	}
	h := newHarness(t, &Options{Credentials: sc, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}, APIBaseURLs: map[string]string{apiKey: f.srv.URL}})
	nodes := []map[string]interface{}{nd("Start", "manualTrigger", 1, nil)}
	links := []string{"Start->Svc"}
	if file != "" {
		nodes = append(nodes,
			nd("Seed", "set", 3.4, map[string]interface{}{"assignments": map[string]interface{}{"assignments": []interface{}{
				assign("file", "string", base64.StdEncoding.EncodeToString([]byte(file)))}}}),
			nd("Bin", "convertToFile", 1.1, map[string]interface{}{"operation": "toBinary", "sourceProperty": "file", "options": map[string]interface{}{"fileName": fileName}}))
		links = []string{"Start->Seed", "Seed->Bin", "Bin->Svc"}
	}
	data := wfJSON(t, append(nodes, svc), links...)
	if _, rep, err := Import(data, ImportOptions{}); err != nil || !rep.Runnable {
		t.Fatalf("not runnable: %v %+v", err, rep.Blocking)
	}
	_, steps := h.run(t, data, "")
	if steps["Svc"].Error != "" {
		t.Fatalf("node failed: %s", steps["Svc"].Error)
	}
	return decodeOutput(steps["Svc"].Output)[0]
}

// importBlocked returns the blocking messages of a one-node workflow.
func importBlocked(t *testing.T, nodeType string, version float64, params map[string]interface{}) []string {
	t.Helper()
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		{"name": "Svc", "type": nodeType, "typeVersion": version, "parameters": params},
	}, "Start->Svc")
	_, rep, err := Import(data, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, b := range rep.Blocking {
		out = append(out, b.Message)
	}
	return out
}

// headerSpy records the values of request header name seen by f, by path.
func headerSpy(f *fakeAPI, name string) func(path string) string {
	var mu sync.Mutex
	seen := map[string]string{}
	h := f.srv.Config.Handler
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get(name)
		mu.Unlock()
		h.ServeHTTP(w, r)
	})
	return func(path string) string {
		mu.Lock()
		defer mu.Unlock()
		return seen[path]
	}
}
