package n8n

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// geminiAPI calls the Gemini API with a googlePalmApi credential.
type geminiAPI struct {
	ec   *execCtx
	base string
	key  string
}

func (ec *execCtx) gemini() (*geminiAPI, error) {
	c, err := ec.credential("googlePalmApi")
	if err != nil {
		return nil, err
	}
	return &geminiAPI{ec: ec, base: credBaseURL(c, "https://generativelanguage.googleapis.com"), key: credString(c, "apiKey")}, nil
}

func (g *geminiAPI) call(method, path string, body interface{}) (map[string]interface{}, error) {
	out, _, err := g.ec.apiCall(method, g.base+path, map[string]string{"x-goog-api-key": g.key}, body, "")
	return out, err
}

// modelPath normalizes a model ID to "models/<id>".
func geminiModelPath(model string) string {
	if strings.HasPrefix(model, "models/") || strings.HasPrefix(model, "tunedModels/") {
		return model
	}
	return "models/" + model
}

func (g *geminiAPI) generate(model string, body map[string]interface{}) (map[string]interface{}, error) {
	out, err := g.call("POST", "/v1beta/"+geminiModelPath(model)+":generateContent", body)
	if err != nil {
		return nil, err
	}
	if len(asList(out["candidates"])) == 0 {
		return nil, permanentError{"Gemini returned no candidates: " + truncate(mustJSON(out), 300)}
	}
	return out, nil
}

// geminiInlineLimit is the largest file sent inline; bigger files go
// through the File API.
const geminiInlineLimit = 15 << 20

// filePart turns file bytes into a request part (inline or uploaded).
func (g *geminiAPI) filePart(data []byte, mimeType string) (map[string]interface{}, error) {
	if len(data) <= geminiInlineLimit {
		return map[string]interface{}{"inlineData": map[string]interface{}{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(data)}}, nil
	}
	f, err := g.upload(data, mimeType, "")
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"fileData": map[string]interface{}{"fileUri": f["uri"], "mimeType": f["mimeType"]}}, nil
}

// upload stores a file with the File API and waits until it is active.
func (g *geminiAPI) upload(data []byte, mimeType, name string) (map[string]interface{}, error) {
	meta := map[string]interface{}{}
	if name != "" {
		meta["file"] = map[string]interface{}{"displayName": name}
	}
	mb, _ := json.Marshal(meta)
	r, err := g.ec.doRequest("POST", g.base+"/upload/v1beta/files", map[string]string{
		"x-goog-api-key": g.key, "X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start",
		"X-Goog-Upload-Header-Content-Length": strconv.Itoa(len(data)), "X-Goog-Upload-Header-Content-Type": mimeType,
	}, mb, "application/json")
	if err != nil {
		return nil, err
	}
	up := r.header.Get("X-Goog-Upload-URL")
	if up == "" {
		return nil, permanentError{"Gemini: file upload did not return an upload URL"}
	}
	r, err = g.ec.doRequest("POST", up, map[string]string{"X-Goog-Upload-Offset": "0", "X-Goog-Upload-Command": "upload, finalize"}, data, mimeType)
	if err != nil {
		return nil, err
	}
	var res struct {
		File map[string]interface{} `json:"file"`
	}
	if err := json.Unmarshal(r.body, &res); err != nil || res.File == nil {
		return nil, permanentError{"Gemini: unexpected file upload response"}
	}
	f := res.File
	for i := 0; toString(f["state"]) == "PROCESSING" && i < 600; i++ {
		select {
		case <-g.ec.ctx.Done():
			return nil, g.ec.ctx.Err()
		case <-time.After(time.Second):
		}
		if f, err = g.call("GET", "/v1beta/"+toString(f["name"]), nil); err != nil {
			return nil, err
		}
	}
	if toString(f["state"]) == "FAILED" {
		return nil, permanentError{"Gemini: file processing failed: " + toString(asMap(f["error"])["message"])}
	}
	return f, nil
}

