package n8n

import (
	"encoding/base64"
	"net/url"
	"strings"
)

// Anthropic vendor node (@n8n/n8n-nodes-langchain.anthropic).

type anthropicAPI struct {
	ec   *execCtx
	base string
	key  string
}

func (ec *execCtx) anthropic() (*anthropicAPI, error) {
	c, err := ec.credential("anthropicApi")
	if err != nil {
		return nil, err
	}
	return &anthropicAPI{ec: ec, base: credBaseURL(c, "https://api.anthropic.com"), key: credString(c, "apiKey")}, nil
}

func (a *anthropicAPI) headers(beta string) map[string]string {
	h := map[string]string{"x-api-key": a.key, "anthropic-version": "2023-06-01"}
	if beta != "" {
		h["anthropic-beta"] = beta
	}
	return h
}

func (a *anthropicAPI) messages(body map[string]interface{}) (map[string]interface{}, error) {
	out, _, err := a.ec.apiCall("POST", a.base+"/v1/messages", a.headers(""), body, "")
	return out, err
}

const anthropicFilesBeta = "files-api-2025-04-14"

func anthropicOutput(ec *execCtx, i int, resp map[string]interface{}, merged bool) []Item {
	var m interface{}
	if merged {
		var b strings.Builder
		for _, c := range asList(resp["content"]) {
			if cm := asMap(c); cm["type"] == "text" {
				b.WriteString(toString(cm["text"]))
			}
		}
		m = b.String()
	}
	if simplify, _ := ec.boolean(i, "simplify", true); simplify {
		j := map[string]interface{}{"content": resp["content"]}
		if merged {
			j["merged_response"] = m
		}
		return one(j)
	}
	j := cloneJSON(resp)
	if merged {
		j["merged_response"] = m
	}
	return one(j)
}

func anthropicNode(ec *execCtx) ([][]Item, error) {
	a, err := ec.anthropic()
	if err != nil {
		return nil, err
	}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOpBy(i, "text", map[string]string{"text": "message", "image": "analyze", "document": "analyze", "file": "upload", "prompt": "generate"})
		mv, _ := ec.param(i, "modelId", "")
		model := firstNonEmpty(rlValue(mv), "claude-sonnet-4-5")
		switch r + "." + o {
		case "text.message":
			return a.message(i, it, model)
		case "image.analyze", "document.analyze":
			text, err := ec.str(i, "text", "")
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(text) == "" {
				return nil, permanentError{"A non-empty prompt is required."}
			}
			var content []interface{}
			if inputType, _ := ec.str(i, "inputType", "url"); inputType == "url" {
				urls, err := ec.str(i, r+"Urls", "")
				if err != nil {
					return nil, err
				}
				prefix := a.base + "/v1/files/"
				for _, u := range splitList(urls) {
					src := map[string]interface{}{"type": "url", "url": u}
					if strings.HasPrefix(u, prefix) {
						src = map[string]interface{}{"type": "file", "file_id": strings.TrimPrefix(u, prefix)}
					}
					content = append(content, map[string]interface{}{"type": r, "source": src})
				}
			} else {
				props, _ := ec.str(i, "binaryPropertyName", "data")
				for _, p := range splitList(props) {
					data, mt, _, err := binaryOf(it, p)
					if err != nil {
						return nil, err
					}
					content = append(content, map[string]interface{}{"type": r, "source": map[string]interface{}{"type": "base64", "media_type": mt, "data": base64.StdEncoding.EncodeToString(data)}})
				}
			}
			content = append(content, map[string]interface{}{"type": "text", "text": text})
			maxTokens, _ := ec.num(i, "options.maxTokens", 1024)
			resp, err := a.messages(map[string]interface{}{"model": model, "max_tokens": maxTokens, "messages": []interface{}{map[string]interface{}{"role": "user", "content": content}}})
			if err != nil {
				return nil, err
			}
			return anthropicOutput(ec, i, resp, false), nil
		case "file.upload":
			var data []byte
			mt, name := "application/octet-stream", "file"
			if inputType, _ := ec.str(i, "inputType", "url"); inputType == "url" {
				u, err := ec.str(i, "fileUrl", "")
				if err != nil {
					return nil, err
				}
				var ct string
				if data, ct, err = ec.downloadWith(u, nil); err != nil {
					return nil, err
				}
				if ct = strings.TrimSpace(strings.Split(ct, ";")[0]); ct != "" {
					mt = ct
				}
				if pu, err := url.Parse(u); err == nil {
					if b := pu.Path[strings.LastIndex(pu.Path, "/")+1:]; b != "" {
						name = b
					}
				}
			} else {
				p, _ := ec.str(i, "binaryPropertyName", "data")
				var err error
				if data, mt, name, err = binaryOf(it, p); err != nil {
					return nil, err
				}
			}
			if n, _ := ec.str(i, "options.fileName", ""); n != "" {
				name = n
			}
			body, ct := multipartBody(nil, "file", name, mt, data)
			out, _, err := a.ec.apiCall("POST", a.base+"/v1/files", a.headers(anthropicFilesBeta), body, ct)
			if err != nil {
				return nil, err
			}
			out["url"] = a.base + "/v1/files/" + toString(out["id"])
			return one(out), nil
		case "file.get", "file.deleteFile", "file.delete":
			id, err := ec.str(i, "fileId", "")
			if err != nil {
				return nil, err
			}
			method := "GET"
			if o != "get" {
				method = "DELETE"
			}
			out, _, err := a.ec.apiCall(method, a.base+"/v1/files/"+url.PathEscape(id), a.headers(anthropicFilesBeta), nil, "")
			if err != nil {
				return nil, err
			}
			if method == "GET" {
				out["url"] = a.base + "/v1/files/" + toString(out["id"])
			}
			return one(out), nil
		case "file.list":
			all, _ := ec.boolean(i, "returnAll", false)
			limit, _ := ec.num(i, "limit", 50)
			var out []Item
			after := ""
			for {
				q := url.Values{"limit": {"1000"}}
				if after != "" {
					q.Set("after_id", after)
				}
				res, _, err := a.ec.apiCall("GET", a.base+"/v1/files?"+q.Encode(), a.headers(anthropicFilesBeta), nil, "")
				if err != nil {
					return nil, err
				}
				for _, f := range asList(res["data"]) {
					fm := asMap(f)
					fm["url"] = a.base + "/v1/files/" + toString(fm["id"])
					out = append(out, Item{JSON: fm})
					if !all && len(out) >= int(limit) {
						return out, nil
					}
				}
				if !toBool(res["has_more"]) {
					return out, nil
				}
				after = toString(res["last_id"])
			}
		}
		return nil, unsupportedOp("Anthropic", r, o)
	})
}

