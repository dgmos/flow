package n8n

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// PostWire (n8n-nodes-postwire): AI-written social posts published to many
// networks. API key (v1-2 default) or OAuth2 (v3 default). Version 1 has no
// resource; its operations map onto the v2 ones. The media-link probe the
// package runs before publishing is skipped; PostWire validates links too.

func init() {
	registerREST("n8n-nodes-postwire.postWire", &restService{
		defResource: "post",
		defOps: map[string]string{"post": "publish", "scheduledPost": "getAll", "brand": "getAll", "connection": "getAll",
			"approval": "getAll", "account": "get", "media": "upload"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			def := "apiKey"
			if ec.version >= 3 {
				def = "oAuth2"
			}
			auth, _ := ec.str(0, "authentication", def)
			ct := "postWireApi"
			if auth == "oAuth2" {
				ct = "postWireOAuth2Api"
			}
			c, err := ec.credential(ct)
			if err != nil {
				return nil, err
			}
			cl := bearerClient(ec.apiBase("postWire", "https://postwire.io"), c, "apiKey")
			cl.headers["X-PostWire-Source"] = "n8n"
			return cl, nil
		},
		aliases: map[string]string{"post/me": "account/get", "post/generateAndPublish": "post/publish"},
		ops: map[string]*restOp{
			"post/publish":   {run: pwPost},
			"post/schedule":  {run: pwPost},
			"post/generate":  {run: pwPost},
			"post/planWeek":  {run: pwPost},
			"post/getStatus": {run: pwStatus},
			"media/upload": {run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
				prop, _ := ec.str(i, "binaryPropertyName", "data")
				v, err := pwUpload(ec, i, it, c, prop)
				return restItems(v), err
			}},
			"account/get": {path: "/api/me", post: func(ec *execCtx, i int, v interface{}) (interface{}, error) {
				simplify, _ := ec.boolean(i, "simplify", true)
				if ec.version == 1 || !simplify {
					return v, nil
				}
				me := asMap(v)
				usage, brands := asMap(me["usage"]), asMap(me["brands"])
				out := map[string]interface{}{"email": me["email"], "plan": me["plan"], "posts_used": usage["posts"], "posts_limit": usage["limit"],
					"brands_used": brands["used"], "brands_limit": brands["limit"], "verified": me["verified"], "account_id": me["account_id"]}
				conn := []interface{}{}
				for _, c := range asList(me["connections"]) {
					conn = append(conn, asMap(c)["platform"])
				}
				out["connected"] = conn
				if ws := asMap(me["workspace"]); me["workspace"] != nil {
					m := asMap(me["member"])
					out["workspace_id"], out["workspace_name"] = ws["id"], ws["name"]
					out["role"] = firstNonNil(m["effective_role"], m["role"])
					out["brand_ids"] = m["brand_ids"]
				}
				return out, nil
			}},
			"brand/getAll": {path: "/api/brands", post: pwSlice("brands")},
			"brand/get":    {path: "/api/brands/{brand}", post: pwUnwrap("brand")},
			"brand/create": {method: "POST", path: "/api/brands", body: "name", post: pwUnwrap("brand")},
			"brand/update": {method: "PATCH", path: "/api/brands/{brand}", body: "name", post: pwUnwrap("brand")},
			"brand/delete": {method: "DELETE", path: "/api/brands/{brand}", post: func(ec *execCtx, i int, _ interface{}) (interface{}, error) {
				id, _ := ec.param(i, "brand", "")
				return map[string]interface{}{"deleted": true, "id": strings.TrimSpace(rlValue(id))}, nil
			}},
			"scheduledPost/getAll": {path: "/api/schedule", query: "@filters from:from:iso to:to:iso", post: func(ec *execCtx, i int, v interface{}) (interface{}, error) {
				status, _ := ec.str(i, "filters.status", "")
				var out []interface{}
				for _, r := range asList(asMap(v)["scheduled"]) {
					if status == "" || asMap(r)["status"] == status {
						out = append(out, r)
					}
				}
				return pwLimit(ec, i, out), nil
			}},
			"scheduledPost/get": {path: "/api/schedule/{scheduledPostId}", post: pwUnwrap("scheduled")},
			"scheduledPost/delete": {method: "DELETE", path: "/api/schedule/{scheduledPostId}", post: func(ec *execCtx, i int, _ interface{}) (interface{}, error) {
				id, _ := ec.str(i, "scheduledPostId", "")
				return map[string]interface{}{"deleted": true, "id": strings.TrimSpace(id)}, nil
			}},
			"scheduledPost/update": {method: "PATCH", path: "/api/schedule/{scheduledPostId}", body: "@updateFields runAt:run_at:iso label", post: pwUnwrap("scheduled"),
				build: func(ec *execCtx, i int, r *restRequest) error {
					if len(r.body) == 0 {
						return permanentError{ec.node.Name + ": Nothing to update"}
					}
					return nil
				}},
			"connection/getAll": {path: "/api/me", post: func(ec *execCtx, i int, v interface{}) (interface{}, error) {
				me := asMap(v)
				names := map[string]interface{}{}
				for _, b := range asList(me["brand_list"]) {
					names[toString(asMap(b)["id"])] = asMap(b)["name"]
				}
				var out []interface{}
				for _, c := range asList(me["connections"]) {
					m := map[string]interface{}{}
					for k, x := range asMap(c) {
						m[k] = x
					}
					m["network"], m["brand_name"] = m["platform"], names[toString(m["brand_id"])]
					out = append(out, m)
				}
				return pwLimit(ec, i, out), nil
			}},
			"connection/checkHealth":       {path: "/api/connect/{platform}/health"},
			"connection/createConnectLink": {method: "POST", path: "/api/connect-link", body: "@options platform"},
			"approval/get":                 {path: "/api/approvals/{approvalId}", post: pwUnwrap("approval")},
			"approval/getAll": {path: "/api/approvals", list: true, listKey: "approvals", sizeParam: "limit", maxSize: 200,
				query: "@filters status brand:brand_id:rl", pager: func(p *restPage, _ interface{}, _ http.Header) bool {
					last := asMap(p.last[len(p.last)-1])["created_at"]
					if last == nil || len(p.last) < 200 {
						return false
					}
					p.query.Set("before", toString(last))
					return true
				}},
		},
	})
}

