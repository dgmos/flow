package n8n

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Airtop (n8n-nodes-base.airtop, versions 1 and 1.1): cloud browser
// sessions, windows, AI extraction, interactions, files and agents. Every
// n8n operation is programmatic, so each one is a run hook. Results are
// the API response spread after {sessionId, windowId}; API errors come as
// {"errors": [...]} in the body. Session/file waits stream the session's
// server-sent events; status checks poll.

// airtopPoll is the polling interval and airtopTimeout the wait limit of
// asynchronous operations (tests shorten them).
var (
	airtopPoll    = time.Second
	airtopTimeout = 5 * time.Minute
)

const (
	airtopTimeoutMsg  = "Timeout reached while waiting for the operation to complete"
	airtopRequiredMsg = "Please fill the '%s' parameter"
)

var (
	airtopProfileRe    = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)
	airtopResolutionRe = regexp.MustCompile(`^\d{3,4}x\d{3,4}$`)
	airtopScrollRe     = regexp.MustCompile(`^-?\d{1,3}(%|px)$`)
	airtopDataURLRe    = regexp.MustCompile(`^data:[^,]*;base64,`)
)

func init() {
	op := func(fn func(x *airtopCtx) ([]Item, error)) *restOp {
		return &restOp{run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
			x := &airtopCtx{cmtyArgs: &cmtyArgs{ec: ec, i: i}, it: it, c: c}
			items, err := fn(x)
			if err == nil && x.err != nil {
				err = x.err
			}
			if err != nil {
				return nil, err
			}
			if strings.HasSuffix(ec.node.Type, "Tool") {
				for k := range items {
					items[k].JSON = airtopToolOutput(items[k].JSON)
				}
			}
			return items, nil
		}}
	}
	registerREST("airtop", &restService{
		defResource: "session",
		defOps: map[string]string{"session": "create", "window": "create", "extraction": "getPaginated",
			"interaction": "click", "file": "getMany", "agent": "run"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "airtopApi", nil)
			if err != nil {
				return nil, err
			}
			cl := bearerClient(ec.apiBase("airtop", "https://api.airtop.ai/api/v1"), c, "apiKey")
			cl.headers["api-key"] = credString(c, "apiKey")
			cl.headers["x-airtop-sdk-environment"] = "n8n"
			cl.headers["x-airtop-sdk-version"] = "1.0.0"
			return cl, nil
		},
		ops: map[string]*restOp{
			"session/create":          op(airtopSessionCreate),
			"session/save":            op(airtopSessionSave),
			"session/terminate":       op(airtopSessionTerminate),
			"session/waitForDownload": op(airtopWaitForDownload),
			"window/create":           op(airtopWindowCreate),
			"window/getLiveView":      op(airtopWindowLiveView),
			"window/list":             op(airtopWindowList),
			"window/load":             op(airtopWindowLoad),
			"window/close":            op(airtopWindowClose),
			"window/takeScreenshot":   op(airtopScreenshot),
			"extraction/query":        op(airtopQuery),
			"extraction/getPaginated": op(airtopPaginated),
			"extraction/scrape":       op(airtopScrape),
			"interaction/click":       op(airtopClick),
			"interaction/hover":       op(airtopHover),
			"interaction/type":        op(airtopType),
			"interaction/scroll":      op(airtopScroll),
			"interaction/fill":        op(airtopFill),
			"file/get":                op(airtopFileGet),
			"file/getMany":            op(airtopFileGetMany),
			"file/deleteFile":         op(airtopFileDelete),
			"file/upload":             op(airtopFileUpload),
			"file/load":               op(airtopFileLoad),
			"agent/run":               op(airtopAgentRun),
		},
	})
}

// airtopCtx is one item's execution of an Airtop operation.
type airtopCtx struct {
	*cmtyArgs
	it Item
	c  *restClient
}

func airtopErr(format string, a ...interface{}) error {
	return permanentError{fmt.Sprintf(format, a...)}
}

func (x *airtopCtx) call(method, path string, q url.Values, body map[string]interface{}) (map[string]interface{}, error) {
	v, err := cmtyCall(x.ec, x.c, method, path, q, body)
	if err != nil {
		return nil, err
	}
	if m, ok := v.(map[string]interface{}); ok {
		return m, nil
	}
	return map[string]interface{}{}, nil
}

// checked is call followed by n8n's validateAirtopApiResponse.
func (x *airtopCtx) checked(method, path string, q url.Values, body map[string]interface{}) (map[string]interface{}, error) {
	m, err := x.call(method, path, q, body)
	if err != nil {
		return nil, err
	}
	return m, airtopCheck(m)
}

func airtopCheck(m map[string]interface{}) error {
	errs := asList(m["errors"])
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, toString(asMap(e)["message"]))
	}
	return permanentError{strings.Join(msgs, "\n")}
}

