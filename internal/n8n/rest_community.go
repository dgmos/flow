package n8n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Helpers shared by the community-node REST connectors (rest_firecrawl.go,
// rest_htmlcsstopdf.go, …). Most community nodes are programmatic: they read
// parameters with defaults, call one or two endpoints and shape the result,
// which run hooks express with these helpers.

// cmtyArgs reads parameters of item i with n8n defaults, keeping the first
// expression error.
type cmtyArgs struct {
	ec  *execCtx
	i   int
	err error
}

func (a *cmtyArgs) get(name string, def interface{}) interface{} {
	v, err := a.ec.param(a.i, name, def)
	if err != nil && a.err == nil {
		a.err = err
	}
	if v == nil {
		return def
	}
	return v
}

func (a *cmtyArgs) str(name, def string) string { return toString(a.get(name, def)) }

func (a *cmtyArgs) num(name string, def float64) float64 {
	if f, ok := toFloat(a.get(name, def)); ok {
		return f
	}
	return def
}

func (a *cmtyArgs) bool(name string, def bool) bool { return toBool(a.get(name, def)) }

// has reports whether the node sets a parameter at all.
func (a *cmtyArgs) has(name string) bool {
	_, ok := a.ec.raw(name)
	return ok
}

// cmtyJSON parses a JSON parameter value (strings are parsed, objects kept).
func cmtyJSON(v interface{}, what string) (interface{}, error) {
	s, ok := v.(string)
	if !ok {
		return v, nil
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var x interface{}
	if err := json.Unmarshal([]byte(s), &x); err != nil {
		return nil, permanentError{fmt.Sprintf("%s must be valid JSON: %v", what, err)}
	}
	return x, nil
}

// cmtyCall sends a JSON request through a REST client and decodes the
// response. body may be nil (no body for GET/DELETE).
func cmtyCall(ec *execCtx, c *restClient, method, path string, q url.Values, body map[string]interface{}) (interface{}, error) {
	if q == nil {
		q = url.Values{}
	}
	if body == nil {
		body = map[string]interface{}{}
	}
	return c.do(ec, &restRequest{method: method, path: path, query: q, body: body})
}

// cmtyRaw sends a request through a REST client and returns the raw
// response (binary downloads, multipart uploads).
func cmtyRaw(ec *execCtx, c *restClient, method, path string, q url.Values, body []byte, ct string) (rawResp, error) {
	u := c.base + path
	all := url.Values{}
	for k, v := range c.query {
		all[k] = v
	}
	for k, v := range q {
		all[k] = v
	}
	if len(all) > 0 {
		u += "?" + all.Encode()
	}
	h := map[string]string{"Accept": "*/*"}
	for k, v := range c.headers {
		h[k] = v
	}
	return ec.doRequest(method, u, h, body, ct)
}

// decodeJSONBody decodes a raw response body; non-JSON text is kept as
// {"data": text}.
func decodeJSONBody(b []byte) interface{} {
	t := bytes.TrimSpace(b)
	if len(t) == 0 {
		return nil
	}
	var v interface{}
	if json.Unmarshal(t, &v) != nil {
		return map[string]interface{}{"data": string(b)}
	}
	return v
}

type cmtyFile struct {
	field, name, mimeType string
	data                  []byte
}

// cmtyMultipart builds a multipart/form-data body; fields keep their order.
func cmtyMultipart(fields [][2]string, files ...cmtyFile) ([]byte, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		_ = w.WriteField(f[0], f[1])
	}
	for _, f := range files {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, f.field, f.name))
		h.Set("Content-Type", f.mimeType)
		p, _ := w.CreatePart(h)
		_, _ = p.Write(f.data)
	}
	_ = w.Close()
	return buf.Bytes(), w.FormDataContentType()
}

// cmtySleep waits d unless the execution is cancelled.
func cmtySleep(ec *execCtx, d time.Duration) error {
	if d <= 0 {
		return ec.ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ec.ctx.Done():
		return ec.ctx.Err()
	case <-t.C:
		return nil
	}
}

// cmtyMerge returns front's fields followed by v's (n8n's {...front, ...v}).
func cmtyMerge(front map[string]interface{}, v interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, x := range front {
		out[k] = x
	}
	for k, x := range asMap(v) {
		out[k] = x
	}
	return out
}

// cmtyObject wraps a non-object response as {"body": v}.
func cmtyObject(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{"body": v}
}

// cmtyKeys returns a map's keys in sorted order.
func cmtyKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cmtyHeaderFileName extracts a file name from Content-Disposition.
func cmtyHeaderFileName(h http.Header) string {
	cd := h.Get("Content-Disposition")
	if cd == "" {
		return ""
	}
	if i := strings.Index(cd, "filename"); i >= 0 {
		s := cd[i+len("filename"):]
		s = strings.TrimPrefix(s, "*")
		s = strings.TrimPrefix(strings.TrimSpace(s), "=")
		if j := strings.Index(s, "''"); j >= 0 {
			s = s[j+2:]
		}
		s = strings.Trim(strings.SplitN(s, ";", 2)[0], `" `)
		if u, err := url.PathUnescape(s); err == nil {
			s = u
		}
		return s
	}
	return ""
}
