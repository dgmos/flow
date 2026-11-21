package n8n

import (
	"encoding/base64"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// GitLab (n8n-nodes-base.gitlab), versions 1 and 1.1: issues, releases,
// repositories, files and users at {server}/api/v4. Projects are addressed
// as the URL-encoded "owner/repository"; version 1 percent-decodes path
// values first (they were sent unescaped before).

func init() {
	registerREST("gitlab", &restService{
		defResource: "issue",
		defOps:      map[string]string{"issue": "create", "repository": "getIssues", "user": "getRepositories", "release": "create", "file": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, t, err := ec.restCred(i, "gitlabApi", map[string]string{"accessToken": "gitlabApi", "oAuth2": "gitlabOAuth2Api"})
			if err != nil {
				return nil, err
			}
			restDefaultLimit(ec, 20)
			server := strings.TrimRight(firstNonEmpty(credString(c, "server"), "https://gitlab.com"), "/")
			h := map[string]string{"Private-Token": credString(c, "accessToken")}
			if t == "gitlabOAuth2Api" {
				h = map[string]string{"Authorization": "Bearer " + accessToken(c)}
			}
			return &restClient{base: ec.apiBase("gitlab", server+"/api/v4"), headers: h}, nil
		},
		pager: glPager,
		ops: map[string]*restOp{
			"issue/create": {method: "POST", build: glProject("/issues", func(ec *execCtx, i int, r *restRequest) error {
				a := &cmtyArgs{ec: ec, i: i}
				r.body["title"] = a.str("title", "")
				r.body["description"] = a.str("body", "")
				r.body["due_date"] = a.str("due_date", "")
				r.body["labels"] = glJoin(a.get("labels", nil), "label")
				r.body["assignee_ids"] = glPluck(a.get("assignee_ids", nil), "assignee")
				return a.err
			})},
			"issue/createComment": {method: "POST", body: "body", build: glProject("/issues/{issueNumber}/notes", nil)},
			"issue/edit": {method: "PUT", body: "@editFields *", build: glProject("/issues/{issueNumber}", func(ec *execCtx, i int, r *restRequest) error {
				if v, ok := r.body["labels"]; ok {
					r.body["labels"] = glJoin(v, "label")
				}
				if v, ok := r.body["assignee_ids"]; ok {
					r.body["assignee_ids"] = glPluck(v, "assignee")
				}
				if s, ok := r.body["state"].(string); ok {
					if ev := map[string]string{"closed": "close", "open": "reopen"}[s]; ev != "" {
						r.body["state_event"] = ev
						delete(r.body, "state")
					}
				}
				return nil
			})},
			"issue/get":  {build: glProject("/issues/{issueNumber}", nil)},
			"issue/lock": {run: glLock},

			"release/create": {method: "POST", body: "releaseTag:tag_name @additionalFields *", build: glProject("/releases", nil)},
			"release/delete": {method: "DELETE", build: glRelease("/releases/{tag_name}", nil)},
			"release/get":    {build: glRelease("/releases/{tag_name}", nil)},
			"release/getAll": {list: true, sizeParam: "per_page", query: "@additionalFields *", build: glRelease("/releases", glPageAll)},
			"release/update": {method: "PUT", body: "@additionalFields *", build: glRelease("/releases/{tag_name}", func(ec *execCtx, i int, r *restRequest) error {
				if m, ok := r.body["milestones"].(string); ok {
					var l []interface{}
					for _, s := range strings.Split(m, ",") {
						l = append(l, s)
					}
					r.body["milestones"] = l
				}
				return nil
			})},

			"repository/get":       {build: glProject("", nil)},
			"repository/getIssues": {list: true, sizeParam: "per_page", query: "@getRepositoryIssuesFilters *", build: glProject("/issues", glPageAll)},
			"user/getRepositories": {build: func(ec *execCtx, i int, r *restRequest) error {
				owner, err := glValue(ec, i, "owner")
				r.path = "/users/" + url.PathEscape(owner) + "/projects"
				return err
			}},

			"file/create": {method: "POST", build: glProject("/repository/files/{filePath}", glFileBody(true))},
			"file/edit":   {method: "PUT", build: glProject("/repository/files/{filePath}", glFileBody(true))},
			"file/delete": {method: "DELETE", build: glProject("/repository/files/{filePath}", glFileBody(false))},
			"file/get":    {run: glFileGet},
			"file/list": {list: true, sizeParam: "per_page", query: "@additionalParameters *", build: glProject("/repository/tree", func(ec *execCtx, i int, r *restRequest) error {
				if all, _ := ec.boolean(i, "returnAll", false); !all {
					p, _ := ec.num(i, "page", 1)
					r.query.Set("page", strconv.Itoa(int(p)))
				}
				if p, _ := ec.str(i, "filePath", ""); p != "" {
					r.query.Set("path", p)
				}
				return glPageAll(ec, i, r)
			})},
		},
	})
}

var glEncodedRun = regexp.MustCompile(`(?:%[0-9A-Fa-f]{2})+`)

// glValue reads a path value; version 1 decodes percent-encoded runs.
func glValue(ec *execCtx, i int, name string) (string, error) {
	v, err := ec.str(i, name, "")
	if err != nil || ec.version >= 1.1 {
		return v, err
	}
	return glEncodedRun.ReplaceAllStringFunc(v, func(s string) string {
		if d, err := url.PathUnescape(s); err == nil {
			return d
		}
		return s
	}), nil
}

// glSegment path-escapes one segment the way encodeURIComponent does
// ("/" included).
func glSegment(s string) string {
	return strings.ReplaceAll(url.PathEscape(s), "/", "%2F")
}

// glSub replaces {param} in a path suffix with escaped (decoded in v1) values.
func glSub(ec *execCtx, i int, p string) (string, error) {
	var err error
	out := restPathRe.ReplaceAllStringFunc(p, func(m string) string {
		v, e := glValue(ec, i, restPathRe.FindStringSubmatch(m)[2])
		if e != nil && err == nil {
			err = e
		}
		return glSegment(v)
	})
	return out, err
}

// glProject prefixes the path with /projects/{owner/repository}.
func glProject(suffix string, then func(ec *execCtx, i int, r *restRequest) error) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		owner, err := glValue(ec, i, "owner")
		if err != nil {
			return err
		}
		repo, err := glValue(ec, i, "repository")
		if err != nil {
			return err
		}
		s, err := glSub(ec, i, suffix)
		if err != nil {
			return err
		}
		r.path = "/projects/" + glSegment(owner+"/"+repo) + s
		if then != nil {
			return then(ec, i, r)
		}
		return nil
	}
}

