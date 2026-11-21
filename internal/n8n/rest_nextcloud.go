package n8n

import (
	"encoding/xml"
	"io"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Nextcloud (n8n-nodes-base.nextCloud): files and folders over WebDAV at the
// credential's webDavUrl (…/remote.php/webdav) and users and shares over OCS
// at the same host. OCS and PROPFIND answers are XML, converted like xml2js
// with explicitArray=false.

func init() {
	ops := map[string]*restOp{
		"file/download": {run: ncDownload},
		"file/upload":   {run: ncUpload},
		"folder/create": {run: ncDav("MKCOL", false)},
		"folder/list":   {run: ncList},
		"user/create":   {run: ncUser("create")},
		"user/delete":   {run: ncUser("delete")},
		"user/get":      {run: ncUser("get")},
		"user/getAll":   {run: ncUser("getAll")},
		"user/update":   {run: ncUser("update")},
		"file/copy":     {run: ncDav("COPY", true)},
		"file/delete":   {run: ncDav("DELETE", false)},
		"file/move":     {run: ncDav("MOVE", true)},
		"file/share":    {run: ncShare},
		"folder/copy":   {run: ncDav("COPY", true)},
		"folder/delete": {run: ncDav("DELETE", false)},
		"folder/move":   {run: ncDav("MOVE", true)},
		"folder/share":  {run: ncShare},
	}
	registerREST("nextCloud", &restService{
		defResource: "file",
		defOps:      map[string]string{"file": "upload", "folder": "create", "user": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, t, err := ec.restCred(i, "nextCloudApi", map[string]string{"accessToken": "nextCloudApi", "oAuth2": "nextCloudOAuth2Api"})
			if err != nil {
				return nil, err
			}
			dav := ec.apiBase("nextCloud", strings.TrimRight(credString(c, "webDavUrl"), "/"))
			if u, err := url.Parse(dav); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, permanentError{ec.node.Name + `: Invalid WebDAV URL in credentials: "` + dav + `". The URL must start with https:// or http://.`}
			}
			h := map[string]string{"Authorization": "Bearer " + accessToken(c)}
			if t == "nextCloudApi" {
				h = basicAuth(credString(c, "user"), credString(c, "password"))
			}
			return &restClient{base: dav, headers: h}, nil
		},
		ops: ops,
	})
}

var ncDavSuffix = regexp.MustCompile(`/remote\.php/webdav/?$`)

// ncPath escapes each segment of a WebDAV path.
func ncPath(p string) string {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for k, s := range segs {
		segs[k] = url.PathEscape(s)
	}
	return "/" + strings.Join(segs, "/")
}

// ncCall sends a request; ocs selects the OCS API (WebDAV suffix stripped).
func ncCall(ec *execCtx, c *restClient, method, p string, ocs bool, h map[string]string, body []byte, ct string) (rawResp, error) {
	u := c.base + p
	if ocs {
		h["OCS-APIRequest"] = "true"
		u = ncDavSuffix.ReplaceAllString(c.base, "") + "/" + p
	}
	hh := map[string]string{}
	for k, v := range c.headers {
		hh[k] = v
	}
	for k, v := range h {
		hh[k] = v
	}
	res, err := ec.doRequest(method, u, hh, body, ct)
	if err != nil {
		return res, err
	}
	if strings.Contains(string(res.body), "<b>Fatal error</b>") {
		return res, permanentError{ec.node.Name + ": NextCloud responded with a 'Fatal error'"}
	}
	return res, nil
}

// ncText outputs a plain response like n8n's wrapData: an empty body gives {}.
func ncText(res rawResp) []Item {
	if s := strings.TrimSpace(string(res.body)); s != "" {
		return []Item{{JSON: map[string]interface{}{"data": string(res.body)}}}
	}
	return []Item{{JSON: map[string]interface{}{}}}
}

func ncDav(method string, dest bool) func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		p, err := ec.str(i, "path", "")
		if err != nil {
			return nil, err
		}
		h := map[string]string{}
		if dest {
			to, err := ec.str(i, "toPath", "")
			if err != nil {
				return nil, err
			}
			h["Destination"] = c.base + ncPath(to)
		}
		res, err := ncCall(ec, c, method, ncPath(p), false, h, nil, "")
		if err != nil {
			return nil, err
		}
		return ncText(res), nil
	}
}

func ncUpload(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	p, err := ec.str(i, "path", "")
	if err != nil {
		return nil, err
	}
	var body []byte
	if bin, _ := ec.boolean(i, "binaryDataUpload", false); bin {
		prop, _ := ec.str(i, "binaryPropertyName", "data")
		data, _, _, err := binaryOf(it, prop)
		if err != nil {
			return nil, permanentError{ec.node.Name + ": " + err.Error()}
		}
		body = data
	} else {
		s, err := ec.str(i, "fileContent", "")
		if err != nil {
			return nil, err
		}
		body = []byte(s)
	}
	res, err := ncCall(ec, c, "PUT", ncPath(p), false, map[string]string{}, body, "")
	if err != nil {
		return nil, err
	}
	return ncText(res), nil
}

