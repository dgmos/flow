package n8n

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
)

// Google Cloud Storage (n8n-nodes-base.googleCloudStorage), JSON API v1.
// n8n's node is declarative; the ops below mirror its routing.

func init() {
	googleScopes["cloudStorage"] = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/devstorage.full_control"
	registerREST("googleCloudStorage", &restService{
		defResource: "bucket",
		defOps:      map[string]string{"bucket": "getAll", "object": "getAll"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			h, err := ec.googleAuth("cloudStorage", "googleCloudStorageOAuth2Api")
			if err != nil {
				return nil, err
			}
			gcsDefaults(ec)
			return &restClient{base: ec.apiBase("googleCloudStorage", "https://storage.googleapis.com"), headers: h}, nil
		},
		ops: map[string]*restOp{
			"bucket/create": {method: "POST", path: "/storage/v1/b/", body: "bucketName:name",
				query: "projectId:project projectRLC:project:rl projection @createAcl predefinedAcl predefinedDefaultObjectAcl:predefinedObjectAcl",
				build: gcsJSONBody("createBody", "acl billing cors customPlacementConfig dataLocations defaultObjectAcl encryption iamConfiguration labels lifecycle logging retentionPolicy versioning website")},
			"bucket/delete": {method: "DELETE", path: "/storage/v1/b/{bucketName}", query: "@getFilters *"},
			"bucket/get":    {path: "/storage/v1/b/{bucketName}", query: "projection @getFilters *"},
			"bucket/getAll": {run: gcsList("/storage/v1/b/", "projectId:project projectRLC:project:rl prefix projection", "")},
			"bucket/update": {method: "PATCH", path: "/storage/v1/b/{bucketName}",
				query: "projection @createAcl predefinedAcl predefinedDefaultObjectAcl:predefinedObjectAcl @getFilters *",
				build: gcsJSONBody("createBody", "acl billing cors customPlacementConfig dataLocations defaultObjectAcl encryption iamConfiguration labels lifecycle logging retentionPolicy versioning website")},

			"object/create": {method: "POST", path: "/upload/storage/v1/b/{bucketName}/o/", query: "objectName:name =uploadType:multipart updateProjection:projection @createQuery *",
				build: gcsUpload},
			"object/delete": {method: "DELETE", path: "/storage/v1/b/{bucketName}/o/{objectName}", query: "@getParameters *", build: gcsEncryption},
			"object/get":    {run: gcsObjectGet},
			"object/getAll": {run: gcsList("/storage/v1/b/{bucketName}/o/", "projection @listFilters *", "maxResults")},
			"object/update": {method: "PATCH", path: "/storage/v1/b/{bucketName}/o/{objectName}", query: "updateProjection:projection @metagenAndAclQuery *",
				build: func(ec *execCtx, i int, r *restRequest) error {
					if err := gcsJSONBody("updateData", "acl metadata")(ec, i, r); err != nil {
						return err
					}
					return gcsEncryption(ec, i, r)
				}},
		},
	})
}

// gcsDefaults fills the parameter defaults n8n's routing always sends.
func gcsDefaults(ec *execCtx) {
	p := map[string]interface{}{"projection": "noAcl", "updateProjection": "full", "alt": "json"}
	for k, v := range ec.params {
		p[k] = v
	}
	ec.params = p
}

// gcsJSONBody copies a collection into the body, parsing the fields n8n
// takes as JSON strings (kept as strings when they do not parse).
func gcsJSONBody(coll, jsonFields string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		for k, v := range fieldsOf(ec, i, coll) {
			if s, ok := v.(string); ok && strings.Contains(" "+jsonFields+" ", " "+k+" ") {
				var x interface{}
				if json.Unmarshal([]byte(s), &x) == nil {
					v = x
				}
			}
			r.body[k] = v
		}
		return nil
	}
}

// gcsEncryption adds the customer-supplied encryption key headers.
func gcsEncryption(ec *execCtx, i int, r *restRequest) error {
	for k, v := range fieldsOf(ec, i, "encryptionHeaders") {
		if !restEmpty(v) {
			r.headers[k] = toString(v)
		}
	}
	return nil
}

