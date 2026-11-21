package n8n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/url"
	"strings"
	"time"
)

// youTubeVideoParts is what part "*" expands to for video get.
const youTubeVideoParts = "contentDetails,id,liveStreamingDetails,localizations,player,recordingDetails,snippet,statistics,status,topicDetails"

// youTubeVideoOps are the supported operations of the YouTube node's video
// resource (the one used by templates).
var youTubeVideoOps = map[string]bool{"getAll": true, "get": true, "update": true, "upload": true, "delete": true}

func youTubeNode(ec *execCtx) ([][]Item, error) {
	h, err := ec.googleAuth("youtube", "youTubeOAuth2Api")
	if err != nil {
		return nil, err
	}
	root := ec.apiBase("youtube", "https://www.googleapis.com")
	base := root + "/youtube/v3"
	call := func(method, path string, q url.Values, body interface{}) (map[string]interface{}, error) {
		u := base + path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		res, _, err := ec.apiCall(method, u, h, body, "")
		return res, err
	}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		r, o := ec.resourceOpBy(i, "channel", map[string]string{"channel": "getAll", "playlist": "getAll", "playlistItem": "add", "video": "getAll", "videoCategory": "getAll"})
		if r != "video" {
			op := youTubeOther.op(r, o)
			if op == nil {
				return nil, permanentError{fmt.Sprintf("YouTube: %s/%s is not supported", r, o)}
			}
			return youTubeOther.exec(ec, i, op, &restClient{base: base, headers: h})
		}
		if !youTubeVideoOps[o] {
			return nil, permanentError{fmt.Sprintf("YouTube: %s/%s is not supported", r, o)}
		}
		switch o {
		case "getAll":
			q := url.Values{"part": {"snippet"}, "type": {"video"}}
			filters := asMap(mustParam(ec, "filters", map[string]interface{}{}))
			opts := asMap(mustParam(ec, "options", map[string]interface{}{}))
			if len(filters) == 0 {
				q.Set("forMine", "true")
			}
			for prefix, m := range map[string]map[string]interface{}{"options": opts, "filters": filters} {
				for k := range m {
					v, err := ec.param(i, prefix+"."+k, nil)
					if err != nil {
						return nil, err
					}
					if s := toString(v); s != "" {
						q.Set(k, youTubeTime(k, s))
					}
				}
			}
			all, _ := ec.boolean(i, "returnAll", false)
			limit, _ := ec.num(i, "limit", 25)
			var out []Item
			for {
				if !all {
					q.Set("maxResults", fmt.Sprint(int(limit)-len(out)))
				} else {
					q.Set("maxResults", "50")
				}
				res, err := call("GET", "/search", q, nil)
				if err != nil {
					return nil, err
				}
				for _, v := range asList(res["items"]) {
					out = append(out, Item{JSON: asMap(v)})
				}
				next := toString(res["nextPageToken"])
				if next == "" || (!all && len(out) >= int(limit)) || len(out) >= 5000 {
					break
				}
				q.Set("pageToken", next)
			}
			return out, nil
		case "get":
			id, _ := ec.str(i, "videoId", "")
			parts := "*"
			if v, ok := ec.raw("part"); ok {
				var ps []string
				for _, p := range asList(v) {
					ps = append(ps, toString(p))
				}
				if len(ps) > 0 {
					parts = strings.Join(ps, ",")
				}
			}
			if parts == "*" {
				parts = youTubeVideoParts
			}
			res, err := call("GET", "/videos", url.Values{"id": {id}, "part": {parts}}, nil)
			if err != nil {
				return nil, err
			}
			items := asList(res["items"])
			if len(items) == 0 {
				return nil, permanentError{fmt.Sprintf("YouTube: video %q not found", id)}
			}
			return []Item{{JSON: asMap(items[0])}}, nil
		case "delete":
			id, _ := ec.str(i, "videoId", "")
			if _, err := call("DELETE", "/videos", url.Values{"id": {id}}, nil); err != nil {
				return nil, err
			}
			return one(map[string]interface{}{"success": true}), nil
		case "update", "upload":
			fieldsKey := "updateFields"
			if o == "upload" {
				fieldsKey = "options"
			}
			title, _ := ec.str(i, "title", "")
			snippet := map[string]interface{}{"title": title}
			if c, _ := ec.str(i, "categoryId", ""); c != "" {
				snippet["categoryId"] = c
			}
			status, recording := map[string]interface{}{}, map[string]interface{}{}
			q := url.Values{"part": {"snippet,status,recordingDetails"}}
			fields := asMap(mustParam(ec, fieldsKey, map[string]interface{}{}))
			for k := range fields {
				v, err := ec.param(i, fieldsKey+"."+k, nil)
				if err != nil {
					return nil, err
				}
				switch k {
				case "description", "defaultLanguage":
					snippet[k] = toString(v)
				case "tags":
					var tags []string
					for _, t := range strings.Split(toString(v), ",") {
						if t = strings.TrimSpace(t); t != "" {
							tags = append(tags, t)
						}
					}
					snippet["tags"] = tags
				case "privacyStatus", "license", "publishAt":
					status[k] = youTubeTime(k, toString(v))
				case "embeddable", "publicStatsViewable", "selfDeclaredMadeForKids":
					status[k] = toBool(v)
				case "recordingDate":
					recording[k] = youTubeTime(k, toString(v))
				case "notifySubscribers", "onBehalfOfContentOwner", "onBehalfOfContentOwnerChannel":
					q.Set(k, toString(v))
				}
			}
			body := map[string]interface{}{"snippet": snippet, "status": status, "recordingDetails": recording}
			if o == "update" {
				id, _ := ec.str(i, "videoId", "")
				body["id"] = id
				res, err := call("PUT", "/videos", q, body)
				return one(res), err
			}
			prop, _ := ec.str(i, "binaryProperty", "data")
			data, mt, _, err := binaryOf(it, prop)
			if err != nil {
				return nil, err
			}
			var buf bytes.Buffer
			w := multipart.NewWriter(&buf)
			meta, _ := w.CreatePart(map[string][]string{"Content-Type": {"application/json; charset=UTF-8"}})
			mb, _ := json.Marshal(body)
			_, _ = meta.Write(mb)
			media, _ := w.CreatePart(map[string][]string{"Content-Type": {mt}})
			_, _ = media.Write(data)
			_ = w.Close()
			q.Set("uploadType", "multipart")
			res, _, err := ec.apiCall("POST", root+"/upload/youtube/v3/videos?"+q.Encode(), h, buf.Bytes(), "multipart/related; boundary="+w.Boundary())
			return one(res), err
		}
		return nil, nil
	})
}