// glRelease addresses /projects/{projectId}.
func glRelease(suffix string, then func(ec *execCtx, i int, r *restRequest) error) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		s, err := glSub(ec, i, "/projects/{projectId}"+suffix)
		if err != nil {
			return err
		}
		r.path = s
		if then != nil {
			return then(ec, i, r)
		}
		return nil
	}
}

// glPageAll requests pages of 100 from page 1 when returnAll is on.
func glPageAll(ec *execCtx, i int, r *restRequest) error {
	if all, _ := ec.boolean(i, "returnAll", false); all {
		r.query.Set("per_page", "100")
		r.query.Set("page", "1")
	}
	return nil
}

// glPager follows the next page while the Link header has rel="next".
func glPager(p *restPage, _ interface{}, hdr http.Header) bool {
	if !strings.Contains(hdr.Get("Link"), "next") {
		return false
	}
	n, err := strconv.Atoi(p.query.Get("page"))
	if err != nil || n < 1 {
		n = 1
	}
	p.query.Set("page", strconv.Itoa(n+1))
	return true
}

// glJoin joins the key of each {key: v} entry with commas.
func glJoin(v interface{}, key string) string {
	var out []string
	for _, x := range asListOrOne(v) {
		if s := toString(asMap(x)[key]); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ",")
}

