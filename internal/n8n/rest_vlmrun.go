package n8n

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// VLM Run (@vlm-run/n8n-nodes-vlmrun): document/audio/video/image
// extraction, agents, chat completions and artifacts. Two bases: the API
// (credential apiBaseUrl) and the agent API (agentBaseUrl).

func init() {
	run := func(fn func(ec *execCtx, i int, it Item, v *vlmClient) (interface{}, error)) *restOp {
		return &restOp{run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
			cred, err := ec.credential("vlmRunApi")
			if err != nil {
				return nil, err
			}
			agent := strings.TrimRight(firstNonEmpty(strings.TrimSpace(credString(cred, "agentBaseUrl")), "https://agent.vlm.run/v1"), "/")
			ac := *c
			ac.base = ec.apiBase("vlmRunAgent", agent)
			v, err := fn(ec, i, it, &vlmClient{api: c, agent: &ac})
			if err != nil {
				return nil, err
			}
			if item, ok := v.(Item); ok {
				return []Item{item}, nil
			}
			return []Item{{JSON: asMap(v)}}, nil
		}}
	}
	registerREST("@vlm-run/n8n-nodes-vlmrun.vlmRun", &restService{
		defOps: map[string]string{"": "document"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("vlmRunApi")
			if err != nil {
				return nil, err
			}
			base := strings.TrimRight(firstNonEmpty(strings.TrimSpace(credString(c, "apiBaseUrl")), "https://api.vlm.run/v1"), "/")
			return &restClient{base: ec.apiBase("vlmRun", base), headers: map[string]string{"Authorization": "Bearer " + credString(c, "apiKey"), "X-Client-Id": "n8n-vlmrun"}}, nil
		},
		ops: map[string]*restOp{
			"/document":       run(vlmGenerate("document")),
			"/audio":          run(vlmGenerate("audio")),
			"/video":          run(vlmGenerate("video")),
			"/image":          run(vlmImage),
			"/file":           run(vlmFile),
			"/executeAgent":   run(vlmAgent),
			"/chatCompletion": run(vlmChat),
			"/artifacts":      run(vlmArtifact),
		},
	})
}

type vlmClient struct{ api, agent *restClient }

func vlmFileOf(ec *execCtx, i int, it Item) ([]byte, string, string, error) {
	prop, _ := ec.str(i, "file", "data")
	data, mt, name, err := binaryOf(it, prop)
	if err != nil {
		return nil, "", "", err
	}
	if b := asMap(it.Binary[firstNonEmpty(prop, "data")]); toString(b["fileName"]) == "" {
		name = "unnamed_file"
	}
	return data, mt, name, nil
}

// vlmGenerate uploads the file and requests a prediction for its kind.
func vlmGenerate(kind string) func(ec *execCtx, i int, it Item, v *vlmClient) (interface{}, error) {
	return func(ec *execCtx, i int, it Item, v *vlmClient) (interface{}, error) {
		data, mt, name, err := vlmFileOf(ec, i, it)
		if err != nil {
			return nil, err
		}
		f := newCNForm()
		f.file("file", name, mt, data)
		up, err := v.api.do(ec, f.request("POST", "/files"))
		if err != nil {
			return nil, err
		}
		domain, _ := ec.str(0, "domain", "")
		batch, _ := ec.boolean(0, "processAsynchronously", false)
		body := map[string]interface{}{"model": "vlm-1", "domain": domain, "file_id": asMap(up)["id"], "batch": batch}
		if batch {
			if cb, _ := ec.str(0, "callbackUrl", ""); cb != "" {
				body["callback_url"] = cb
			}
		}
		return v.api.do(ec, &restRequest{method: "POST", path: "/" + kind + "/generate", body: body})
	}
}

func vlmImage(ec *execCtx, i int, it Item, v *vlmClient) (interface{}, error) {
	prop, _ := ec.str(i, "file", "data")
	b, ok := it.Binary[firstNonEmpty(prop, "data")].(map[string]interface{})
	if !ok {
		return nil, permanentError{fmt.Sprintf("%s: no binary data found for property %q", ec.node.Name, prop)}
	}
	domain, _ := ec.str(0, "domain", "")
	body := map[string]interface{}{"images": []interface{}{"data:" + toString(b["mimeType"]) + ";base64," + toString(b["data"])}, "model": "vlm-1", "domain": domain}
	return v.api.do(ec, &restRequest{method: "POST", path: "/image/generate", body: body})
}

// vlmPresigned uploads a file through a presigned URL of the agent API.
func vlmPresigned(ec *execCtx, v *vlmClient, name string, data []byte, mt string) (map[string]interface{}, error) {
	r, err := v.agent.do(ec, &restRequest{method: "POST", path: "/files/presigned-url", body: map[string]interface{}{"filename": name, "purpose": "assistants", "expiration": 86400}})
	if err != nil {
		return nil, err
	}
	res := asMap(r)
	ct := firstNonEmpty(toString(pick(res, "content_type")), firstNonEmpty(mt, "application/octet-stream"))
	if _, err := ec.doRequest("PUT", toString(res["url"]), nil, data, ct); err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	for k, x := range res {
		out[k] = x
	}
	out["url"] = res["public_url"]
	return out, nil
}