// youTubeTime normalizes date filters/fields to RFC 3339, as the API needs.
func youTubeTime(key, v string) string {
	switch key {
	case "publishedAfter", "publishedBefore", "publishAt", "recordingDate":
		if t, ok := timeOf(v); ok {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return v
}

// youTubeOther covers the channel, playlist, playlist item and video
// category resources.
var youTubeOther = &restService{
	pager: cursorPager("nextPageToken", "pageToken"),
	ops: map[string]*restOp{
		"channel/get": {path: "/channels", query: "channelId:id", out: "items", build: youTubeParts(youTubeChannelParts)},
		"channel/getAll": {path: "/channels", list: true, listKey: "items", sizeParam: "maxResults", maxSize: 50, query: "@options * @filters *",
			build: youTubeMine(youTubeChannelParts, "categoryId", "forUsername", "id", "managedByMe")},
		"playlist/get": {path: "/playlists", query: "playlistId:id @options *", out: "items", build: youTubeParts(youTubePlaylistParts)},
		"playlist/getAll": {path: "/playlists", list: true, listKey: "items", sizeParam: "maxResults", maxSize: 50, query: "@options * @filters *",
			build: youTubeMine(youTubePlaylistParts, "channelId", "id")},
		"playlist/create": {method: "POST", path: "/playlists", query: "=part:snippet @options onBehalfOfContentOwner onBehalfOfContentOwnerChannel",
			body: "title:snippet.title @options description:snippet.description tags:snippet.tags:csv privacyStatus:snippet.privacyStatus defaultLanguage:snippet.defaultLanguage"},
		"playlist/update": {method: "PUT", path: "/playlists", query: "=part:snippet,status @updateFields onBehalfOfContentOwner",
			body: "playlistId:id title:snippet.title @updateFields tags:snippet.tags:csv privacyStatus:status.privacyStatus description:snippet.description defaultLanguage:snippet.defaultLanguage"},
		"playlist/delete":  {method: "DELETE", path: "/playlists", query: "playlistId:id @options onBehalfOfContentOwner", success: true},
		"playlistItem/get": {path: "/playlistItems", query: "playlistItemId:id @options *", out: "items", build: youTubeParts(youTubePlaylistItemParts)},
		"playlistItem/getAll": {path: "/playlistItems", list: true, listKey: "items", sizeParam: "maxResults", maxSize: 50, query: "playlistId @options *",
			build: youTubeParts(youTubePlaylistItemParts)},
		"playlistItem/add": {method: "POST", path: "/playlistItems", query: "=part:snippet,contentDetails @options onBehalfOfContentOwner",
			body: "playlistId:snippet.playlistId videoId:snippet.resourceId.videoId =snippet.resourceId.kind:youtube#video @options position:snippet.position:int note:contentDetails.note startAt:contentDetails.startAt:iso endAt:contentDetails.endAt:iso"},
		"playlistItem/delete":  {method: "DELETE", path: "/playlistItems", query: "playlistItemId:id @options onBehalfOfContentOwner", success: true},
		"videoCategory/getAll": {path: "/videoCategories", list: true, listKey: "items", query: "regionCode =part:snippet"},
	},
}

const (
	youTubeChannelParts      = "brandingSettings,contentDetails,contentOwnerDetails,id,localizations,snippet,statistics,status,topicDetails"
	youTubePlaylistParts     = "contentDetails,id,localizations,player,snippet,status"
	youTubePlaylistItemParts = "contentDetails,id,snippet,status"
)

// youTubeParts sets "part" from the node's part list ("*" = all).
func youTubeParts(all string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		v, _ := ec.param(i, "part", nil)
		var ps []string
		for _, p := range asListOrOne(v) {
			if s := toString(p); s != "" {
				ps = append(ps, s)
			}
		}
		part := strings.Join(ps, ",")
		if part == "" || strings.Contains(part, "*") {
			part = all
		}
		r.query.Set("part", part)
		return nil
	}
}

// youTubeMine lists the user's own records unless a filter selects others.
func youTubeMine(all string, filters ...string) func(ec *execCtx, i int, r *restRequest) error {
	parts := youTubeParts(all)
	return func(ec *execCtx, i int, r *restRequest) error {
		if err := parts(ec, i, r); err != nil {
			return err
		}
		for _, f := range filters {
			if r.query.Get(f) != "" {
				return nil
			}
		}
		r.query.Set("mine", "true")
		return nil
	}
}