// gcsUpload builds the multipart/related upload of object/create.
func gcsUpload(ec *execCtx, i int, r *restRequest) error {
	if err := gcsEncryption(ec, i, r); err != nil {
		return err
	}
	name, _ := ec.str(i, "objectName", "")
	meta := map[string]interface{}{"name": name}
	tmp := &restRequest{body: map[string]interface{}{}}
	if err := gcsJSONBody("createData", "acl metadata")(ec, i, tmp); err != nil {
		return err
	}
	for k, v := range tmp.body {
		meta[k] = v
	}
	var content []byte
	ct := "text/plain"
	if bin, _ := ec.boolean(i, "createFromBinary", true); bin {
		prop, _ := ec.str(i, "createBinaryPropertyName", "data")
		var it Item
		if items := ec.items(); i < len(items) {
			it = items[i]
		}
		data, mt, _, err := binaryOf(it, prop)
		if err != nil {
			return permanentError{ec.node.Name + ": " + err.Error()}
		}
		content, ct = data, mt
	} else {
		s, _ := ec.str(i, "createContent", "")
		content = []byte(s)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"application/json"}})
	_ = json.NewEncoder(part).Encode(meta)
	part, _ = w.CreatePart(textproto.MIMEHeader{"Content-Type": {ct}})
	_, _ = part.Write(content)
	_ = w.Close()
	r.raw, r.contentType = buf.Bytes(), "multipart/related; boundary="+w.Boundary()
	return nil
}

// gcsList returns the first page (sizeParam = the page size parameter, or
// the projection's maximum for buckets), or all pages with returnAll.
func gcsList(path, query, sizeParam string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		r, err := (&restOp{path: path, query: query}).request(ec, i)
		if err != nil {
			return nil, err
		}
		all, _ := ec.boolean(i, "returnAll", false)
		if !all {
			if sizeParam != "" {
				n, _ := ec.num(i, sizeParam, 100)
				r.query.Set("maxResults", strconv.Itoa(int(n)))
			} else if p, _ := ec.str(i, "projection", "noAcl"); p == "noAcl" {
				r.query.Set("maxResults", "1000")
			} else {
				r.query.Set("maxResults", "200")
			}
		}
		var out []interface{}
		for n := 0; n < 1000; n++ {
			v, err := c.do(ec, r)
			if err != nil {
				return nil, err
			}
			m := asMap(v)
			out = append(out, asList(m["items"])...)
			tok := toString(m["nextPageToken"])
			if !all || tok == "" || tok == "<nil>" {
				break
			}
			r.query.Set("pageToken", tok)
		}
		return restItems(out), nil
	}
}

func gcsObjectGet(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	r, err := (&restOp{path: "/storage/v1/b/{bucketName}/o/{objectName}", query: "alt projection @getParameters *", build: gcsEncryption}).request(ec, i)
	if err != nil {
		return nil, err
	}
	if alt, _ := ec.str(i, "alt", "json"); alt != "media" {
		v, err := c.do(ec, r)
		if err != nil {
			return nil, err
		}
		return restItems(v), nil
	}
	h := map[string]string{}
	for k, v := range c.headers {
		h[k] = v
	}
	for k, v := range r.headers {
		h[k] = v
	}
	u := c.base + r.path
	if len(r.query) > 0 {
		u += "?" + r.query.Encode()
	}
	res, err := ec.doRequest("GET", u, h, nil, "")
	if err != nil {
		return nil, err
	}
	prop, _ := ec.str(i, "binaryPropertyName", "data")
	name, _ := ec.str(i, "objectName", "")
	mt := res.contentType
	if mt == "" {
		mt = "application/octet-stream"
	}
	if k := strings.LastIndex(name, "/"); k >= 0 {
		name = name[k+1:]
	}
	return []Item{{JSON: map[string]interface{}{}, Binary: map[string]interface{}{firstNonEmpty(prop, "data"): binaryData(res.body, mt, name)}}}, nil
}