// id reads sessionId/windowId, whose n8n default is {{ $json.<name> }}.
func (x *airtopCtx) id(name string) string {
	if !x.has(name) {
		return strings.TrimSpace(toString(x.it.JSON[name]))
	}
	return strings.TrimSpace(x.str(name, ""))
}

func (x *airtopCtx) required(name, label string) (string, error) {
	v := strings.TrimSpace(x.str(name, ""))
	if v == "" {
		return "", airtopErr(airtopRequiredMsg, label)
	}
	return v, nil
}

func (x *airtopCtx) sessionID() (string, error) {
	s := x.id("sessionId")
	if s == "" {
		return "", airtopErr("Please fill the 'Session ID' parameter")
	}
	return s, nil
}

func (x *airtopCtx) sessionWindow() (string, string, error) {
	s, err := x.sessionID()
	if err != nil {
		return "", "", err
	}
	w := x.id("windowId")
	if w == "" {
		return "", "", airtopErr("Please fill the 'Window ID' parameter")
	}
	return s, w, nil
}

func (x *airtopCtx) profileName() (string, error) {
	p := strings.TrimSpace(x.str("profileName", ""))
	if p != "" && !airtopProfileRe.MatchString(p) {
		return "", airtopErr("'Browser Profile ID' should only contain letters, numbers and dashes")
	}
	return p, nil
}

func (x *airtopCtx) url(required bool) (string, error) {
	u := strings.TrimSpace(x.str("url", ""))
	if u == "" && required {
		return "", airtopErr(airtopRequiredMsg, "URL")
	}
	if u != "" && !strings.HasPrefix(u, "http") {
		return "", airtopErr("'URL' must start with 'http' or 'https'")
	}
	return u, nil
}

func airtopResolution(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v != "" && !airtopResolutionRe.MatchString(v) {
		return "", airtopErr("'Screen Resolution' must be in the format 'width x height' (e.g. '1280x720')")
	}
	return v, nil
}

func airtopSessionPath(s string, rest ...string) string {
	p := "/sessions/" + url.PathEscape(s)
	for k, r := range rest {
		if k%2 == 0 {
			p += "/" + r
		} else {
			p += "/" + url.PathEscape(r)
		}
	}
	return p
}

func airtopWindowPath(s, w, suffix string) string {
	return airtopSessionPath(s, "windows", w) + suffix
}

// airtopOut returns {sessionId, windowId, ...response} as one item.
func airtopOut(s, w string, res map[string]interface{}) []Item {
	front := map[string]interface{}{}
	if s != "" {
		front["sessionId"] = s
	}
	if w != "" {
		front["windowId"] = w
	}
	return []Item{{JSON: cmtyMerge(front, res)}}
}

func (x *airtopCtx) wait(start time.Time, limit time.Duration) error {
	if time.Since(start) >= limit {
		return airtopErr(airtopTimeoutMsg)
	}
	return cmtySleep(x.ec, airtopPoll)
}

// ---------------------------------------------------------------- sessions

// createSession creates a session and polls until it is running.
func (x *airtopCtx) createSession(body map[string]interface{}) (string, map[string]interface{}, error) {
	res, err := x.call("POST", "/sessions", nil, body)
	if err != nil {
		return "", nil, err
	}
	id := toString(jpath(res, "data.id"))
	if id == "" {
		return "", nil, airtopErr("Failed to create session")
	}
	status := toString(jpath(res, "data.status"))
	start := time.Now()
	for status != "running" {
		if err := x.wait(start, airtopTimeout); err != nil {
			return "", nil, err
		}
		st, err := x.call("GET", airtopSessionPath(id), nil, nil)
		if err != nil {
			return "", nil, err
		}
		status = toString(jpath(st, "data.status"))
	}
	return id, res, nil
}

func (x *airtopCtx) proxy() (interface{}, error) {
	switch x.str("proxy", "none") {
	case "integrated":
		if !x.has("proxyConfig") {
			return map[string]interface{}{"country": "US", "sticky": true}, nil
		}
		if m := asMap(x.get("proxyConfig", nil)); len(m) > 0 {
			return m, nil
		}
		return true, nil
	case "proxyUrl":
		return x.required("proxyUrl", "Proxy URL")
	}
	return false, nil
}