// message implements Text → Message a Model, including connected tools.
func (a *anthropicAPI) message(i int, it Item, model string) ([]Item, error) {
	ec := a.ec
	mv, err := ec.param(i, "messages.values", nil)
	if err != nil {
		return nil, err
	}
	var msgs []interface{}
	var chat []chatMsg
	for _, raw := range asList(mv) {
		m := asMap(raw)
		c := toString(m["content"])
		if strings.TrimSpace(c) == "" {
			continue
		}
		role := firstNonEmpty(toString(m["role"]), "user")
		msgs = append(msgs, map[string]interface{}{"role": role, "content": c})
		chat = append(chat, chatMsg{Role: role, Content: c})
	}
	if add, _ := ec.boolean(i, "addAttachments", false); add {
		props, _ := ec.str(i, "binaryPropertyName", "data")
		var blocks []interface{}
		for _, p := range splitList(props) {
			data, mt, _, err := binaryOf(it, p)
			if err != nil {
				return nil, err
			}
			typ := "document"
			if strings.HasPrefix(mt, "image/") {
				typ = "image"
			}
			blocks = append(blocks, map[string]interface{}{"type": typ, "source": map[string]interface{}{"type": "base64", "media_type": mt, "data": base64.StdEncoding.EncodeToString(data)}})
		}
		if len(blocks) > 0 {
			msgs = append(msgs, map[string]interface{}{"role": "user", "content": blocks})
		}
	}
	if len(msgs) == 0 {
		return nil, permanentError{"A non-empty prompt is required."}
	}
	system, _ := ec.str(i, "options.system", "")
	maxTokens, _ := ec.num(i, "options.maxTokens", 1024)
	merged, _ := ec.boolean(i, "options.includeMergedResponse", false)
	tools, err := ec.buildTools(i)
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		m := &anthropicModel{client: outboundClient(ec.opts.Outbound), limit: ec.opts.Outbound.MaxResponseBytesOrDefault(), baseURL: a.base, apiKey: a.key, model: model}
		var base llmRequest
		base.MaxTokens = int(maxTokens)
		if t, err := ec.num(i, "options.temperature", -1); err == nil && t >= 0 {
			base.Temperature = &t
		}
		var prompt []string
		for _, c := range chat {
			prompt = append(prompt, c.Content)
		}
		maxIter, _ := ec.num(i, "options.maxToolsIterations", 15)
		res, err := ec.runAgent(ec.ctx, m, base, system, strings.Join(prompt, "\n"), tools, nil, int(maxIter))
		if err != nil {
			return nil, err
		}
		return anthropicOutput(ec, i, map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": res.text}}, "role": "assistant", "model": model, "stop_reason": "end_turn"}, merged), nil
	}
	body := map[string]interface{}{"model": model, "messages": msgs, "max_tokens": maxTokens}
	if system != "" {
		body["system"] = system
	}
	for k, opt := range map[string]string{"temperature": "temperature", "top_p": "topP", "top_k": "topK"} {
		if v, ok := ec.raw("options." + opt); ok && v != nil {
			if n, err := ec.num(i, "options."+opt, 0); err == nil {
				body[k] = n
			}
		}
	}
	var builtIn []interface{}
	if ws, _ := ec.boolean(i, "options.webSearch", false); ws {
		t := map[string]interface{}{"type": "web_search_20250305", "name": "web_search"}
		if n, _ := ec.num(i, "options.maxUses", 0); n > 0 {
			t["max_uses"] = n
		}
		for k, opt := range map[string]string{"allowed_domains": "allowedDomains", "blocked_domains": "blockedDomains"} {
			if s, _ := ec.str(i, "options."+opt, ""); s != "" {
				t[k] = splitList(s)
			}
		}
		builtIn = append(builtIn, t)
	}
	if len(builtIn) > 0 {
		body["tools"] = builtIn
	}
	resp, err := a.messages(body)
	for pauses := 0; err == nil && resp["stop_reason"] == "pause_turn" && pauses < 3; pauses++ {
		msgs = append(msgs, map[string]interface{}{"role": "assistant", "content": resp["content"]})
		body["messages"] = msgs
		resp, err = a.messages(body)
	}
	if err != nil {
		return nil, err
	}
	return anthropicOutput(ec, i, resp, merged), nil
}