// ncDownload attaches the file to the input item.
func ncDownload(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	p, err := ec.str(i, "path", "")
	if err != nil {
		return nil, err
	}
	res, err := ncCall(ec, c, "GET", ncPath(p), false, map[string]string{}, nil, "")
	if err != nil {
		return nil, err
	}
	prop, _ := ec.str(i, "binaryPropertyName", "data")
	name := path.Base(p)
	out := Item{JSON: it.JSON, Binary: map[string]interface{}{}}
	for k, b := range it.Binary {
		out.Binary[k] = b
	}
	out.Binary[prop] = binaryData(res.body, glMime(name, res.body), name)
	return []Item{out}, nil
}

// ncList lists a folder (PROPFIND), skipping the folder itself.
func ncList(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	p, err := ec.str(i, "path", "")
	if err != nil {
		return nil, err
	}
	res, err := ncCall(ec, c, "PROPFIND", ncPath(p), false, map[string]string{}, nil, "")
	if err != nil {
		return nil, err
	}
	doc, err := ncXML(res.body)
	if err != nil {
		return nil, permanentError{ec.node.Name + ": " + err.Error()}
	}
	resp, ok := asMap(doc["d:multistatus"])["d:response"].([]interface{})
	if !ok {
		return nil, nil
	}
	names := [][2]string{{"d:getlastmodified", "lastModified"}, {"d:getcontentlength", "contentLength"}, {"d:getcontenttype", "contentType"}}
	var out []Item
	for _, r := range resp[1:] {
		m := asMap(r)
		item := map[string]interface{}{}
		href := toString(m["d:href"])
		if len(href) > 19 {
			item["path"] = href[19:]
		} else {
			item["path"] = ""
		}
		ps := m["d:propstat"]
		if l, ok := ps.([]interface{}); ok && len(l) > 0 {
			ps = l[0]
		}
		props := asMap(asMap(ps)["d:prop"])
		for _, n := range names {
			if v, ok := props[n[0]]; ok {
				item[n[1]] = v
			}
		}
		if props["d:resourcetype"] == "" {
			item["type"] = "file"
		} else {
			item["type"] = "folder"
		}
		if et := toString(props["d:getetag"]); len(et) >= 2 {
			item["eTag"] = et[1 : len(et)-1]
		}
		out = append(out, Item{JSON: item})
	}
	return out, nil
}