func vlmFile(ec *execCtx, i int, it Item, v *vlmClient) (interface{}, error) {
	if op, _ := ec.str(0, "fileOperation", "list"); op == "list" {
		files, err := v.api.do(ec, &restRequest{method: "GET", path: "/files", query: url.Values{"limit": {"100"}}})
		return map[string]interface{}{"files": files}, err
	}
	data, mt, name, err := vlmFileOf(ec, i, it)
	if err != nil {
		return nil, err
	}
	return vlmPresigned(ec, v, name, data, mt)
}

func vlmAgent(ec *execCtx, i int, it Item, v *vlmClient) (interface{}, error) {
	prompt, _ := ec.str(0, "agentPrompt", "")
	cb, _ := ec.str(0, "agentCallbackUrl", "")
	var inputs map[string]interface{}
	if multi, _ := ec.boolean(0, "multipleFiles", false); multi {
		m, _ := ec.param(0, "fileMappings.mapping", nil)
		urls := map[string]interface{}{}
		for _, x := range asList(m) {
			p := asMap(x)
			k, u := toString(p["key"]), toString(p["url"])
			if k == "" || u == "" {
				return nil, permanentError{ec.node.Name + ": both key and URL are required for each file mapping"}
			}
			urls[k] = u
		}
		if len(urls) == 0 {
			return nil, permanentError{ec.node.Name + ": at least one file mapping is required when multiple files is enabled"}
		}
		inputs = map[string]interface{}{"urls": urls}
	} else {
		data, mt, name, err := vlmFileOf(ec, i, it)
		if err != nil {
			return nil, err
		}
		up, err := vlmPresigned(ec, v, name, data, mt)
		if err != nil {
			return nil, err
		}
		if toString(up["public_url"]) == "" {
			return nil, permanentError{ec.node.Name + ": failed to obtain uploaded file URL"}
		}
		inputs = map[string]interface{}{"url": up["public_url"]}
	}
	body := map[string]interface{}{"config": map[string]interface{}{"prompt": prompt}, "inputs": inputs}
	if cb != "" {
		body["callback_url"] = cb
	}
	return v.agent.do(ec, &restRequest{method: "POST", path: "/agent/execute", body: body})
}

