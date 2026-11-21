package n8n

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
)

// Helpers shared by the community-node REST services (ElevenLabs, PDF.co,
// Upload-Post, VLM Run, …): multipart bodies, raw calls and binary output.

// cnForm builds a multipart/form-data body with ordered parts.
type cnForm struct {
	buf bytes.Buffer
	w   *multipart.Writer
}

func newCNForm() *cnForm {
	f := &cnForm{}
	f.w = multipart.NewWriter(&f.buf)
	return f
}

func (f *cnForm) field(k string, v interface{}) {
	_ = f.w.WriteField(k, toString(v))
}

func (f *cnForm) file(field, name, mt string, data []byte) {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, field, firstNonEmpty(name, "file")))
	h.Set("Content-Type", firstNonEmpty(mt, "application/octet-stream"))
	p, _ := f.w.CreatePart(h)
	_, _ = p.Write(data)
}

// request finishes the body as a restRequest.
func (f *cnForm) request(method, path string) *restRequest {
	_ = f.w.Close()
	return &restRequest{method: method, path: path, query: url.Values{}, raw: f.buf.Bytes(), contentType: f.w.FormDataContentType(), headers: map[string]string{}}
}

// cnRaw sends a request with the client's auth and returns the raw response
// (for binary downloads and full-response outputs).
func cnRaw(ec *execCtx, c *restClient, r *restRequest) (rawResp, error) {
	u := r.path
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = c.base + u
	}
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
	body, ct := r.raw, r.contentType
	if body == nil && r.body != nil && r.method != http.MethodGet {
		body, ct = []byte(mustJSON(r.body)), "application/json"
	}
	h := map[string]string{}
	for k, v := range c.headers {
		h[k] = v
	}
	for k, v := range r.headers {
		h[k] = v
	}
	return ec.doRequest(r.method, u, h, body, ct)
}

// cnHeaders returns response headers as n8n exposes them (lower-case keys).
func cnHeaders(h http.Header) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range h {
		out[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	return out
}

// cnBinaryItem returns an item with JSON j and the data as binary property prop.
func cnBinaryItem(j map[string]interface{}, prop string, data []byte, mt, name string) Item {
	if j == nil {
		j = map[string]interface{}{}
	}
	return Item{JSON: j, Binary: map[string]interface{}{firstNonEmpty(prop, "data"): binaryData(data, mt, name)}}
}

// cnDecode decodes a raw JSON response (a string when it is not JSON).
func cnDecode(r rawResp) interface{} {
	b := bytes.TrimSpace(r.body)
	if len(b) == 0 {
		return nil
	}
	var v interface{}
	if jsonUnmarshal(b, &v) != nil {
		return string(r.body)
	}
	return v
}

// cnFullResponse mirrors n8n's returnFullResponse output.
func cnFullResponse(r rawResp) map[string]interface{} {
	return map[string]interface{}{"body": cnDecode(r), "headers": cnHeaders(r.header), "statusCode": r.status, "statusMessage": http.StatusText(r.status)}
}