func pwUnwrap(key string) func(*execCtx, int, interface{}) (interface{}, error) {
	return func(_ *execCtx, _ int, v interface{}) (interface{}, error) {
		if x := asMap(v)[key]; x != nil {
			return x, nil
		}
		return v, nil
	}
}

func pwLimit(ec *execCtx, i int, l []interface{}) []interface{} {
	if all, _ := ec.boolean(i, "returnAll", false); !all {
		n, _ := ec.num(i, "limit", 50)
		if int(n) < len(l) {
			l = l[:int(n)]
		}
	}
	return l
}

func pwSlice(key string) func(*execCtx, int, interface{}) (interface{}, error) {
	return func(ec *execCtx, i int, v interface{}) (interface{}, error) {
		return pwLimit(ec, i, asList(asMap(v)[key])), nil
	}
}

var pwMediaNeed = map[string]string{"instagram": "any", "tiktok": "video", "youtube": "video"}

var (
	pwVideoRe = regexp.MustCompile(`(?i)\.(mp4|mov|webm|m3u8|avi|mkv|m4v)(\?|#|$)`)
	pwImageRe = regexp.MustCompile(`(?i)\.(jpe?g|png|gif|webp|heic|heif|avif|bmp|tiff?)(\?|#|$)`)
)

func pwClassify(u, declared string) string {
	switch {
	case u == "":
		return "none"
	case declared == "video" || declared == "image":
		return declared
	case pwVideoRe.MatchString(u):
		return "video"
	case pwImageRe.MatchString(u):
		return "image"
	}
	return "unknown"
}

