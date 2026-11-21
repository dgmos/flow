package n8n

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Per-service webhook trigger behavior, following n8n's trigger nodes.

const hookBase = "n8n-nodes-base."

// received answers 200 without an execution, as n8n does for ignored calls.
func received() hookResult {
	return jsonReply(map[string]interface{}{"message": "Webhook call received"})
}

func unauthorized() hookResult {
	return reply(http.StatusUnauthorized, "text/plain", []byte("Unauthorized"))
}

// fresh checks a Unix timestamp (seconds or milliseconds) against maxAge.
func fresh(ts string, maxAge time.Duration) bool {
	f, err := strconv.ParseFloat(strings.TrimSpace(ts), 64)
	if err != nil || math.IsNaN(f) {
		return false
	}
	if f > 1e10 {
		f /= 1000
	}
	return math.Abs(float64(time.Now().Unix())-f) <= maxAge.Seconds()
}

func eqConst(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// sigParts parses "t=…,v1=…" signature headers (last occurrence wins).
func sigParts(h string) map[string]string {
	out := map[string]string{}
	for _, p := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(p, "=")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func (q *hookRequest) secret() string {
	return firstNonEmpty(toString(q.state["webhookSecret"]), toString(q.state["secret"]))
}

func (q *hookRequest) credOptional(types ...string) map[string]interface{} {
	for _, t := range types {
		if c, err := q.cred(t); err == nil {
			return c
		}
	}
	return map[string]interface{}{}
}

// jsArray turns an array body into one item per element (returnJsonArray).
func jsArray(v interface{}) []interface{} {
	if l, ok := v.([]interface{}); ok {
		return l
	}
	return []interface{}{v}
}

func hexHMAC256(secret string, msg []byte) string {
	return hex.EncodeToString(hmacSum(sha256.New, secret, msg))
}

func b64HMAC256(secret string, msg []byte) string {
	return base64.StdEncoding.EncodeToString(hmacSum(sha256.New, secret, msg))
}

func init() {
	serviceHooks[hookBase+"slackTrigger"] = serviceHook{credTypes: []string{"slackApi"}, handle: slackHook}
	serviceHooks[hookBase+"jotFormTrigger"] = serviceHook{credTypes: []string{"jotFormApi"}, handle: jotFormHook}
	serviceHooks[hookBase+"shopifyTrigger"] = serviceHook{credTypes: []string{"shopifyApi", "shopifyAccessTokenApi", "shopifyOAuth2Api"}, handle: shopifyHook}
	serviceHooks[hookBase+"typeformTrigger"] = serviceHook{credTypes: []string{"typeformApi", "typeformOAuth2Api"}, handle: typeformHook}
	serviceHooks[hookBase+"githubTrigger"] = serviceHook{credTypes: []string{"githubApi", "githubOAuth2Api"}, handle: githubHook}
	serviceHooks[hookBase+"stripeTrigger"] = serviceHook{credTypes: []string{"stripeApi"}, handle: stripeHook}
	serviceHooks[hookBase+"hubspotTrigger"] = serviceHook{methods: []string{"POST", "GET"}, credTypes: []string{"hubspotDeveloperApi"}, handle: hubspotHook}
	serviceHooks[hookBase+"calendlyTrigger"] = serviceHook{credTypes: []string{"calendlyApi", "calendlyOAuth2Api"}, handle: timestampedHook("calendly-webhook-signature")}
	serviceHooks[hookBase+"wooCommerceTrigger"] = serviceHook{credTypes: []string{"wooCommerceApi"}, handle: wooCommerceHook}
	serviceHooks[hookBase+"pipedriveTrigger"] = serviceHook{credTypes: []string{"pipedriveApi", "pipedriveOAuth2Api"}, handle: pipedriveHook}
	serviceHooks[hookBase+"jiraTrigger"] = serviceHook{credTypes: []string{"jiraSoftwareCloudApi", "jiraSoftwareServerApi", "jiraSoftwareServerPatApi"}, handle: jiraHook}
	serviceHooks[hookBase+"webflowTrigger"] = serviceHook{credTypes: []string{"webflowApi", "webflowOAuth2Api"}, handle: webflowHook}
	serviceHooks[hookBase+"clickUpTrigger"] = serviceHook{credTypes: []string{"clickUpApi", "clickUpOAuth2Api"}, handle: clickUpHook}
	serviceHooks[hookBase+"linearTrigger"] = serviceHook{credTypes: []string{"linearApi", "linearOAuth2Api"}, handle: linearHook}
	serviceHooks[hookBase+"gitlabTrigger"] = serviceHook{credTypes: []string{"gitlabApi", "gitlabOAuth2Api"}, handle: gitlabHook}
	serviceHooks[hookBase+"trelloTrigger"] = serviceHook{methods: []string{"POST", "HEAD"}, credTypes: []string{"trelloApi"}, handle: trelloHook}
	serviceHooks[hookBase+"mailchimpTrigger"] = serviceHook{methods: []string{"POST", "GET"}, credTypes: []string{"mailchimpApi", "mailchimpOAuth2Api"}, handle: mailchimpHook}
	serviceHooks[hookBase+"calTrigger"] = serviceHook{credTypes: []string{"calApi"}, handle: calHook}
	serviceHooks[hookBase+"gumroadTrigger"] = serviceHook{credTypes: []string{"gumroadApi", "gumroadOAuth2Api"}, handle: func(q *hookRequest) (hookResult, error) {
		return ok(jsArray(q.parsed)...), nil
	}}
	serviceHooks[hookBase+"facebookLeadAdsTrigger"] = serviceHook{methods: []string{"POST", "GET"}, credTypes: []string{"facebookLeadAdsOAuth2Api"}, handle: facebookLeadAdsHook}
}

// ---------------------------------------------------------------- Slack

func slackHook(q *hookRequest) (hookResult, error) {
	ts := q.r.Header.Get("X-Slack-Request-Timestamp")
	if ts == "" || !fresh(ts, 5*time.Minute) {
		return unauthorized(), nil
	}
	c := q.credOptional("slackApi")
	if sec := credString(c, "signatureSecret"); sec != "" {
		want := "v0=" + hexHMAC256(sec, []byte("v0:"+ts+":"+string(q.body)))
		if !eqConst(want, q.r.Header.Get("X-Slack-Signature")) {
			return unauthorized(), nil
		}
	}
	body := q.json()
	if body["type"] == "url_verification" {
		return jsonReply(map[string]interface{}{"challenge": body["challenge"]}), nil
	}
	ev := asMap(body["event"])
	if ev == nil {
		return received(), nil
	}
	typ := toString(ev["type"])
	selected := q.events("trigger", false)
	if !selected("file_share") && !selected("any_event") && !selected(typ) {
		return received(), nil
	}
	ch := firstNonEmpty(toString(ev["channel"]), firstNonEmpty(toString(asMap(ev["item"])["channel"]), toString(ev["channel_id"])))
	if typ != "team_join" && typ != "app_home_opened" && !toBool(q.param("watchWorkspace", false)) && ch != rlValue(q.param("channelId", "")) {
		return received(), nil
	}
	opts := asMap(q.param("options", nil))
	user := firstNonEmpty(toString(ev["user"]), toString(asMap(ev["message"])["user"]))
	for _, u := range listOrCSV(opts["userIds"]) {
		if u == user && user != "" {
			return received(), nil
		}
	}
	if typ == "reaction_added" {
		var emojis []string
		for _, e := range strings.Split(toString(opts["reactionEmojis"]), ",") {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				emojis = append(emojis, e)
			}
		}
		if len(emojis) > 0 {
			found := false
			for _, e := range emojis {
				found = found || e == strings.ToLower(toString(ev["reaction"]))
			}
			if !found {
				return received(), nil
			}
		}
	}
	tok := firstNonEmpty(credString(c, "accessToken"), accessToken(c))
	slack := func(method string, args url.Values) map[string]interface{} {
		out, _, err := q.ec.apiCall("GET", q.ec.apiBase("slack", "https://slack.com/api")+"/"+method+"?"+args.Encode(), map[string]string{"Authorization": "Bearer " + tok}, nil, "")
		if err != nil {
			return nil
		}
		return out
	}
	if toBool(opts["resolveIds"]) && tok != "" {
		if u, isMap := ev["user"].(map[string]interface{}); isMap && typ == "team_join" {
			ev["user_resolved"] = u["name"]
		} else if user := toString(ev["user"]); user != "" {
			ev["user_resolved"] = asMap(asMap(slack("users.info", url.Values{"user": {user}}))["user"])["name"]
			if typ == "reaction_added" && toString(ev["item_user"]) != "" {
				ev["item_user_resolved"] = asMap(asMap(slack("users.info", url.Values{"user": {toString(ev["item_user"])}}))["user"])["name"]
			}
		}
		if ch != "" {
			ev["channel_resolved"] = asMap(asMap(slack("conversations.info", url.Values{"channel": {ch}}))["channel"])["name"]
		}
	}
	item := map[string]interface{}{"json": ev}
	if toBool(q.param("downloadFiles", false)) && ev["subtype"] == "file_share" && (selected("file_share") || selected("any_event")) {
		bin := map[string]interface{}{}
		for k, f := range asList(ev["files"]) {
			fm := asMap(f)
			u, err := url.Parse(toString(fm["url_private_download"]))
			if err != nil || u.Scheme != "https" || (u.Host != "files.slack.com" && u.Host != "files.slack-gov.com") || !strings.HasPrefix(u.Path, "/files-pri/") {
				continue
			}
			data, _, err := q.ec.downloadWith(u.String(), map[string]string{"Authorization": "Bearer " + tok})
			if err != nil {
				return hookResult{}, err
			}
			bin["file_"+strconv.Itoa(k)] = binaryData(data, toString(fm["mimetype"]), toString(fm["name"]))
		}
		if len(bin) > 0 {
			item["binary"] = bin
		}
	}
	return hookResult{run: true, items: []interface{}{item}}, nil
}

// ---------------------------------------------------------------- JotForm

func jotFormHook(q *hookRequest) (hookResult, error) {
	data := map[string]interface{}{}
	if mt, params, err := mime.ParseMediaType(q.r.Header.Get("Content-Type")); err == nil && strings.HasPrefix(mt, "multipart/") {
		form, err := multipart.NewReader(strings.NewReader(string(q.body)), params["boundary"]).ReadForm(32 << 20)
		if err != nil {
			return hookResult{}, err
		}
		for k, v := range form.Value {
			if len(v) == 1 {
				data[k] = v[0]
			} else {
				data[k] = v
			}
		}
	} else {
		data = q.json()
	}
	var raw map[string]interface{}
	_ = json.Unmarshal([]byte(toString(data["rawRequest"])), &raw)
	resolve, only := toBool(q.param("resolveData", true)), toBool(q.param("onlyAnswers", true))
	if !resolve {
		if only {
			return ok(raw), nil
		}
		data["rawRequest"] = raw
		return ok(data), nil
	}
	c, err := q.cred("jotFormApi")
	if err != nil {
		return hookResult{}, err
	}
	host := "https://api.jotform.com"
	if r := credString(c, "apiDomain"); r != "" {
		host = "https://" + strings.TrimPrefix(r, "https://")
	}
	res, _, err := q.ec.apiCall("GET", host+"/form/"+url.PathEscape(q.str("form", ""))+"/questions", map[string]string{"APIKEY": credString(c, "apiKey")}, nil, "")
	if err != nil {
		return hookResult{}, err
	}
	names := map[string]string{}
	for _, v := range asMap(res["content"]) {
		m := asMap(v)
		names[toString(m["name"])] = toString(m["text"])
	}
	out := map[string]interface{}{}
	for k, v := range raw {
		if i := strings.IndexByte(k, '_'); i >= 0 {
			if text, ok := names[k[i+1:]]; ok {
				out[text] = v
			}
		}
	}
	if only {
		return ok(out), nil
	}
	data["rawRequest"] = out
	return ok(data), nil
}

// ---------------------------------------------------------------- Shopify

func shopifyHook(q *hookRequest) (hookResult, error) {
	h := q.r.Header
	topic, sig := h.Get("X-Shopify-Topic"), h.Get("X-Shopify-Hmac-Sha256")
	if topic == "" || sig == "" || h.Get("X-Shopify-Shop-Domain") == "" || h.Get("X-Shopify-Api-Version") == "" {
		return received(), nil
	}
	var secret string
	switch q.str("authentication", "apiKey") {
	case "accessToken":
		secret = credString(q.credOptional("shopifyAccessTokenApi"), "appSecretKey")
	case "oAuth2":
		secret = credString(q.credOptional("shopifyOAuth2Api"), "clientSecret")
	default:
		secret = credString(q.credOptional("shopifyApi"), "sharedSecret")
	}
	if b64HMAC256(secret, q.body) != sig || topic != q.str("topic", "") {
		return received(), nil
	}
	return ok(jsArray(q.parsed)...), nil
}

// ---------------------------------------------------------------- Typeform

func typeformHook(q *hookRequest) (hookResult, error) {
	if sec := q.secret(); sec != "" {
		if !eqConst("sha256="+b64HMAC256(sec, q.body), q.r.Header.Get("Typeform-Signature")) {
			return unauthorized(), nil
		}
	}
	body := q.json()
	fr := asMap(body["form_response"])
	if fr == nil || fr["definition"] == nil || fr["answers"] == nil {
		return hookResult{}, fmt.Errorf("expected definition/answers data in form_response")
	}
	simplify, only := toBool(q.param("simplifyAnswers", true)), toBool(q.param("onlyAnswers", true))
	answers := asList(fr["answers"])
	if simplify {
		titles := map[string]string{}
		for _, f := range asList(asMap(fr["definition"])["fields"]) {
			fm := asMap(f)
			titles[toString(fm["id"])] = strings.NewReplacer("{{", "[", "}}", "]").Replace(toString(fm["title"]))
		}
		conv := map[string]interface{}{}
		for _, a := range answers {
			am := asMap(a)
			v := am[toString(am["type"])]
			if vm, isMap := v.(map[string]interface{}); isMap {
				if l, has := vm["label"]; has {
					v = l
				} else if l, has := vm["labels"]; has {
					v = l
				}
			}
			conv[titles[toString(asMap(am["field"])["id"])]] = v
		}
		if only {
			return ok(conv), nil
		}
		fr["answers"] = conv
		return ok(body), nil
	}
	if only {
		if toFloatDefault(q.state["__version"], 1.1) >= 1.1 {
			out := map[string]interface{}{}
			for _, a := range answers {
				out[toString(asMap(asMap(a)["field"])["id"])] = a
			}
			return ok(out), nil
		}
		return ok(map[string]interface{}(nil)), nil
	}
	return ok(body), nil
}

// ---------------------------------------------------------------- GitHub

func githubHook(q *hookRequest) (hookResult, error) {
	sec := q.secret()
	sig := q.r.Header.Get("X-Hub-Signature-256")
	if sec == "" || !strings.HasPrefix(sig, "sha256=") || !eqConst(strings.TrimPrefix(sig, "sha256="), hexHMAC256(sec, q.body)) {
		return unauthorized(), nil
	}
	body := q.json()
	if _, hook := body["hook_id"]; hook && body["action"] == nil {
		return reply(http.StatusOK, "text/plain", []byte("OK")), nil
	}
	return ok(q.fullItem()), nil
}

// ---------------------------------------------------------------- Stripe

func stripeHook(q *hookRequest) (hookResult, error) {
	sec := firstNonEmpty(q.secret(), credString(q.credOptional("stripeApi"), "signatureSecret"))
	if sec != "" {
		p := sigParts(q.r.Header.Get("Stripe-Signature"))
		if p["t"] == "" || !fresh(p["t"], 5*time.Minute) || !eqConst(hexHMAC256(sec, []byte(p["t"]+"."+string(q.body))), p["v1"]) {
			return unauthorized(), nil
		}
	}
	typ := toString(q.json()["type"])
	if typ == "" || !q.events("events", false)(typ) {
		return received(), nil
	}
	return ok(jsArray(q.parsed)...), nil
}

// ---------------------------------------------------------------- HubSpot

func hubspotHook(q *hookRequest) (hookResult, error) {
	sig := q.r.Header.Get("X-Hubspot-Signature")
	if sig == "" {
		return received(), nil
	}
	sum := sha256.Sum256([]byte(credString(q.credOptional("hubspotDeveloperApi"), "clientSecret") + compactJSON(q.body)))
	if hex.EncodeToString(sum[:]) != sig {
		return received(), nil
	}
	var items []interface{}
	for _, e := range jsArray(q.parsed) {
		em := cloneJSON(asMap(e))
		st := toString(em["subscriptionType"])
		for _, k := range []string{"contact", "company", "deal", "ticket", "conversation"} {
			if strings.Contains(st, k) {
				em[k+"Id"] = em["objectId"]
			}
		}
		delete(em, "objectId")
		items = append(items, em)
	}
	return ok(items...), nil
}

// compactJSON re-serializes JSON the way JSON.stringify does (no spaces),
// for services that sign the stringified body.
func compactJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// ---------------------------------------------------------------- Calendly, Mailchimp (t=…,v1=… HMAC)

func timestampedHook(header string) func(q *hookRequest) (hookResult, error) {
	return func(q *hookRequest) (hookResult, error) {
		if res, bad := verifyTimestamped(q, header); bad {
			return res, nil
		}
		return ok(jsArray(q.parsed)...), nil
	}
}

func verifyTimestamped(q *hookRequest, header string) (hookResult, bool) {
	sec := q.secret()
	if sec == "" {
		return hookResult{}, false
	}
	p := sigParts(q.r.Header.Get(header))
	if p["t"] == "" || !fresh(p["t"], 5*time.Minute) || !eqConst(hexHMAC256(sec, []byte(p["t"]+"."+string(q.body))), p["v1"]) {
		return unauthorized(), true
	}
	return hookResult{}, false
}

func mailchimpHook(q *hookRequest) (hookResult, error) {
	if q.r.Method == http.MethodGet {
		return reply(http.StatusOK, "", nil), nil
	}
	if res, bad := verifyTimestamped(q, "X-Mailchimp-Signature"); bad {
		return res, nil
	}
	body := q.json()
	if _, has := body["id"]; has {
		return received(), nil // n8n drops bodies with a top-level id
	}
	typ := toString(body["type"])
	if !q.events("events", false)(typ) && !q.events("sources", false)(typ) {
		return received(), nil
	}
	return ok(body), nil
}

// ---------------------------------------------------------------- WooCommerce

func wooCommerceHook(q *hookRequest) (hookResult, error) {
	if q.r.Header.Get("X-Wc-Webhook-Id") == "" {
		return received(), nil
	}
	sec := q.secret()
	if sec == "" {
		return hookResult{}, fmt.Errorf("WooCommerce Trigger: the webhook is not registered (no secret)")
	}
	if !eqConst(b64HMAC256(sec, q.body), q.r.Header.Get("X-Wc-Webhook-Signature")) {
		return unauthorized(), nil
	}
	return ok(jsArray(q.parsed)...), nil
}

// ---------------------------------------------------------------- Pipedrive

func pipedriveHook(q *hookRequest) (hookResult, error) {
	if q.str("incomingAuthentication", "none") == "basicAuth" {
		c, err := q.cred("httpBasicAuth")
		if err != nil {
			return reply(http.StatusInternalServerError, "text/plain", []byte("No authentication data defined on node!")), nil
		}
		u, p, has := q.r.BasicAuth()
		if !has {
			return reply(http.StatusUnauthorized, "text/plain", []byte("Authorization is required!")), nil
		}
		if !eqConst(u, credString(c, "user")) || !eqConst(p, credString(c, "password")) {
			return reply(http.StatusForbidden, "text/plain", []byte("Authorization data is wrong!")), nil
		}
	}
	return ok(jsArray(q.parsed)...), nil
}

// ---------------------------------------------------------------- Jira

func jiraHook(q *hookRequest) (hookResult, error) {
	query := map[string]interface{}{}
	for k, v := range q.r.URL.Query() {
		query[k] = v[0]
	}
	if q.str("incomingAuthentication", "none") == "queryAuth" || toBool(q.param("authenticateWebhook", false)) {
		c, err := q.cred("httpQueryAuth")
		if err != nil || credString(c, "name") == "" {
			return hookResult{status: http.StatusForbidden, contentType: "application/json", body: []byte(`{"message":"Auth settings are not valid, some data are missing"}`)}, nil
		}
		name := credString(c, "name")
		if !eqConst(toString(query[name]), base64.StdEncoding.EncodeToString([]byte(credString(c, "value")))) {
			return hookResult{status: http.StatusForbidden, contentType: "application/json", body: []byte(`{"message":"Provided authentication data is not valid"}`)}, nil
		}
		delete(query, name)
	}
	body := cloneJSON(q.json())
	for k, v := range query {
		body[k] = v
	}
	return ok(body), nil
}

// ---------------------------------------------------------------- Webflow

func webflowHook(q *hookRequest) (hookResult, error) {
	if toFloatDefault(q.state["__version"], 2) >= 2 {
		sec := credString(q.credOptional("webflowOAuth2Api"), "clientSecret")
		ts := q.r.Header.Get("X-Webflow-Timestamp")
		if sec == "" || ts == "" || strings.Trim(ts, "0123456789") != "" || !fresh(ts, 5*time.Minute) ||
			!eqConst(hexHMAC256(sec, []byte(ts+":"+string(q.body))), q.r.Header.Get("X-Webflow-Signature")) {
			return unauthorized(), nil
		}
	}
	return ok(jsArray(q.parsed)...), nil
}

// ---------------------------------------------------------------- ClickUp

func clickUpHook(q *hookRequest) (hookResult, error) {
	if hexHMAC256(q.secret(), []byte(compactJSON(q.body))) != q.r.Header.Get("X-Signature") {
		return received(), nil
	}
	return ok(jsArray(q.parsed)...), nil
}

// ---------------------------------------------------------------- Linear

func linearHook(q *hookRequest) (hookResult, error) {
	if ts, has := q.json()["webhookTimestamp"]; has && !fresh(toString(ts), time.Minute) {
		return unauthorized(), nil
	}
	if sec := credString(q.credOptional("linearApi", "linearOAuth2Api"), "signingSecret"); sec != "" {
		if !eqConst(hexHMAC256(sec, q.body), q.r.Header.Get("Linear-Signature")) {
			return unauthorized(), nil
		}
	}
	return ok(jsArray(q.parsed)...), nil
}

// ---------------------------------------------------------------- GitLab

func gitlabHook(q *hookRequest) (hookResult, error) {
	if sec := q.secret(); sec != "" && !eqConst(sec, q.r.Header.Get("X-Gitlab-Token")) {
		return unauthorized(), nil
	}
	return ok(q.fullItem()), nil
}

// ---------------------------------------------------------------- Trello

func trelloHook(q *hookRequest) (hookResult, error) {
	if q.r.Method == http.MethodHead {
		return reply(http.StatusOK, "", nil), nil
	}
	if sec := credString(q.credOptional("trelloApi"), "oauthSecret"); sec != "" {
		cb := toString(q.state["__url"])
		want := base64.StdEncoding.EncodeToString(hmacSum(sha1.New, sec, []byte(string(q.body)+cb)))
		if !eqConst(want, q.r.Header.Get("X-Trello-Webhook")) {
			return unauthorized(), nil
		}
	}
	return ok(jsArray(q.parsed)...), nil
}

// ---------------------------------------------------------------- Cal.com

func calHook(q *hookRequest) (hookResult, error) {
	if sec := q.secret(); sec != "" && !eqConst(hexHMAC256(sec, q.body), q.r.Header.Get("X-Cal-Signature-256")) {
		return unauthorized(), nil
	}
	body := q.json()
	payload, isMap := body["payload"].(map[string]interface{})
	if !isMap {
		return ok(body), nil
	}
	out := map[string]interface{}{"triggerEvent": body["triggerEvent"], "createdAt": body["createdAt"]}
	for k, v := range payload {
		out[k] = v
	}
	return ok(out), nil
}

// ---------------------------------------------------------------- Facebook Lead Ads

func facebookLeadAdsHook(q *hookRequest) (hookResult, error) {
	qs := q.r.URL.Query()
	if q.r.Method == http.MethodGet {
		if ch := qs.Get("hub.challenge"); ch != "" && qs.Get("hub.verify_token") == toString(q.state["__nodeId"]) {
			return reply(http.StatusOK, "text/plain", []byte(ch)), nil
		}
		return received(), nil
	}
	c := q.credOptional("facebookLeadAdsOAuth2Api")
	if "sha256="+hexHMAC256(credString(c, "clientSecret"), q.body) != q.r.Header.Get("X-Hub-Signature-256") {
		return received(), nil
	}
	body := q.json()
	if body["object"] != "page" {
		return received(), nil
	}
	page, form := rlValue(q.param("page", "")), rlValue(q.param("form", ""))
	simplify := toBool(asMap(q.param("options", nil))["simplifyOutput"]) || asMap(q.param("options", nil))["simplifyOutput"] == nil
	tok := accessToken(c)
	graph := func(path string, fields string) (map[string]interface{}, error) {
		out, _, err := q.ec.apiCall("GET", q.ec.apiBase("facebookGraph", "https://graph.facebook.com/v21.0")+"/"+url.PathEscape(path)+"?fields="+fields, map[string]string{"Authorization": "Bearer " + tok}, nil, "")
		return out, err
	}
	var items []interface{}
	for _, e := range asList(body["entry"]) {
		for _, ch := range asList(asMap(e)["changes"]) {
			cm := asMap(ch)
			v := asMap(cm["value"])
			if cm["field"] != "leadgen" || toString(v["page_id"]) != page || toString(v["form_id"]) != form {
				continue
			}
			lead, err := graph(toString(v["leadgen_id"]), "field_data,created_time,ad_id,ad_name,adset_id,adset_name,form_id")
			if err != nil {
				return hookResult{}, err
			}
			f, err := graph(toString(v["form_id"]), "id,name,locale,status,page,questions")
			if err != nil {
				return hookResult{}, err
			}
			ad := map[string]interface{}{"id": lead["ad_id"], "name": lead["ad_name"]}
			adset := map[string]interface{}{"id": lead["adset_id"], "name": lead["adset_name"]}
			if simplify {
				data := map[string]interface{}{}
				for _, fd := range asList(lead["field_data"]) {
					fm := asMap(fd)
					var first interface{}
					if l := asList(fm["values"]); len(l) > 0 {
						first = l[0]
					}
					data[toString(fm["name"])] = first
				}
				items = append(items, map[string]interface{}{"id": lead["id"], "data": data,
					"form": map[string]interface{}{"id": f["id"], "name": f["name"], "locale": f["locale"], "status": f["status"]},
					"ad":   ad, "adset": adset, "page": f["page"], "created_time": lead["created_time"]})
			} else {
				items = append(items, map[string]interface{}{"id": lead["id"], "field_data": lead["field_data"], "form": f, "ad": ad, "adset": adset,
					"page": f["page"], "created_time": lead["created_time"], "event": v})
			}
		}
	}
	if len(items) == 0 {
		return received(), nil
	}
	return ok(items...), nil
}