func airtopSessionCreate(x *airtopCtx) ([]Item, error) {
	profile, err := x.profileName()
	if err != nil {
		return nil, err
	}
	timeout := x.num("timeoutMinutes", 10)
	if timeout < 1 || timeout > 10080 {
		return nil, airtopErr("Timeout must be between 1 and 10080 minutes")
	}
	save := x.bool("saveProfileOnTermination", false)
	if save && profile == "" {
		return nil, airtopErr("'Browser Profile ID' is required when 'Save Profile' is enabled")
	}
	proxy, err := x.proxy()
	if err != nil {
		return nil, err
	}
	cfg := map[string]interface{}{"profileName": profile, "timeoutMinutes": timeout, "proxy": proxy,
		"solveCaptcha": x.bool("additionalFields.solveCaptcha", false), "record": x.bool("record", false)}
	if ext := x.str("additionalFields.extensionIds", ""); ext != "" {
		var ids []interface{}
		for _, s := range strings.Split(ext, ",") {
			ids = append(ids, strings.TrimSpace(s))
		}
		cfg["extensionIds"] = ids
	}
	if x.err != nil {
		return nil, x.err
	}
	id, res, err := x.createSession(map[string]interface{}{"configuration": cfg})
	if err != nil {
		return nil, err
	}
	if save {
		if _, err := x.call("PUT", airtopSessionPath(id, "save-profile-on-termination", profile), nil, nil); err != nil {
			return nil, err
		}
	}
	return airtopOut(id, "", res), nil
}

func airtopSessionSave(x *airtopCtx) ([]Item, error) {
	s, err := x.sessionID()
	if err != nil {
		return nil, err
	}
	if _, err := x.required("profileName", "Browser Profile ID"); err != nil {
		return nil, err
	}
	profile, err := x.profileName()
	if err != nil {
		return nil, err
	}
	res, err := x.checked("PUT", airtopSessionPath(s, "save-profile-on-termination", profile), nil, nil)
	if err != nil {
		return nil, err
	}
	return []Item{{JSON: cmtyMerge(map[string]interface{}{"sessionId": s, "profileName": profile}, res)}}, nil
}

func airtopSessionTerminate(x *airtopCtx) ([]Item, error) {
	s, err := x.sessionID()
	if err != nil {
		return nil, err
	}
	if _, err := x.checked("DELETE", airtopSessionPath(s), nil, nil); err != nil {
		return nil, err
	}
	return []Item{{JSON: map[string]interface{}{"success": true}}}, nil
}

func airtopWaitForDownload(x *airtopCtx) ([]Item, error) {
	s, err := x.sessionID()
	if err != nil {
		return nil, err
	}
	// n8n reads a top-level "timeout" that the operation does not show, so
	// it always waits 30s; the shown additionalFields.timeout is honored.
	secs := x.num("timeout", x.num("additionalFields.timeout", 30))
	ev, err := x.waitEvent(s, time.Duration(secs*float64(time.Second)), func(e map[string]interface{}) bool {
		return e["event"] == "file_status" && e["status"] == "available"
	})
	if err != nil {
		return nil, err
	}
	return []Item{{JSON: map[string]interface{}{"sessionId": s,
		"data": map[string]interface{}{"fileId": ev["fileId"], "downloadUrl": ev["downloadUrl"]}}}}, nil
}

// waitEvent reads the session's event stream until cond matches,
// reconnecting when the stream ends.
func (x *airtopCtx) waitEvent(s string, limit time.Duration, cond func(map[string]interface{}) bool) (map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(x.ec.ctx, limit)
	defer cancel()
	u := x.c.base + airtopSessionPath(s, "events") + "?all=true"
	for {
		ev, err := airtopStream(ctx, x.ec, x.c, u, cond)
		if ev != nil {
			return ev, nil
		}
		if x.ec.ctx.Err() != nil {
			return nil, x.ec.ctx.Err()
		}
		if ctx.Err() != nil {
			return nil, airtopErr(airtopTimeoutMsg)
		}
		var perm permanentError
		if errors.As(err, &perm) {
			return nil, err
		}
		t := time.NewTimer(airtopPoll)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
	}
}

func airtopStream(ctx context.Context, ec *execCtx, c *restClient, u string, cond func(map[string]interface{}) bool) (map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := outboundClient(ec.opts.Outbound).Do(req)
	if err != nil {
		return nil, sanitizeErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		msg := fmt.Sprintf("GET %s failed with status %d", redactURL(u), resp.StatusCode)
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			return nil, errors.New(msg)
		}
		return nil, permanentError{msg}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev map[string]interface{}
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev) != nil {
			continue
		}
		if cond(ev) {
			return ev, nil
		}
	}
	return nil, sc.Err()
}

// ---------------------------------------------------------------- windows