// pwUpload hosts a binary file on PostWire storage (upload slot, PUT, finalize).
func pwUpload(ec *execCtx, i int, it Item, c *restClient, prop string) (map[string]interface{}, error) {
	data, mt, _, err := binaryOf(it, prop)
	if err != nil {
		return nil, err
	}
	ct := strings.ToLower(strings.TrimSpace(strings.Split(mt, ";")[0]))
	switch ct {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "video/mp4", "video/quicktime", "video/webm":
	default:
		return nil, permanentError{fmt.Sprintf("%s: the file in '%s' is %s; PostWire hosts JPEG, PNG, WebP, GIF, MP4, MOV and WebM", ec.node.Name, prop, firstNonEmpty(ct, "of an unknown type"))}
	}
	const partMax = 50 << 20
	body := map[string]interface{}{"content_type": ct, "size_bytes": len(data)}
	if len(data) > partMax {
		body["multipart"] = true
	}
	sv, err := c.do(ec, &restRequest{method: "POST", path: "/api/media/upload-url", body: body})
	if err != nil {
		return nil, err
	}
	slot := asMap(sv)
	type part struct {
		url        string
		start, end int
	}
	parts := []part{{toString(slot["upload_url"]), 0, len(data)}}
	if len(data) > partMax {
		parts = nil
		for _, x := range asList(slot["parts"]) {
			p := asMap(x)
			s, _ := toFloat(p["start"])
			e, _ := toFloat(p["end"])
			parts = append(parts, part{toString(p["upload_url"]), int(s), int(e)})
		}
	}
	for _, p := range parts {
		if p.start < 0 || p.end > len(data) || p.start > p.end {
			return nil, permanentError{ec.node.Name + ": invalid upload part range"}
		}
		if _, err := ec.doRequest("PUT", p.url, map[string]string{"Content-Type": ct}, data[p.start:p.end], ct); err != nil {
			return nil, err
		}
	}
	v, err := c.do(ec, &restRequest{method: "GET", path: "/api/media/finalize", query: url.Values{"path": {toString(slot["path"])}}})
	return asMap(v), err
}

// pwGenerate asks for drafts, retrying twice for networks left empty.
func pwGenerate(ec *execCtx, c *restClient, body map[string]interface{}) (map[string]interface{}, []interface{}, map[string]interface{}, error) {
	v, err := c.do(ec, &restRequest{method: "POST", path: "/api/generate", body: body})
	if err != nil {
		return nil, nil, nil, err
	}
	first := asMap(v)
	var next map[string]interface{}
	if first["next_step"] != nil {
		next = map[string]interface{}{"next_step": first["next_step"], "not_connected": first["not_connected"], "connect_urls": first["connect_urls"]}
	}
	drafts := map[string]interface{}{}
	for k, d := range asMap(first["drafts"]) {
		drafts[k] = d
	}
	empty := func() []interface{} {
		var out []interface{}
		for _, p := range asList(body["platforms"]) {
			if strings.TrimSpace(toString(asMap(drafts[toString(p)])["text"])) == "" {
				out = append(out, p)
			}
		}
		return out
	}
	missing := empty()
	for attempt := 0; attempt < 2 && len(missing) > 0; attempt++ {
		again := map[string]interface{}{}
		for k, x := range body {
			again[k] = x
		}
		again["platforms"] = missing
		v, err := c.do(ec, &restRequest{method: "POST", path: "/api/generate", body: again})
		if err != nil {
			return nil, nil, nil, err
		}
		for _, p := range missing {
			if d := asMap(asMap(asMap(v)["drafts"])[toString(p)]); strings.TrimSpace(toString(d["text"])) != "" {
				drafts[toString(p)] = d
			}
		}
		missing = empty()
	}
	return drafts, missing, next, nil
}

func pwStrings(v interface{}) []interface{} {
	var out []interface{}
	for _, x := range asListOrOne(v) {
		if s := strings.TrimSpace(toString(x)); s != "" && x != nil {
			out = append(out, s)
		}
	}
	return out
}