// fileParts collects the files of an analyze/transcribe operation from URLs
// or binary properties.
func (g *geminiAPI) fileParts(i int, it Item, urlsParam, fallbackMime string) ([]interface{}, error) {
	ec := g.ec
	inputType, _ := ec.str(i, "inputType", "url")
	var parts []interface{}
	if inputType == "binary" {
		props, _ := ec.str(i, "binaryPropertyName", "data")
		for _, p := range splitList(props) {
			data, mt, _, err := binaryOf(it, p)
			if err != nil {
				return nil, err
			}
			part, err := g.filePart(data, mt)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
		return parts, nil
	}
	urls, err := ec.str(i, urlsParam, "")
	if err != nil {
		return nil, err
	}
	for _, u := range splitList(urls) {
		switch {
		case strings.HasPrefix(u, g.base+"/") || strings.HasPrefix(u, "https://generativelanguage.googleapis.com/"):
			// A Gemini File API URI.
			f, err := g.call("GET", strings.TrimPrefix(strings.TrimPrefix(u, g.base), "https://generativelanguage.googleapis.com"), nil)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]interface{}{"fileData": map[string]interface{}{"fileUri": u, "mimeType": f["mimeType"]}})
		case isYouTubeURL(u):
			// Gemini reads YouTube videos directly.
			parts = append(parts, map[string]interface{}{"fileData": map[string]interface{}{"fileUri": u, "mimeType": "video/*"}})
		default:
			data, mt, err := ec.downloadWith(u, nil)
			if err != nil {
				return nil, err
			}
			if mt = strings.TrimSpace(strings.Split(mt, ";")[0]); mt == "" || mt == "application/octet-stream" {
				mt = fallbackMime
			}
			part, err := g.filePart(data, mt)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return nil, permanentError{"Gemini: no input files"}
	}
	return parts, nil
}

func isYouTubeURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := strings.TrimPrefix(p.Hostname(), "www.")
	return h == "youtube.com" || h == "youtu.be" || h == "m.youtube.com"
}

// geminiOutput returns candidates (simplified) or the whole response.
func geminiOutput(ec *execCtx, i int, resp map[string]interface{}) []Item {
	if simplify, _ := ec.boolean(i, "simplify", true); !simplify {
		return []Item{{JSON: resp}}
	}
	var out []Item
	for _, c := range asList(resp["candidates"]) {
		if m, ok := c.(map[string]interface{}); ok {
			out = append(out, Item{JSON: m})
		}
	}
	return out
}