func airtopWindowCreate(x *airtopCtx) ([]Item, error) {
	s, err := x.sessionID()
	if err != nil {
		return nil, err
	}
	u, err := x.url(false)
	if err != nil {
		return nil, err
	}
	res, err := airtopResolution(x.str("screenResolution", ""))
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{"url": u}
	for k, v := range asMap(x.get("additionalFields", nil)) {
		body[k] = v
	}
	if x.err != nil {
		return nil, x.err
	}
	out, err := x.call("POST", airtopSessionPath(s, "windows"), nil, body)
	if err != nil {
		return nil, err
	}
	if err := airtopCheck(out); err != nil {
		return nil, err
	}
	w := toString(jpath(out, "data.windowId"))
	if w == "" {
		return nil, airtopErr("Failed to create window")
	}
	if x.bool("getLiveView", false) {
		q := url.Values{}
		if x.bool("includeNavigationBar", false) {
			q.Set("includeNavigationBar", "true")
		}
		if res != "" {
			q.Set("screenResolution", res)
		}
		if x.bool("disableResize", false) {
			q.Set("disableResize", "true")
		}
		if out, err = x.checked("GET", airtopWindowPath(s, w, ""), q, nil); err != nil {
			return nil, err
		}
	}
	return airtopOut(s, w, out), nil
}

func airtopWindowLiveView(x *airtopCtx) ([]Item, error) {
	s, w, err := x.sessionWindow()
	if err != nil {
		return nil, err
	}
	add := asMap(x.get("additionalFields", nil))
	q := url.Values{}
	if toBool(add["includeNavigationBar"]) {
		q.Set("includeNavigationBar", "true")
	}
	if r := toString(add["screenResolution"]); r != "" {
		if r, err = airtopResolution(r); err != nil {
			return nil, err
		}
		q.Set("screenResolution", r)
	}
	if toBool(add["disableResize"]) {
		q.Set("disableResize", "true")
	}
	res, err := x.checked("GET", airtopWindowPath(s, w, ""), q, nil)
	if err != nil {
		return nil, err
	}
	return airtopOut(s, w, res), nil
}

func airtopWindowList(x *airtopCtx) ([]Item, error) {
	s, err := x.sessionID()
	if err != nil {
		return nil, err
	}
	res, err := x.checked("GET", airtopSessionPath(s, "windows"), nil, nil)
	if err != nil {
		return nil, err
	}
	return airtopOut(s, "", res), nil
}

func airtopWindowLoad(x *airtopCtx) ([]Item, error) {
	s, w, err := x.sessionWindow()
	if err != nil {
		return nil, err
	}
	u, err := x.url(true)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{"url": u}
	if v := x.get("additionalFields.waitUntil", nil); v != nil {
		body["waitUntil"] = v
	}
	res, err := x.checked("POST", airtopWindowPath(s, w, ""), nil, body)
	if err != nil {
		return nil, err
	}
	return airtopOut(s, w, res), nil
}

func airtopWindowClose(x *airtopCtx) ([]Item, error) {
	s, w, err := x.sessionWindow()
	if err != nil {
		return nil, err
	}
	res, err := x.checked("DELETE", airtopWindowPath(s, w, ""), nil, nil)
	if err != nil {
		return nil, err
	}
	return airtopOut(s, w, res), nil
}

func airtopScreenshot(x *airtopCtx) ([]Item, error) {
	s, w, err := x.sessionWindow()
	if err != nil {
		return nil, err
	}
	asBinary := x.bool("outputImageAsBinary", false)
	res, err := x.checked("POST", airtopWindowPath(s, w, "/screenshot"), nil, nil)
	if err != nil {
		return nil, err
	}
	image := ""
	var bin map[string]interface{}
	if shots := asList(jpath(res, "meta.screenshots")); len(shots) > 0 {
		dataURL := toString(asMap(shots[0])["dataUrl"])
		if asBinary {
			data, err := base64.StdEncoding.DecodeString(airtopDataURLRe.ReplaceAllString(dataURL, ""))
			if err != nil {
				return nil, airtopErr("screenshot is not valid base64")
			}
			bin = map[string]interface{}{"data": binaryData(data, "image/jpeg", "screenshot.jpg")}
		} else {
			image = dataURL
		}
	}
	return []Item{{JSON: map[string]interface{}{"sessionId": s, "windowId": w, "image": image}, Binary: bin}}, nil
}

// ---------------------------------------------------------------- extraction

func (x *airtopCtx) terminate(s string) error {
	_, err := x.call("DELETE", airtopSessionPath(s), nil, nil)
	return err
}

// createSessionAndWindow backs the "Automatically Create Session" mode. The
// session id is returned even when the window fails so it can be cleaned up.
func (x *airtopCtx) createSessionAndWindow() (string, string, error) {
	profile, err := x.profileName()
	if err != nil {
		return "", "", err
	}
	u, err := x.required("url", "URL")
	if err != nil {
		return "", "", err
	}
	s, _, err := x.createSession(map[string]interface{}{"configuration": map[string]interface{}{"profileName": profile}})
	if err != nil {
		return "", "", err
	}
	res, err := x.call("POST", airtopSessionPath(s, "windows"), nil, map[string]interface{}{"url": u})
	if err != nil {
		return s, "", err
	}
	w := toString(jpath(res, "data.windowId"))
	if w == "" {
		return s, "", airtopErr("Failed to create window")
	}
	return s, w, nil
}

