package n8n

import (
	"encoding/base64"
	"regexp"
	"strings"
)

// Decodo Web Scraping API (community node @decodo/n8n-nodes-decodo.decodo).
// The operation is the scraping target; a basic-auth token (base64
// "user:pass") uses the Scraper API, any other key the Data API.

var decodoPrintable = regexp.MustCompile(`^[\x20-\x7e]+$`)

func init() {
	op := &restOp{run: decodoScrape}
	ops := map[string]*restOp{}
	// youtube_transcript is the target of older package versions.
	for _, t := range []string{"universal", "google_search", "amazon", "youtube_subtitles", "youtube_transcript"} {
		ops["/"+t] = op
	}
	registerREST("@decodo/n8n-nodes-decodo.decodo", &restService{
		defOps: map[string]string{"": "universal"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "decodoApi", nil)
			if err != nil {
				return nil, err
			}
			tok := strings.TrimSpace(credString(c, "token"))
			base, auth := "https://data.decodo.com/v1", "Bearer "+tok
			if d, err := base64.StdEncoding.DecodeString(tok); err == nil && decodoPrintable.Match(d) && strings.Contains(string(d), ":") {
				base, auth = "https://scraper-api.decodo.com/v2", "Basic "+tok
			}
			return &restClient{base: ec.apiBase("decodo", base), headers: map[string]string{"Authorization": auth, "x-integration": "n8n"}}, nil
		},
		ops: ops,
	})
}

func decodoScrape(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	a := &cmtyArgs{ec: ec, i: i}
	_, target := ec.resourceOpBy(i, "", map[string]string{"": "universal"})
	b := map[string]interface{}{"target": target}
	set := func(k string, v interface{}) {
		if !restEmpty(v) && v != false {
			b[k] = v
		}
	}
	set("url", a.get("url", nil))
	set("query", a.get("query", nil))
	set("query", a.get("video_id", nil))
	// Stored parameters are sent as set; unset ones take the default n8n
	// shows for the target (hidden ones fall back to false/empty).
	headlessDef := target == "universal" || target == "google_search"
	parseDef := target == "google_search" || target == "amazon"
	limitDef, originDef := interface{}(nil), interface{}(nil)
	if target == "google_search" {
		limitDef = 10.0
	}
	if target == "youtube_subtitles" {
		originDef = "auto_generated"
	}
	if a.bool("headless", headlessDef) {
		b["headless"] = "html"
	}
	set("parse", a.bool("parse", parseDef))
	set("geo", a.get("geo", nil))
	set("locale", a.get("locale", nil))
	set("subtitle_origin", a.get("subtitle_origin", originDef))
	set("language_code", a.get("language_code", nil))
	set("limit", a.get("results_limit", limitDef))
	set("markdown", a.bool("markdown", false))
	if a.err != nil {
		return nil, a.err
	}
	v, err := cmtyCall(ec, c, "POST", "/scrape", nil, b)
	if err != nil {
		return nil, err
	}
	if m := asMap(v); toString(m["status"]) == "failed" {
		code := ""
		if sc := toString(m["status_code"]); sc != "" {
			code = " (" + sc + ")"
		}
		return nil, permanentError{"Decodo could not scrape the target" + code + ": " + firstNonEmpty(toString(m["message"]), "no reason given")}
	}
	return restItems(v), nil
}