// geminiBinaryItems turns inline image/video parts into binary items.
func geminiBinaryItems(resp map[string]interface{}, prop, base string) ([]Item, error) {
	var out []Item
	for _, c := range asList(resp["candidates"]) {
		for _, raw := range asList(asMap(asMap(c)["content"])["parts"]) {
			d := asMap(asMap(raw)["inlineData"])
			if d == nil {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(toString(d["data"]))
			if err != nil {
				return nil, permanentError{"Gemini: invalid image data"}
			}
			out = append(out, binaryItem(data, toString(d["mimeType"]), base, prop))
			break
		}
	}
	if len(out) == 0 {
		return nil, permanentError{"Gemini: the model returned no image"}
	}
	return out, nil
}

// binaryItem is an item carrying one file, with its metadata as JSON (as
// n8n's AI vendor nodes output generated files).
func binaryItem(data []byte, mimeType, base, prop string) Item {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	ext := "bin"
	if e := extFromMime(mimeType); e != "" {
		ext = e
	}
	b := binaryData(data, mimeType, base+"."+ext)
	j := cloneJSON(b)
	delete(j, "data")
	return Item{JSON: j, Binary: map[string]interface{}{prop: b}}
}

func extFromMime(mt string) string {
	switch strings.Split(mt, ";")[0] {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "video/mp4":
		return "mp4"
	case "audio/mpeg":
		return "mp3"
	case "audio/wav", "audio/x-wav":
		return "wav"
	}
	if i := strings.IndexByte(mt, '/'); i >= 0 {
		return strings.TrimPrefix(mt[i+1:], "x-")
	}
	return ""
}

func googleGeminiNode(ec *execCtx) ([][]Item, error) {
	g, err := ec.gemini()
	if err != nil {
		return nil, err
	}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOpBy(i, "text", map[string]string{"text": "message", "image": "analyze", "video": "analyze", "audio": "transcribe", "document": "analyze", "file": "upload"})
		modelV, _ := ec.param(i, "modelId", "")
		model := rlValue(modelV)
		switch r + "." + o {
		case "text.message":
			return g.message(i, model)
		case "image.analyze", "document.analyze", "audio.analyze", "video.analyze", "audio.transcribe":
			src := map[string][2]string{"image": {"imageUrls", "image/png"}, "document": {"documentUrls", "application/pdf"},
				"audio": {"audioUrls", "audio/mpeg"}, "video": {"videoUrls", "video/mp4"}}[r]
			parts, err := g.fileParts(i, it, src[0], src[1])
			if err != nil {
				return nil, err
			}
			var text string
			if o == "transcribe" {
				text = "Generate a transcript of the speech"
				if s, _ := ec.str(i, "options.startTime", ""); s != "" {
					text += " from " + s
				}
				if e, _ := ec.str(i, "options.endTime", ""); e != "" {
					text += " to " + e
				}
			} else if text, err = ec.str(i, "text", ""); err != nil {
				return nil, err
			} else if strings.TrimSpace(text) == "" {
				return nil, permanentError{"A non-empty prompt is required."}
			}
			body := map[string]interface{}{"contents": []interface{}{map[string]interface{}{"role": "user", "parts": append(parts, map[string]interface{}{"text": text})}}}
			if n, _ := ec.num(i, "options.maxOutputTokens", 0); n > 0 {
				body["generationConfig"] = map[string]interface{}{"maxOutputTokens": n}
			}
			resp, err := g.generate(model, body)
			if err != nil {
				return nil, err
			}
			return geminiOutput(ec, i, resp), nil
		case "image.generate":
			prompt, err := ec.str(i, "prompt", "")
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(prompt) == "" {
				return nil, permanentError{"A non-empty prompt is required."}
			}
			prop, _ := ec.str(i, "options.binaryPropertyOutput", "data")
			if strings.Contains(model, "imagen") {
				n, _ := ec.num(i, "options.sampleCount", 1)
				resp, err := g.call("POST", "/v1beta/"+geminiModelPath(model)+":predict", map[string]interface{}{
					"instances": []interface{}{map[string]interface{}{"prompt": prompt}}, "parameters": map[string]interface{}{"sampleCount": n}})
				if err != nil {
					return nil, err
				}
				var out []Item
				for _, p := range asList(resp["predictions"]) {
					m := asMap(p)
					data, err := base64.StdEncoding.DecodeString(toString(m["bytesBase64Encoded"]))
					if err != nil {
						return nil, permanentError{"Gemini: invalid image data"}
					}
					out = append(out, binaryItem(data, toString(m["mimeType"]), "image", prop))
				}
				return out, nil
			}
			if model == "" {
				model = "models/gemini-2.5-flash-image"
			}
			resp, err := g.generate(model, map[string]interface{}{
				"contents":         []interface{}{map[string]interface{}{"role": "user", "parts": []interface{}{map[string]interface{}{"text": prompt}}}},
				"generationConfig": map[string]interface{}{"responseModalities": []interface{}{"IMAGE", "TEXT"}}})
			if err != nil {
				return nil, err
			}
			return geminiBinaryItems(resp, prop, "image")
		case "image.edit":
			prompt, err := ec.str(i, "prompt", "")
			if err != nil {
				return nil, err
			}
			if model == "" {
				model = "models/gemini-2.5-flash-image"
			}
			prop, _ := ec.str(i, "options.binaryPropertyOutput", "edited")
			names := []string{"data"}
			if v, _ := ec.param(i, "images.values", nil); len(asList(v)) > 0 {
				names = nil
				for _, raw := range asList(v) {
					if n := toString(asMap(raw)["binaryPropertyName"]); n != "" {
						names = append(names, n)
					}
				}
			}
			var parts []interface{}
			for _, n := range names {
				data, mt, _, err := binaryOf(it, n)
				if err != nil {
					return nil, err
				}
				part, err := g.filePart(data, mt)
				if err != nil {
					return nil, err
				}
				parts = append(parts, part)
			}
			resp, err := g.generate(model, map[string]interface{}{
				"contents":         []interface{}{map[string]interface{}{"role": "user", "parts": append(parts, map[string]interface{}{"text": prompt})}},
				"generationConfig": map[string]interface{}{"responseModalities": []interface{}{"IMAGE"}}})
			if err != nil {
				return nil, err
			}
			return geminiBinaryItems(resp, prop, "image")
		case "video.generate":
			return g.generateVideo(i, model)
		case "video.download":
			u, err := ec.str(i, "url", "")
			if err != nil {
				return nil, err
			}
			prop, _ := ec.str(i, "options.binaryPropertyOutput", "data")
			return g.downloadVideo(u, prop)
		case "file.upload":
			inputType, _ := ec.str(i, "inputType", "url")
			var data []byte
			mt, name := "application/octet-stream", ""
			if inputType == "url" {
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
			} else {
				prop, _ := ec.str(i, "binaryPropertyName", "data")
				var err error
				if data, mt, name, err = binaryOf(it, prop); err != nil {
					return nil, err
				}
			}
			f, err := g.upload(data, mt, name)
			if err != nil {
				return nil, err
			}
			return one(f), nil
		}
		return nil, unsupportedOp("Google Gemini", r, o)
	})
}