func ncShare(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	a := &cmtyArgs{ec: ec, i: i}
	p := a.str("path", "")
	st := int(a.num("shareType", 0))
	if a.err != nil {
		return nil, a.err
	}
	if st == 200 {
		body := `<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop><oc:fileid/></d:prop>
</d:propfind>`
		res, err := ncCall(ec, c, "PROPFIND", ncPath(p), false, map[string]string{"Depth": "0"}, []byte(body), "application/xml")
		if err != nil {
			return nil, err
		}
		doc, err := ncXML(res.body)
		if err != nil {
			return nil, permanentError{ec.node.Name + ": " + err.Error()}
		}
		r := asMap(doc["d:multistatus"])["d:response"]
		if l, ok := r.([]interface{}); ok && len(l) > 0 {
			r = l[0]
		}
		ps := asMap(r)["d:propstat"]
		if l, ok := ps.([]interface{}); ok && len(l) > 0 {
			ps = l[0]
		}
		id, _ := asMap(asMap(ps)["d:prop"])["oc:fileid"].(string)
		if id == "" {
			return nil, permanentError{ec.node.Name + ": Could not retrieve internal link: oc:fileid not found in PROPFIND response"}
		}
		base := ncDavSuffix.ReplaceAllString(c.base, "")
		if base == c.base {
			return nil, permanentError{ec.node.Name + ": WebDAV URL must end with /remote.php/webdav for generating an internal link."}
		}
		return []Item{{JSON: map[string]interface{}{"link": base + "/f/" + id}}}, nil
	}
	f := url.Values{}
	for k, v := range asMap(a.get("options", nil)) {
		f.Set(k, toString(v))
	}
	f.Set("path", p)
	f.Set("shareType", strconv.Itoa(st))
	if with := map[int]string{0: "user", 7: "circleId", 4: "email", 1: "groupId"}[st]; with != "" {
		f.Set("shareWith", a.str(with, ""))
	}
	if a.err != nil {
		return nil, a.err
	}
	res, err := ncCall(ec, c, "POST", "ocs/v2.php/apps/files_sharing/api/v1/shares", true, map[string]string{},
		[]byte(f.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return nil, err
	}
	ocs, err := ncOCS(ec, res.body)
	if err != nil {
		return nil, err
	}
	return []Item{{JSON: asMap(ocs["data"])}}, nil
}

// ncOCS parses an OCS answer and fails unless meta.status is "ok".
func ncOCS(ec *execCtx, b []byte) (map[string]interface{}, error) {
	doc, err := ncXML(b)
	if err != nil {
		return nil, permanentError{ec.node.Name + ": " + err.Error()}
	}
	ocs := asMap(doc["ocs"])
	meta := asMap(ocs["meta"])
	if toString(meta["status"]) != "ok" {
		return nil, permanentError{ec.node.Name + ": " + firstNonEmpty(toString(meta["message"]), toString(meta["status"]))}
	}
	return ocs, nil
}

func ncUser(op string) func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		a := &cmtyArgs{ec: ec, i: i}
		form := "application/x-www-form-urlencoded"
		method, p, body := "GET", "ocs/v1.php/cloud/users", ""
		if op != "create" && op != "getAll" {
			p += "/" + url.PathEscape(a.str("userId", ""))
		}
		switch op {
		case "create":
			// n8n concatenates the values unescaped.
			method = "POST"
			body = "userid=" + a.str("userId", "") + "&email=" + a.str("email", "")
			if d := a.str("additionalFields.displayName", ""); d != "" {
				body += "&displayName=" + d
			}
		case "delete":
			method = "DELETE"
		case "update":
			method = "PUT"
			f := asMap(a.get("updateFields.field", nil))
			var parts []string
			for _, k := range []string{"key", "value"} {
				if v, ok := f[k]; ok {
					parts = append(parts, k+"="+toString(v))
				}
			}
			body = strings.Join(parts, "&")
		case "getAll":
			q := url.Values{}
			for k, v := range asMap(a.get("options", nil)) {
				if !restEmpty(v) {
					q.Set(k, toString(v))
				}
			}
			if !a.bool("returnAll", false) {
				q.Set("limit", strconv.Itoa(int(a.num("limit", 50))))
			}
			if len(q) > 0 {
				p += "?" + q.Encode()
			}
		}
		if a.err != nil {
			return nil, a.err
		}
		var raw []byte
		if body != "" {
			raw = []byte(body)
		}
		res, err := ncCall(ec, c, method, p, true, map[string]string{"Content-Type": form}, raw, form)
		if err != nil {
			return nil, err
		}
		ocs, err := ncOCS(ec, res.body)
		if err != nil {
			return nil, err
		}
		switch op {
		case "delete", "update":
			return []Item{{JSON: asMap(ocs["meta"])}}, nil
		case "getAll":
			var out []Item
			if el := asMap(asMap(ocs["data"])["users"])["element"]; el != nil {
				for _, u := range asListOrOne(el) {
					out = append(out, Item{JSON: map[string]interface{}{"id": u}})
				}
			}
			return out, nil
		}
		return []Item{{JSON: asMap(ocs["data"])}}, nil
	}
}

// ncXML converts XML like xml2js (explicitArray=false): qualified names keep
// their prefix, text-only elements become strings, attributes go under "$".
func ncXML(b []byte) (map[string]interface{}, error) {
	type node struct {
		name     string
		attrs    map[string]interface{}
		text     strings.Builder
		children []*node
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	dec.Strict = false
	root := &node{}
	stack := []*node{root}
	qname := func(n xml.Name) string {
		if n.Space != "" {
			return n.Space + ":" + n.Local
		}
		return n.Local
	}
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		cur := stack[len(stack)-1]
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: qname(t.Name), attrs: map[string]interface{}{}}
			for _, at := range t.Attr {
				n.attrs[qname(at.Name)] = at.Value
			}
			cur.children = append(cur.children, n)
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			cur.text.Write(t)
		}
	}
	var conv func(n *node) interface{}
	conv = func(n *node) interface{} {
		text := n.text.String()
		if strings.TrimSpace(text) == "" {
			text = ""
		}
		if len(n.children) == 0 && len(n.attrs) == 0 {
			return text
		}
		obj := map[string]interface{}{}
		if len(n.attrs) > 0 {
			obj["$"] = n.attrs
		}
		if text != "" {
			obj["_"] = text
		}
		for _, c := range n.children {
			v := conv(c)
			if prev, ok := obj[c.name]; ok {
				if l, ok := prev.([]interface{}); ok {
					obj[c.name] = append(l, v)
				} else {
					obj[c.name] = []interface{}{prev, v}
				}
			} else {
				obj[c.name] = v
			}
		}
		return obj
	}
	if len(root.children) == 0 {
		return map[string]interface{}{}, nil
	}
	top := root.children[0]
	return map[string]interface{}{top.name: conv(top)}, nil
}