func glPluck(v interface{}, key string) []interface{} {
	out := []interface{}{}
	for _, x := range asListOrOne(v) {
		if m := asMap(x); m[key] != nil && m[key] != "" {
			out = append(out, m[key])
		}
	}
	return out
}

// glFileBody builds a commit body; withContent adds the file content.
func glFileBody(withContent bool) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		a := &cmtyArgs{ec: ec, i: i}
		r.body["branch"] = a.str("branch", "")
		r.body["commit_message"] = a.str("commitMessage", "")
		ap := asMap(a.get("additionalParameters", nil))
		author := asMap(ap["author"])
		if n := toString(author["name"]); n != "" {
			r.body["author_name"] = n
		}
		if e := toString(author["email"]); e != "" {
			r.body["author_email"] = e
		}
		if !withContent {
			return a.err
		}
		if s := toString(asMap(ap["branchStart"])["branchStart"]); s != "" {
			r.body["start_branch"] = s
		}
		if a.bool("binaryData", false) {
			var it Item
			if items := ec.items(); i < len(items) {
				it = items[i]
			}
			data, _, _, err := binaryOf(it, a.str("binaryPropertyName", "data"))
			if err != nil {
				return permanentError{ec.node.Name + ": " + err.Error()}
			}
			r.body["content"] = base64.StdEncoding.EncodeToString(data)
			r.body["encoding"] = "base64"
		} else if toString(ap["encoding"]) == "base64" {
			r.body["content"] = base64.StdEncoding.EncodeToString([]byte(a.str("fileContent", "")))
		} else {
			r.body["content"] = a.str("fileContent", "")
		}
		return a.err
	}
}

// glFileGet returns the file, by default as a binary property on the input item.
func glFileGet(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	r := &restRequest{method: "GET", query: url.Values{}, body: map[string]interface{}{}}
	if err := glProject("/repository/files/{filePath}", nil)(ec, i, r); err != nil {
		return nil, err
	}
	ref, _ := ec.str(i, "additionalParameters.reference", "")
	r.query.Set("ref", firstNonEmpty(ref, "master"))
	v, err := c.do(ec, r)
	if err != nil {
		return nil, err
	}
	if asBin, _ := ec.boolean(i, "asBinaryProperty", true); !asBin {
		return restItems(v), nil
	}
	if l, ok := v.([]interface{}); ok && len(l) > 1 {
		return nil, permanentError{ec.node.Name + ": File Path is a folder, not a file."}
	}
	m := asMap(v)
	data, err := base64.StdEncoding.DecodeString(toString(m["content"]))
	if err != nil {
		return nil, permanentError{ec.node.Name + ": file content is not valid base64"}
	}
	prop, _ := ec.str(i, "binaryPropertyName", "data")
	name := toString(m["file_name"])
	if name == "" || name == "<nil>" {
		name = toString(m["file_path"])
	}
	out := Item{JSON: it.JSON, Binary: map[string]interface{}{}}
	for k, b := range it.Binary {
		out.Binary[k] = b
	}
	out.Binary[prop] = binaryData(data, glMime(name, data), name)
	return []Item{out}, nil
}

// glLock locks an issue's discussion; like n8n it passes the input item through.
func glLock(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	r := &restRequest{method: "PUT", query: url.Values{}, body: map[string]interface{}{"discussion_locked": true}}
	if err := glProject("/issues/{issueNumber}", nil)(ec, i, r); err != nil {
		return nil, err
	}
	if _, err := c.do(ec, r); err != nil {
		return nil, err
	}
	return []Item{it}, nil
}

// glMime guesses a MIME type from a file name, then from the content.
func glMime(name string, data []byte) string {
	if mt := mime.TypeByExtension(path.Ext(name)); mt != "" {
		return mt
	}
	return sniffMime(data)
}