// message implements Text → Message a Model, including connected tools.
func (g *geminiAPI) message(i int, model string) ([]Item, error) {
	ec := g.ec
	mv, err := ec.param(i, "messages.values", nil)
	if err != nil {
		return nil, err
	}
	var msgs []chatMsg
	if sys, _ := ec.str(i, "options.systemMessage", ""); sys != "" {
		msgs = append(msgs, chatMsg{Role: "system", Content: sys})
	}
	for _, raw := range asList(mv) {
		m := asMap(raw)
		c := toString(m["content"])
		if strings.TrimSpace(c) == "" {
			continue
		}
		role := toString(m["role"])
		switch role {
		case "model":
			role = "assistant"
		case "":
			role = "user"
		}
		msgs = append(msgs, chatMsg{Role: role, Content: c})
	}
	if len(msgs) == 0 || msgs[len(msgs)-1].Role == "system" {
		return nil, permanentError{"A non-empty prompt is required."}
	}
	jsonOut, _ := ec.boolean(i, "jsonOutput", false)
	tools, err := ec.buildTools(i)
	if err != nil {
		return nil, err
	}
	gen := map[string]interface{}{}
	for _, k := range []string{"temperature", "topP", "topK", "maxOutputTokens", "candidateCount", "frequencyPenalty", "presencePenalty"} {
		if v, ok := ec.raw("options." + k); ok && v != nil {
			if n, err := ec.num(i, "options."+k, 0); err == nil {
				gen[k] = n
			}
		}
	}
	if n, err := ec.num(i, "options.thinkingBudget", -2); err == nil && n > -2 {
		gen["thinkingConfig"] = map[string]interface{}{"thinkingBudget": n}
	}
	if jsonOut {
		gen["responseMimeType"] = "application/json"
	}
	var builtIn []interface{}
	bt, _ := ec.param(i, "builtInTools", nil)
	btm := asMap(bt)
	for _, k := range []string{"googleSearch", "urlContext", "codeExecution"} {
		if toBool(btm[k]) {
			builtIn = append(builtIn, map[string]interface{}{k: map[string]interface{}{}})
		}
	}
	if btm["googleMaps"] != nil {
		builtIn = append(builtIn, map[string]interface{}{"googleMaps": map[string]interface{}{}})
	}
	if ce, _ := ec.boolean(i, "options.codeExecution", false); ce && ec.version < 1.1 {
		builtIn = append(builtIn, map[string]interface{}{"codeExecution": map[string]interface{}{}})
	}
	if len(tools) > 0 {
		model := &geminiModel{client: outboundClient(ec.opts.Outbound), limit: ec.opts.Outbound.MaxResponseBytesOrDefault(), baseURL: g.base, apiKey: g.key, model: model}
		var base llmRequest
		if t, ok := gen["temperature"].(float64); ok {
			base.Temperature = &t
		}
		system, prompt := "", ""
		for _, m := range msgs {
			if m.Role == "system" {
				system = m.Content
			} else {
				prompt += m.Content + "\n"
			}
		}
		maxIter, _ := ec.num(i, "options.maxToolsIterations", 15)
		r, err := ec.runAgent(ec.ctx, model, base, system, strings.TrimSpace(prompt), tools, nil, int(maxIter))
		if err != nil {
			return nil, err
		}
		return []Item{{JSON: map[string]interface{}{"content": map[string]interface{}{"parts": []interface{}{map[string]interface{}{"text": r.text}}, "role": "model"},
			"finishReason": "STOP", "index": 0.0}}}, nil
	}
	var contents []interface{}
	body := map[string]interface{}{}
	for _, m := range msgs {
		switch m.Role {
		case "system":
			body["systemInstruction"] = map[string]interface{}{"parts": []interface{}{map[string]interface{}{"text": m.Content}}}
		case "assistant":
			contents = append(contents, map[string]interface{}{"role": "model", "parts": []interface{}{map[string]interface{}{"text": m.Content}}})
		default:
			contents = append(contents, map[string]interface{}{"role": "user", "parts": []interface{}{map[string]interface{}{"text": m.Content}}})
		}
	}
	body["contents"] = contents
	if len(gen) > 0 {
		body["generationConfig"] = gen
	}
	if len(builtIn) > 0 {
		body["tools"] = builtIn
	}
	resp, err := g.generate(model, body)
	if err != nil {
		return nil, err
	}
	if inc, _ := ec.boolean(i, "options.includeMergedResponse", false); inc {
		for _, c := range asList(resp["candidates"]) {
			cm := asMap(c)
			var b strings.Builder
			for _, p := range asList(asMap(cm["content"])["parts"]) {
				b.WriteString(toString(asMap(p)["text"]))
			}
			cm["mergedResponse"] = b.String()
		}
	}
	return geminiOutput(ec, i, resp), nil
}