// withSession runs an extraction request on an existing session or on a
// new one, terminating it afterwards (autoTerminateSession) or on error.
func (x *airtopCtx) withSession(path func(s, w string) string, body map[string]interface{}) (map[string]interface{}, error) {
	isNew := x.str("sessionMode", "existing") == "new"
	var s, w string
	var err error
	if isNew {
		s, w, err = x.createSessionAndWindow()
	} else {
		s, w, err = x.sessionWindow()
	}
	if err == nil && x.err != nil {
		err = x.err
	}
	var res map[string]interface{}
	if err == nil {
		res, err = x.checked("POST", path(s, w), nil, body)
	}
	if err == nil && isNew && x.bool("autoTerminateSession", true) {
		if err = x.terminate(s); err == nil {
			return res, nil
		}
	}
	if err != nil {
		if s != "" {
			_ = x.terminate(s)
		}
		return nil, err
	}
	return airtopOut(s, w, res)[0].JSON, nil
}

// parseJSON mirrors parseJsonIfPresent: with Parse JSON Output and a JSON
// schema, the model response becomes "output".
func (x *airtopCtx) parseJSON(res map[string]interface{}) ([]Item, error) {
	schema := x.get("additionalFields.outputSchema", "")
	if s, ok := schema.(string); !x.bool("additionalFields.parseJsonOutput", false) || (ok && !strings.HasPrefix(s, "{")) || schema == nil {
		return []Item{{JSON: res}}, nil
	}
	var out interface{}
	if json.Unmarshal([]byte(toString(jpath(res, "data.modelResponse"))), &out) != nil {
		return nil, airtopErr("Output is not a valid JSON")
	}
	j := map[string]interface{}{"output": out}
	for _, k := range []string{"sessionId", "windowId"} {
		if v, ok := res[k]; ok {
			j[k] = v
		}
	}
	return []Item{{JSON: j}}, nil
}

func airtopSchema(v interface{}) interface{} {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return nil
	}
	return mustJSON(v)
}

func airtopQuery(x *airtopCtx) ([]Item, error) {
	add := asMap(x.get("additionalFields", nil))
	visual := "disabled"
	if toBool(add["includeVisualAnalysis"]) {
		visual = "enabled"
	}
	cfg := map[string]interface{}{"experimental": map[string]interface{}{"includeVisualAnalysis": visual}}
	if s := airtopSchema(add["outputSchema"]); !restEmpty(s) {
		cfg["outputSchema"] = s
	}
	body := map[string]interface{}{"prompt": x.str("prompt", ""), "configuration": cfg}
	res, err := x.withSession(func(s, w string) string { return airtopWindowPath(s, w, "/page-query") }, body)
	if err != nil {
		return nil, err
	}
	return x.parseJSON(res)
}

func airtopPaginated(x *airtopCtx) ([]Item, error) {
	add := asMap(x.get("additionalFields", nil))
	cfg := map[string]interface{}{}
	for _, k := range []string{"paginationMode", "interactionMode", "outputSchema"} {
		v := add[k]
		if k == "outputSchema" {
			v = airtopSchema(v)
		}
		if airtopTruthy(v) {
			cfg[k] = v
		}
	}
	body := map[string]interface{}{"prompt": x.str("prompt", ""), "configuration": cfg}
	res, err := x.withSession(func(s, w string) string { return airtopWindowPath(s, w, "/paginated-extraction") }, body)
	if err != nil {
		return nil, err
	}
	return x.parseJSON(res)
}