// pwDefault reads a parameter whose n8n default is an expression on $json.
func pwDefault(ec *execCtx, i int, it Item, name, jsonKey string) interface{} {
	v, _ := ec.param(i, name, nil)
	if s, ok := v.(string); v == nil || ok && strings.HasPrefix(s, "={{") {
		return it.JSON[jsonKey]
	}
	return v
}

func pwStatus(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	platform, _ := ec.str(i, "statusPlatform", "")
	id := strings.TrimSpace(toString(pwDefault(ec, i, it, "postId", "id")))
	q := url.Values{"id": {id}}
	if platform != "postwire" {
		q.Set("platform", platform)
	}
	v, err := c.do(ec, &restRequest{method: "GET", path: "/api/post/status", query: q})
	return restItems(v), err
}

func pwPost(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	_, op := ec.resourceOpBy(i, "post", map[string]string{"post": "publish"})
	mode := ""
	if ec.version == 1 {
		switch op {
		case "publish", "schedule":
			mode = "text"
		case "generateAndPublish":
			op, mode = "publish", "smart"
		}
	}
	pv, _ := ec.param(i, "platforms", nil)
	platforms := pwStrings(pv)
	opts := fieldsOf(ec, i, "options")
	if len(platforms) == 0 {
		return nil, permanentError{ec.node.Name + ": No network selected in 'Networks'"}
	}
	brandID := strings.TrimSpace(rlValue(firstNonNil(opts["brand"], opts["brandId"])))
	str := func(k string) string { return strings.TrimSpace(toString(opts[k])) }
	tz := firstNonEmpty(str("timezone"), "UTC")

	if op == "planWeek" {
		for _, p := range platforms {
			if pwMediaNeed[toString(p)] == "video" {
				return nil, permanentError{fmt.Sprintf("%s: %s cannot be in a week plan", ec.node.Name, p)}
			}
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, permanentError{fmt.Sprintf("%s: 'Timezone' is not a known time zone: %q", ec.node.Name, tz)}
		}
		_, off := time.Now().In(loc).Zone()
		topic, _ := ec.str(i, "topic", "")
		days, hour := 5.0, 10.0
		if f, ok := toFloat(firstNonNil(opts["days"], ec.params["days"])); ok {
			days = f
		}
		if f, ok := toFloat(firstNonNil(opts["hour"], ec.params["hour"])); ok {
			hour = f
		}
		body := map[string]interface{}{"topic": topic, "platforms": platforms, "days": days, "start_hour": hour, "timezone": tz, "tz_offset_minutes": -off / 60}
		if s := str("brandVoice"); s != "" {
			body["brand_voice"] = s
		}
		if brandID != "" {
			body["brand_id"] = brandID
		}
		v, err := c.do(ec, &restRequest{method: "POST", path: "/api/week", body: body})
		if err != nil {
			return nil, err
		}
		res := asMap(v)
		res["timezone"] = tz
		return []Item{{JSON: res}}, nil
	}

	// Media from a URL or an uploaded binary.
	source, _ := ec.str(i, "mediaSource", "none")
	if ec.version == 1 {
		source = "none"
		if u, _ := ec.str(i, "mediaUrl", ""); u != "" {
			source = "url"
		}
	}
	mediaURL, declared := "", firstNonEmpty(str("mediaType"), "auto")
	switch source {
	case "url":
		u, _ := ec.str(i, "mediaUrl", "")
		mediaURL = strings.TrimSpace(u)
	case "binary":
		prop, _ := ec.str(i, "binaryPropertyName", "data")
		hosted, err := pwUpload(ec, i, it, c, prop)
		if err != nil {
			return nil, err
		}
		mediaURL = toString(hosted["media_url"])
		_, mt, _, _ := binaryOf(it, prop)
		ct := firstNonEmpty(toString(hosted["content_type"]), mt)
		if declared == "auto" {
			if strings.HasPrefix(ct, "video/") {
				declared = "video"
			} else if strings.HasPrefix(ct, "image/") {
				declared = "image"
			}
		}
	}
	genBody := func() map[string]interface{} {
		prompt, _ := ec.str(i, "prompt", "")
		b := map[string]interface{}{"prompt": prompt, "platforms": platforms}
		if mediaURL != "" {
			b["media_url"] = mediaURL
		}
		if s := str("brandVoice"); s != "" {
			b["brand_voice"] = s
		}
		return b
	}
	if op == "generate" {
		drafts, missing, next, err := pwGenerate(ec, c, genBody())
		if err != nil {
			return nil, err
		}
		out := map[string]interface{}{"drafts": drafts, "platforms": platforms}
		if len(missing) > 0 {
			out["missing"] = missing
		}
		for k, v := range next {
			out[k] = v
		}
		if mediaURL != "" {
			out["media_url"] = mediaURL
		}
		var preview []string
		for _, p := range platforms {
			if d := asMap(drafts[toString(p)]); drafts[toString(p)] != nil {
				title := ""
				if t := toString(d["title"]); t != "" {
					title = t + "\n"
				}
				preview = append(preview, "*"+toString(p)+"*\n"+title+toString(d["text"]))
			}
		}
		out["preview"] = strings.Join(preview, "\n\n")
		return []Item{{JSON: out}}, nil
	}

	if mode == "" {
		mode, _ = ec.str(i, "contentMode", "smart")
	}
	kind := pwClassify(mediaURL, declared)
	wantsVideo := false
	for _, p := range platforms {
		need := pwMediaNeed[toString(p)]
		if need == "video" && (kind == "image" || kind == "none") || need == "any" && kind == "none" {
			return nil, permanentError{fmt.Sprintf("%s: %s will not accept a post without %s", ec.node.Name, p, map[bool]string{true: "a video", false: "an image or video"}[need == "video"])}
		}
		wantsVideo = wantsVideo || need == "video"
	}
	treatAsVideo := kind == "video" || kind == "unknown" && wantsVideo
	var perPlatform map[string]interface{}
	text := ""
	switch mode {
	case "smart":
		drafts, _, _, err := pwGenerate(ec, c, genBody())
		if err != nil {
			return nil, err
		}
		perPlatform = drafts
	case "drafts":
		raw := pwDefault(ec, i, it, "drafts", "drafts")
		if s, ok := raw.(string); ok {
			if err := jsonUnmarshal([]byte(s), &raw); err != nil {
				return nil, permanentError{ec.node.Name + ": 'Drafts' is not valid JSON"}
			}
		}
		obj := asMap(raw)
		if d, ok := obj["drafts"].(map[string]interface{}); ok {
			obj = d
		}
		perPlatform = obj
		var missing []string
		for _, p := range platforms {
			if toString(asMap(obj[toString(p)])["text"]) == "" {
				missing = append(missing, toString(p))
			}
		}
		if len(missing) > 0 && mediaURL == "" {
			return nil, permanentError{fmt.Sprintf("%s: No draft for %s", ec.node.Name, strings.Join(missing, ", "))}
		}
	default:
		text, _ = ec.str(i, "text", "")
	}

	// Threads, X reply and first comments.
	xThread, thread := pwStrings(opts["xThread"]), pwStrings(opts["thread"])
	xReply, first := str("xReply"), str("firstComment")
	threadNets := map[string]bool{"x": true, "bluesky": true, "mastodon": true}
	for _, p := range platforms {
		ps := toString(p)
		if !threadNets[ps] {
			continue
		}
		var chain interface{}
		switch {
		case ps == "x" && len(xThread) > 0:
			chain = xThread
		case len(thread) > 0:
			chain = thread
		case toBool(opts["splitThread"]):
			chain = true
		default:
			continue
		}
		if perPlatform == nil {
			perPlatform = map[string]interface{}{}
		}
		d := map[string]interface{}{}
		for k, x := range asMap(perPlatform[ps]) {
			d[k] = x
		}
		if toString(d["text"]) == "" && text != "" {
			d["text"] = text
		}
		d["thread"] = chain
		perPlatform[ps] = d
	}
	netOpts := map[string]interface{}{}
	for _, p := range platforms {
		ps := toString(p)
		if ps == "x" && xReply != "" {
			netOpts["x"] = map[string]interface{}{"reply": xReply}
		} else if first != "" && pwFirstComment[ps] {
			netOpts[ps] = map[string]interface{}{"first_comment": first}
		}
	}
	body := map[string]interface{}{"platforms": platforms}
	if perPlatform != nil {
		body["per_platform"] = perPlatform
	}
	set := func(k string, v string) {
		if v != "" {
			body[k] = v
		}
	}
	set("text", text)
	if treatAsVideo {
		set("video_url", mediaURL)
	} else {
		set("photo_url", mediaURL)
	}
	set("title", str("title"))
	set("privacy", str("privacy"))
	set("brand_id", brandID)
	if len(netOpts) > 0 {
		body["options"] = netOpts
	}

	if op == "schedule" {
		when, _ := ec.str(i, "when", "time")
		if when == "nextSlot" {
			body["run_at"] = "next_slot"
			set("timezone", str("timezone"))
		} else {
			at, _ := ec.param(i, "runAt", "")
			if restEmpty(at) {
				return nil, permanentError{ec.node.Name + ": 'Publish At' is empty"}
			}
			iso, err := restConv("iso", at)
			if err != nil {
				return nil, permanentError{ec.node.Name + ": 'Publish At' is not a valid date"}
			}
			body["run_at"] = iso
		}
		set("label", str("label"))
		v, err := c.do(ec, &restRequest{method: "POST", path: "/api/schedule", body: body})
		if err != nil {
			return nil, err
		}
		res := asMap(v)
		row, ok := res["scheduled"].(map[string]interface{})
		if res["status"] == "pending_approval" || !ok {
			res["drafts"] = perPlatform
			return []Item{{JSON: res}}, nil
		}
		out := map[string]interface{}{}
		for k, x := range row {
			out[k] = x
		}
		for _, k := range []string{"slot", "held_networks", "held_message", "x_credits"} {
			if res[k] != nil {
				out[k] = res[k]
			}
		}
		if toBool(res["held"]) {
			out["held"], out["code"], out["message"], out["upgrade_url"] = true, res["code"], res["message"], res["upgrade_url"]
		}
		out["drafts"] = perPlatform
		return []Item{{JSON: out}}, nil
	}

	if idem := str("idempotencyKey"); idem != "" {
		if len(idem) > 200 {
			idem = idem[:200]
		}
		body["idempotency_key"] = idem
	}
	v, err := c.do(ec, &restRequest{method: "POST", path: "/api/post", body: body})
	if err != nil {
		return nil, err
	}
	res := asMap(v)
	results := asList(res["results"])
	if res["results"] == nil {
		results = []interface{}{res}
	}
	var out []Item
	failed := 0
	for _, r := range results {
		m := map[string]interface{}{}
		for k, x := range asMap(r) {
			m[k] = x
		}
		if res["status"] == "pending_approval" {
			appr := asMap(res["approval"])
			m["status"], m["id"], m["schedule_id"], m["approval_id"] = "pending_approval", res["id"], firstNonNil(res["schedule_id"], res["id"]), res["approval_id"]
			m["review_url"], m["reasons"], m["message"] = appr["review_url"], appr["reasons"], firstNonNil(res["message_for_user"], res["message"])
		} else if res["notice"] != nil {
			m["notice"] = res["notice"]
		}
		if m["ok"] == false {
			failed++
		}
		out = append(out, Item{JSON: m})
	}
	if len(results) > 0 && failed == len(results) && res["status"] != "pending_approval" {
		var why []string
		for _, r := range results {
			m := asMap(r)
			why = append(why, fmt.Sprintf("%v: %v", m["platform"], firstNonNil(m["error"], firstNonNil(m["code"], "failed"))))
		}
		return nil, permanentError{fmt.Sprintf("%s: PostWire published to none of the %d selected network(s): %s", ec.node.Name, len(results), strings.Join(why, "; "))}
	}
	return out, nil
}

var pwFirstComment = map[string]bool{"linkedin": true, "x": true, "bluesky": true, "mastodon": true}