func (g *geminiAPI) generateVideo(i int, model string) ([]Item, error) {
	ec := g.ec
	prompt, err := ec.str(i, "prompt", "")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(model, "veo") {
		return nil, permanentError{fmt.Sprintf("Model %s is not supported for video generation. Please use a Veo model", model)}
	}
	params := map[string]interface{}{}
	for _, k := range []string{"aspectRatio", "personGeneration", "durationSeconds"} {
		if v, _ := ec.param(i, "options."+k, nil); v != nil && v != "" {
			params[k] = v
		}
	}
	n, _ := ec.num(i, "options.sampleCount", 1)
	params["sampleCount"] = n
	op, err := g.call("POST", "/v1beta/"+geminiModelPath(model)+":predictLongRunning", map[string]interface{}{
		"instances": []interface{}{map[string]interface{}{"prompt": prompt}}, "parameters": params})
	if err != nil {
		return nil, err
	}
	for !toBool(op["done"]) {
		select {
		case <-ec.ctx.Done():
			return nil, ec.ctx.Err()
		case <-time.After(5 * time.Second):
		}
		if op, err = g.call("GET", "/v1beta/"+toString(op["name"]), nil); err != nil {
			return nil, err
		}
	}
	if e := asMap(op["error"]); e != nil {
		return nil, permanentError{"Gemini video generation failed: " + toString(e["message"])}
	}
	samples := asList(asMap(asMap(op["response"])["generateVideoResponse"])["generatedSamples"])
	returnAs, _ := ec.str(i, "returnAs", "video")
	prop, _ := ec.str(i, "options.binaryPropertyOutput", "data")
	var out []Item
	for _, s := range samples {
		u := toString(asMap(asMap(s)["video"])["uri"])
		if returnAs != "video" {
			out = append(out, Item{JSON: map[string]interface{}{"url": u}})
			continue
		}
		items, err := g.downloadVideo(u, prop)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func (g *geminiAPI) downloadVideo(u, prop string) ([]Item, error) {
	pu, err := url.Parse(u)
	if err != nil {
		return nil, permanentError{"Gemini: invalid video URL"}
	}
	gb, _ := url.Parse(g.base)
	headers := map[string]string{}
	if gb != nil && pu.Host == gb.Host {
		// Only send the API key to the credential's own host.
		headers["x-goog-api-key"] = g.key
	}
	data, mt, err := g.ec.downloadWith(u, headers)
	if err != nil {
		return nil, err
	}
	if mt = strings.TrimSpace(strings.Split(mt, ";")[0]); mt == "" || mt == "application/octet-stream" {
		mt = "video/mp4"
	}
	return []Item{binaryItem(data, mt, "video", prop)}, nil
}
