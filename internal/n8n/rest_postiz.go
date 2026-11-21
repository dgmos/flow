package n8n

import (
	"strings"
)

// Postiz (n8n-nodes-postiz): social media scheduling via the public API.

func init() {
	registerREST("n8n-nodes-postiz.postiz", &restService{
		defOps: map[string]string{"": "createPost"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("postizApi")
			if err != nil {
				return nil, err
			}
			host := firstNonEmpty(strings.TrimRight(credString(c, "host"), "/"), "https://api.postiz.com")
			return &restClient{base: ec.apiBase("postiz", host) + "/public/v1", headers: map[string]string{"Authorization": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"/createPost":      {method: "POST", path: "/posts", build: postizCreate},
			"/getPosts":        {path: "/posts", query: "startDate endDate customer"},
			"/getIntegrations": {path: "/integrations"},
			"/deletePost":      {method: "DELETE", path: "/posts/{postId}"},
			"/uploadFile":      {run: postizUpload},
			"/generateVideo": {method: "POST", path: "/generate-video", body: "videoType:type output", build: func(ec *execCtx, i int, r *restRequest) error {
				r.body["customParams"] = postizParams(ec, i, "customParameters", true)
				return nil
			}},
			"/videoFunction": {method: "POST", path: "/video/function", body: "functionName identifier", build: func(ec *execCtx, i int, r *restRequest) error {
				for k, v := range postizParams(ec, i, "additionalParameters", false) {
					r.body[k] = v
				}
				return nil
			}},
		},
	})
}

// postizParams reads a {parameter: [{key, value}]} collection; values are
// parsed as JSON when asked and possible.
func postizParams(ec *execCtx, i int, name string, parse bool) map[string]interface{} {
	out := map[string]interface{}{}
	v, _ := ec.param(i, name+".parameter", nil)
	for _, x := range asList(v) {
		m := asMap(x)
		val := m["value"]
		if s, ok := val.(string); ok && parse {
			var j interface{}
			if jsonUnmarshal([]byte(s), &j) == nil {
				val = j
			}
		}
		out[toString(m["key"])] = val
	}
	return out
}

func postizCreate(ec *execCtx, i int, r *restRequest) error {
	typ, _ := ec.str(i, "type", "now")
	short, _ := ec.boolean(i, "shortLink", false)
	date, _ := ec.param(i, "date", "")
	tags := []interface{}{}
	tv, _ := ec.param(i, "tags.tag", nil)
	for _, x := range asList(tv) {
		m := asMap(x)
		tags = append(tags, map[string]interface{}{"value": m["value"], "label": m["label"]})
	}
	posts := []interface{}{}
	pv, _ := ec.param(i, "posts.post", nil)
	for _, x := range asList(pv) {
		p := asMap(x)
		settings := map[string]interface{}{}
		for _, s := range asList(asMap(p["settings"])["setting"]) {
			sm := asMap(s)
			var val interface{}
			switch toString(sm["valueType"]) {
			case "number":
				val = sm["numberValue"]
			case "boolean":
				val = sm["booleanValue"]
			case "json":
				val = sm["jsonValue"]
				if s, ok := val.(string); ok {
					var j interface{}
					if jsonUnmarshal([]byte(s), &j) == nil {
						val = j
					}
				}
			default:
				val = sm["stringValue"]
			}
			settings[toString(sm["key"])] = val
		}
		values := []interface{}{}
		for _, c := range asList(asMap(p["value"])["contentItem"]) {
			cm := asMap(c)
			images := []interface{}{}
			for _, img := range asList(asMap(cm["image"])["imageItem"]) {
				im := asMap(img)
				images = append(images, map[string]interface{}{"id": im["id"], "path": im["path"]})
			}
			values = append(values, map[string]interface{}{"content": cm["content"], "id": firstNonEmpty(toString(pick(cm, "id")), ""), "image": images})
		}
		posts = append(posts, map[string]interface{}{"integration": map[string]interface{}{"id": p["integrationId"]}, "value": values,
			"group": toString(pick(p, "group")), "settings": settings})
	}
	r.body = map[string]interface{}{"type": typ, "shortLink": short, "date": date, "tags": tags, "posts": posts}
	return nil
}

func postizUpload(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	prop, _ := ec.str(i, "binaryProperty", "data")
	data, mt, name, err := binaryOf(it, prop)
	if err != nil {
		return nil, err
	}
	f := newCNForm()
	f.file("file", name, mt, data)
	v, err := c.do(ec, f.request("POST", "/upload"))
	if err != nil {
		return nil, err
	}
	return restItems(v), nil
}