func airtopScrape(x *airtopCtx) ([]Item, error) {
	res, err := x.withSession(func(s, w string) string { return airtopWindowPath(s, w, "/scrape-content") }, map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	return []Item{{JSON: res}}, nil
}

// ---------------------------------------------------------------- interactions

// interaction adds the shared visual scope / navigation options and posts.
func (x *airtopCtx) interaction(action string, body, cfg map[string]interface{}) ([]Item, error) {
	s, w, err := x.sessionWindow()
	if err != nil {
		return nil, err
	}
	return x.interactionOn(s, w, action, body, cfg)
}

func (x *airtopCtx) interactionOn(s, w, action string, body, cfg map[string]interface{}) ([]Item, error) {
	add := asMap(x.get("additionalFields", nil))
	if cfg == nil {
		cfg = map[string]interface{}{}
	}
	if v := toString(add["visualScope"]); v != "" {
		cfg["visualAnalysis"] = map[string]interface{}{"scope": v}
	}
	if v := toString(add["waitForNavigation"]); v != "" {
		body["waitForNavigation"] = true
		cfg["waitForNavigationConfig"] = map[string]interface{}{"waitUntil": v}
	}
	body["configuration"] = cfg
	if x.err != nil {
		return nil, x.err
	}
	res, err := x.checked("POST", airtopWindowPath(s, w, "/"+action), nil, body)
	if err != nil {
		return nil, err
	}
	return airtopOut(s, w, res), nil
}

func airtopClick(x *airtopCtx) ([]Item, error) {
	if _, _, err := x.sessionWindow(); err != nil {
		return nil, err
	}
	desc, err := x.required("elementDescription", "Element Description")
	if err != nil {
		return nil, err
	}
	ct := strings.TrimSpace(x.str("clickType", "click"))
	if ct == "" {
		return nil, airtopErr(airtopRequiredMsg, "Click Type")
	}
	return x.interaction("click", map[string]interface{}{"elementDescription": desc}, map[string]interface{}{"clickType": ct})
}

func airtopHover(x *airtopCtx) ([]Item, error) {
	if _, _, err := x.sessionWindow(); err != nil {
		return nil, err
	}
	desc, err := x.required("elementDescription", "Element Description")
	if err != nil {
		return nil, err
	}
	return x.interaction("hover", map[string]interface{}{"elementDescription": desc}, nil)
}

func airtopType(x *airtopCtx) ([]Item, error) {
	if _, _, err := x.sessionWindow(); err != nil {
		return nil, err
	}
	text, err := x.required("text", "Text")
	if err != nil {
		return nil, err
	}
	return x.interaction("type", map[string]interface{}{"text": text, "pressEnterKey": x.bool("pressEnterKey", false),
		"elementDescription": x.str("elementDescription", "")}, nil)
}

func airtopScroll(x *airtopCtx) ([]Item, error) {
	s, w, err := x.sessionWindow()
	if err != nil {
		return nil, err
	}
	mode := x.str("scrollingMode", "automatic")
	edge := asMap(x.get("scrollToEdge.edgeValues", nil))
	by := asMap(x.get("scrollBy.scrollValues", nil))
	empty := func(m map[string]interface{}) bool { return !airtopTruthy(m["xAxis"]) && !airtopTruthy(m["yAxis"]) }
	if mode == "manual" && empty(edge) && empty(by) {
		return nil, airtopErr("Please fill any of the 'Scroll To Edge' or 'Scroll By' parameters")
	}
	body := map[string]interface{}{}
	if mode == "automatic" {
		el, err := x.required("scrollToElement", "Element Description")
		if err != nil {
			return nil, err
		}
		body["scrollToElement"] = el
	} else {
		if empty(by) {
			by = map[string]interface{}{}
		}
		for _, k := range []string{"xAxis", "yAxis"} {
			if v := toString(by[k]); airtopTruthy(by[k]) && !airtopScrollRe.MatchString(v) {
				return nil, airtopErr("'Scroll By' amount must be a number and either a percentage or pixels (e.g. '100px' or '100%%')")
			}
		}
		body["scrollToEdge"], body["scrollBy"] = edge, by
	}
	if v := x.str("scrollWithin", ""); v != "" {
		body["scrollWithin"] = v
	}
	return x.interactionOn(s, w, "scroll", body, nil)
}

func airtopFill(x *airtopCtx) ([]Item, error) {
	s, w, err := x.sessionWindow()
	if err != nil {
		return nil, err
	}
	form, err := x.required("formData", "Form Data")
	if err != nil {
		return nil, err
	}
	res, err := x.call("POST", "/async"+airtopWindowPath(s, w, "/execute-automation"), nil, map[string]interface{}{
		"automationId": "auto", "parameters": map[string]interface{}{"customData": form}})
	if err != nil {
		return nil, err
	}
	req := toString(res["requestId"])
	start := time.Now()
	for {
		st, err := x.checked("GET", "/requests/"+url.PathEscape(req)+"/status", nil, nil)
		if err != nil {
			return nil, err
		}
		if v := toString(st["status"]); v == "completed" || v == "error" {
			return airtopOut(s, w, st), nil
		}
		if err := x.wait(start, airtopTimeout); err != nil {
			return nil, err
		}
	}
}

// ---------------------------------------------------------------- files

func (x *airtopCtx) fileID() (string, error) {
	f := x.str("fileId", "")
	if f == "" {
		return "", airtopErr(airtopRequiredMsg, "File ID")
	}
	return f, nil
}

func airtopFileGet(x *airtopCtx) ([]Item, error) {
	f, err := x.fileID()
	if err != nil {
		return nil, err
	}
	asBinary := x.bool("outputBinaryFile", false)
	res, err := x.call("GET", "/files/"+url.PathEscape(f), nil, nil)
	if err != nil {
		return nil, err
	}
	data := asMap(res["data"])
	dl := toString(data["downloadUrl"])
	if asBinary && dl != "" && data["status"] == "available" {
		r, err := x.ec.doRequest("GET", dl, nil, nil, "")
		if err != nil {
			return nil, err
		}
		name := toString(data["fileName"])
		mt := mimeByExt(name)
		if mt == "" || mt == "application/octet-stream" {
			mt = firstNonEmpty(strings.TrimSpace(strings.Split(r.contentType, ";")[0]), "application/octet-stream")
		}
		return []Item{{JSON: res, Binary: map[string]interface{}{"data": binaryData(r.body, mt, name)}}}, nil
	}
	return []Item{{JSON: res}}, nil
}

func airtopFileGetMany(x *airtopCtx) ([]Item, error) {
	ids := x.str("sessionIds", "")
	single := x.bool("outputSingleItem", true)
	var res map[string]interface{}
	if x.bool("returnAll", false) {
		var files []interface{}
		more := true
		for off := 0; more; off += 100 {
			q := url.Values{"offset": {fmt.Sprint(off)}, "limit": {"100"}, "sessionIds": {ids}}
			page, err := x.call("GET", "/files", q, nil)
			if err != nil {
				return nil, err
			}
			files = append(files, asList(jpath(page, "data.files"))...)
			more = toBool(jpath(page, "data.pagination.hasMore"))
		}
		if files == nil {
			files = []interface{}{}
		}
		res = map[string]interface{}{"data": map[string]interface{}{"files": files, "pagination": map[string]interface{}{"hasMore": false}}}
	} else {
		q := url.Values{"sessionIds": {ids}, "limit": {toString(x.get("limit", 10.0))}}
		var err error
		if res, err = x.call("GET", "/files", q, nil); err != nil {
			return nil, err
		}
	}
	if single {
		return []Item{{JSON: res}}, nil
	}
	return restItems(asList(jpath(res, "data.files"))), nil
}

func airtopFileDelete(x *airtopCtx) ([]Item, error) {
	f, err := x.fileID()
	if err != nil {
		return nil, err
	}
	if _, err := x.call("DELETE", "/files/"+url.PathEscape(f), nil, nil); err != nil {
		return nil, err
	}
	return []Item{{JSON: map[string]interface{}{"data": map[string]interface{}{"message": "File deleted successfully"}}}}, nil
}

// pushFile makes a file available in a session and waits for it there.
func (x *airtopCtx) pushFile(f, s string) error {
	if _, err := x.call("POST", "/files/"+url.PathEscape(f)+"/push", nil, map[string]interface{}{"sessionIds": []interface{}{s}}); err != nil {
		return err
	}
	ev, err := x.waitEvent(s, airtopTimeout, func(e map[string]interface{}) bool {
		return toString(e["fileId"]) == f && e["event"] == "file_upload_status" && (e["status"] == "available" || e["status"] == "upload_failed")
	})
	if err != nil {
		return err
	}
	if ev["status"] == "upload_failed" {
		msg := toString(asMap(ev["eventData"])["error"])
		if msg == "" {
			msg = "Upload failed for File ID: " + f
		}
		return airtopErr("%s", msg)
	}
	return nil
}

func (x *airtopCtx) fileInput(s, w, f string) error {
	_, err := x.call("POST", airtopWindowPath(s, w, "/file-input"), nil, map[string]interface{}{"fileId": f,
		"elementDescription": x.str("elementDescription", ""), "includeHiddenElements": x.bool("includeHiddenElements", true)})
	return err
}

func airtopFileUpload(x *airtopCtx) ([]Item, error) {
	s, w, err := x.sessionWindow()
	if err != nil {
		return nil, err
	}
	name, fileType := x.str("fileName", ""), x.str("fileType", "customer_upload")
	var data []byte
	switch src := x.str("source", "url"); src {
	case "url":
		r, err := x.ec.doRequest("GET", x.str("url", ""), nil, nil, "")
		if err != nil {
			return nil, err
		}
		data = r.body
	case "binary":
		if data, _, _, err = binaryOf(x.it, strings.TrimSpace(x.str("binaryPropertyName", "data"))); err != nil {
			return nil, err
		}
	default:
		return nil, airtopErr("Unsupported source type: %s. Please use 'url' or 'binary'", src)
	}
	if x.err != nil {
		return nil, x.err
	}
	created, err := x.call("POST", "/files", nil, map[string]interface{}{"fileName": name, "fileType": fileType})
	if err != nil {
		return nil, err
	}
	f, up := toString(jpath(created, "data.id")), toString(jpath(created, "data.uploadUrl"))
	if f == "" || up == "" {
		return nil, airtopErr("Failed to create file entry: missing file ID or upload URL")
	}
	if _, err := x.ec.doRequest("PUT", up, nil, data, "application/octet-stream"); err != nil {
		return nil, err
	}
	start := time.Now()
	for {
		st, err := x.call("GET", "/files/"+url.PathEscape(f), nil, nil)
		if err != nil {
			return nil, err
		}
		if jpath(st, "data.status") == "available" {
			break
		}
		if err := x.wait(start, airtopTimeout); err != nil {
			return nil, err
		}
	}
	if err := x.pushFile(f, s); err != nil {
		return nil, err
	}
	if x.bool("triggerFileInputParameter", true) {
		if err := x.fileInput(s, w, f); err != nil {
			return nil, err
		}
	}
	return []Item{{JSON: map[string]interface{}{"sessionId": s, "windowId": w,
		"data": map[string]interface{}{"fileId": f, "message": "File uploaded successfully"}}}}, nil
}

func airtopFileLoad(x *airtopCtx) ([]Item, error) {
	f, s, w := x.str("fileId", ""), x.id("sessionId"), x.id("windowId")
	if err := x.pushFile(f, s); err != nil {
		return nil, err
	}
	if err := x.fileInput(s, w, f); err != nil {
		return nil, err
	}
	return []Item{{JSON: map[string]interface{}{"sessionId": s, "windowId": w,
		"data": map[string]interface{}{"message": "File loaded successfully"}}}}, nil
}

// ---------------------------------------------------------------- agents

// at returns a client for a sibling of /v1 (the /v2 and /hooks APIs).
func (x *airtopCtx) at(prefix string) *restClient {
	return &restClient{base: strings.TrimSuffix(x.c.base, "/v1") + prefix, headers: x.c.headers}
}

func airtopAgentRun(x *airtopCtx) ([]Item, error) {
	agent := rlValue(x.get("agentId", ""))
	params := asMap(x.get("agentParameters", nil))
	await := x.bool("awaitExecution", true)
	timeout := x.num("timeout", 600)
	profile, err := x.profileName()
	if err != nil {
		return nil, err
	}
	if timeout < 10 {
		return nil, airtopErr("Timeout must be at least 10 seconds")
	}
	vars := asMap(params["value"])
	var missing []string
	for _, f := range asList(params["schema"]) {
		m := asMap(f)
		if !toBool(m["required"]) {
			continue
		}
		id := toString(m["id"])
		if v, ok := vars[id]; !ok || v == nil || v == "" {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, airtopErr("Missing required parameters: %s", strings.Join(missing, ", "))
	}
	if x.err != nil {
		return nil, x.err
	}
	v2, hooks := x.at("/v2"), x.at("/hooks")
	ag := "/agents/" + url.PathEscape(agent)
	details, err := cmtyCall(x.ec, v2, "GET", ag, nil, nil)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if profile != "" {
		q.Set("profileId", profile)
	}
	inv, err := cmtyCall(x.ec, hooks, "POST", ag+"/webhooks/"+url.PathEscape(toString(asMap(details)["webhookId"])), q,
		map[string]interface{}{"configVars": vars})
	if err != nil {
		return nil, err
	}
	id := toString(asMap(inv)["invocationId"])
	if id == "" {
		return nil, airtopErr("No 'invocationId' received from agent webhook response")
	}
	if !await {
		return []Item{{JSON: map[string]interface{}{"invocationId": id}}}, nil
	}
	start, limit := time.Now(), time.Duration(timeout*float64(time.Second))
	for {
		if time.Since(start) >= limit {
			return nil, airtopErr(airtopTimeoutMsg)
		}
		r, err := cmtyCall(x.ec, hooks, "GET", ag+"/invocations/"+url.PathEscape(id)+"/result", nil, nil)
		if err != nil {
			return nil, err
		}
		m := asMap(r)
		if airtopTruthy(m["error"]) {
			return nil, airtopErr("%s. Agent Invocation ID: %s", toString(m["error"]), id)
		}
		if airtopTruthy(m["output"]) {
			status := m["status"]
			if !airtopTruthy(status) {
				status = "Unknown"
			}
			return []Item{{JSON: map[string]interface{}{"invocationId": id, "status": status, "output": m["output"]}}}, nil
		}
		if err := cmtySleep(x.ec, airtopPoll); err != nil {
			return nil, err
		}
	}
}

// ---------------------------------------------------------------- helpers

// airtopTruthy is JavaScript truthiness.
func airtopTruthy(v interface{}) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	}
	return true
}

// airtopToolOutput mirrors cleanOutputForToolUse for the airtopTool variant.
func airtopToolOutput(j map[string]interface{}) map[string]interface{} {
	if errs := asList(j["errors"]); len(errs) > 0 {
		return map[string]interface{}{"output": "Error: " + toString(asMap(errs[0])["message"])}
	}
	if airtopTruthy(j["output"]) {
		if m, ok := j["output"].(map[string]interface{}); ok {
			return m
		}
		return map[string]interface{}{"output": j["output"]}
	}
	data := asMap(j["data"])
	if airtopTruthy(data["modelResponse"]) {
		return map[string]interface{}{"output": data["modelResponse"]}
	}
	out := map[string]interface{}{}
	for k, v := range data {
		out[k] = v
	}
	return map[string]interface{}{"output": out}
}