func vlmChat(ec *execCtx, i int, _ Item, v *vlmClient) (interface{}, error) {
	model, _ := ec.str(i, "model", "vlmrun-orion-1:auto")
	in, _ := ec.str(i, "inputType", "text")
	urls := func(param string) []string {
		var out []string
		x, _ := ec.param(i, param+".url", nil)
		for _, e := range asListOrOne(x) {
			if u := strings.TrimSpace(toString(asMap(e)["url"])); u != "" {
				out = append(out, u)
			}
		}
		return out
	}
	var media []interface{}
	switch in {
	case "image":
		for _, u := range urls("imageUrls") {
			media = append(media, map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": u}})
		}
	case "video":
		for _, u := range urls("videoUrls") {
			media = append(media, map[string]interface{}{"type": "video_url", "video_url": map[string]interface{}{"url": u}})
		}
	case "file":
		for _, u := range urls("fileUrls") {
			media = append(media, map[string]interface{}{"type": "file_url", "file_url": map[string]interface{}{"url": u}})
		}
	}
	pm, _ := ec.param(i, "prompt.messages", nil)
	list := asList(pm)
	var msgs []interface{}
	for k, x := range list {
		m := asMap(x)
		role, content := firstNonEmpty(toString(m["role"]), "user"), toString(m["content"])
		if len(media) > 0 && role == "user" && k == len(list)-1 {
			var parts []interface{}
			if strings.TrimSpace(content) != "" {
				parts = append(parts, map[string]interface{}{"type": "text", "text": content})
			}
			msgs = append(msgs, map[string]interface{}{"role": role, "content": append(parts, media...)})
			continue
		}
		if content == "" {
			return nil, permanentError{ec.node.Name + ": text message content cannot be empty"}
		}
		msgs = append(msgs, map[string]interface{}{"role": role, "content": content})
	}
	if len(msgs) == 0 {
		return nil, permanentError{ec.node.Name + ": at least one message is required"}
	}
	body := map[string]interface{}{"messages": msgs, "model": model}
	if rf, _ := ec.param(i, "responseFormat", ""); !restEmpty(rf) {
		x, err := restConv("json", rf)
		if err != nil {
			return nil, permanentError{ec.node.Name + ": invalid JSON format for response_format"}
		}
		if f := asMap(x); f["type"] != nil || f["schema"] != nil {
			if f["type"] == nil {
				f["type"] = "json_schema"
			}
			if f["type"] == "json_schema" && f["strict"] == nil {
				f["strict"] = true
			}
			body["response_format"] = f
		}
	}
	if jo, _ := ec.boolean(i, "jsonOutput", false); jo && in == "text" && body["response_format"] == nil {
		body["response_format"] = map[string]interface{}{"type": "json_object"}
		hasSystem := false
		for _, m := range msgs {
			hasSystem = hasSystem || asMap(m)["role"] == "system"
		}
		if !hasSystem {
			body["messages"] = append([]interface{}{map[string]interface{}{"role": "system", "content": "You are a helpful assistant designed to output JSON."}}, msgs...)
		}
	}
	r, err := v.agent.do(ec, &restRequest{method: "POST", path: "/openai/chat/completions", body: body})
	if err != nil {
		return nil, err
	}
	res := asMap(r)
	choices := asList(res["choices"])
	if body["response_format"] != nil {
		for _, c := range choices {
			msg := asMap(asMap(c)["message"])
			if s, ok := msg["content"].(string); ok {
				var j interface{}
				if jsonUnmarshal([]byte(s), &j) == nil {
					msg["content"] = j
				}
			}
		}
	}
	if simple, _ := ec.boolean(i, "simplifyOutput", false); simple && len(choices) > 0 {
		// n8n outputs the simplified choices array as one item.
		out := map[string]interface{}{}
		for k, c := range choices {
			cm, msg := asMap(c), asMap(asMap(c)["message"])
			idx := cm["index"]
			if idx == nil {
				idx = k
			}
			ann := msg["annotations"]
			if ann == nil {
				ann = []interface{}{}
			}
			out[fmt.Sprint(k)] = map[string]interface{}{"index": idx, "message": map[string]interface{}{"role": firstNonEmpty(toString(pick(msg, "role")), "assistant"),
				"content": firstNonNil(msg["content"], ""), "refusal": msg["refusal"], "annotations": ann}, "logprobs": cm["logprobs"], "finish_reason": firstNonEmpty(toString(pick(cm, "finish_reason")), "stop")}
		}
		return out, nil
	}
	return res, nil
}

var vlmArtifactTypes = map[string][2]string{"img": {"jpg", "image/jpeg"}, "vid": {"mp4", "video/mp4"}, "aud": {"mp3", "audio/mpeg"},
	"doc": {"pdf", "application/pdf"}, "recon": {"spz", "application/octet-stream"}}

func vlmArtifact(ec *execCtx, i int, _ Item, v *vlmClient) (interface{}, error) {
	kind, _ := ec.str(i, "artifactType", "chat")
	obj, _ := ec.str(i, "objectId", "")
	obj = strings.TrimSpace(obj)
	q := url.Values{"object_id": {obj}}
	j := map[string]interface{}{"objectId": obj}
	if kind == "agent" {
		id, _ := ec.str(i, "executionId", "")
		q.Set("execution_id", strings.TrimSpace(id))
		j["executionId"] = id
	} else {
		id, _ := ec.str(i, "sessionId", "")
		q.Set("session_id", strings.TrimSpace(id))
		j["sessionId"] = id
	}
	parts := strings.Split(obj, "_")
	if obj == "" || len(parts) < 2 || len(parts[len(parts)-1]) != 6 {
		return nil, permanentError{fmt.Sprintf("%s: invalid object ID: %s, expected format: <obj_type>_<6-digit-hex-string>", ec.node.Name, obj)}
	}
	res, err := cnRaw(ec, v.agent, &restRequest{method: "GET", path: "/artifacts", query: q})
	if err != nil {
		return nil, err
	}
	typ := parts[0]
	j["type"] = typ
	if typ == "url" {
		u := strings.TrimSpace(string(res.body))
		dl, err := ec.doRequest("GET", u, nil, nil, "")
		if err != nil {
			return nil, permanentError{fmt.Sprintf("%s: failed to process URL artifact: %v", ec.node.Name, err)}
		}
		ext := "bin"
		if pu, err := url.Parse(u); err == nil {
			if e := strings.TrimPrefix(path.Ext(pu.Path), "."); e != "" {
				ext = strings.ToLower(e)
			}
		}
		name := obj + "." + ext
		j["url"], j["filename"] = u, name
		return cnBinaryItem(j, "data", dl.body, firstNonEmpty(dl.contentType, "application/octet-stream"), name), nil
	}
	ext, mt := "bin", firstNonEmpty(res.contentType, "application/octet-stream")
	if t, ok := vlmArtifactTypes[typ]; ok {
		ext, mt = t[0], t[1]
	}
	name := obj + "." + ext
	j["filename"] = name
	return cnBinaryItem(j, "data", res.body, mt, name), nil
}
