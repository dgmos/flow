package n8n

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dgmos/flow/internal/engine"
)

// Upload-Post (n8n-nodes-upload-post): publish photos, videos, text and
// documents to many social networks in one multipart call. Supported: the
// uploads resource, users (list/create/delete/JWT) and upload monitoring
// (status, job status, history). Platform options of the main networks are
// mapped; options of the newer niche networks (dev.to, Hashnode, Whop, …)
// beyond their titles are not.

func init() {
	ops := map[string]*restOp{
		"uploads/uploadPhotos":   {run: upUpload("uploadPhotos", "/upload_photos")},
		"uploads/uploadVideo":    {run: upUpload("uploadVideo", "/upload")},
		"uploads/uploadText":     {run: upUpload("uploadText", "/upload_text")},
		"uploads/uploadDocument": {run: upUpload("uploadDocument", "/upload_document")},
		"users/listUsers":        {path: "/uploadposts/users"},
		"users/createUser":       {method: "POST", path: "/uploadposts/users", body: "newUser:username"},
		"users/deleteUser":       {method: "DELETE", path: "/uploadposts/users", body: "deleteUserId:username"},
		"users/generateJwt": {method: "POST", path: "/uploadposts/users/generate-jwt", body: "redirectUrl:redirect_url logoImage:logo_image redirectButtonText:redirect_button_text " +
			"jwtPlatforms:platforms connectTitle:connect_title connectDescription:connect_description language", build: func(ec *execCtx, i int, r *restRequest) error {
			r.body["username"] = upUser(ec, i)
			r.body["show_calendar"], _ = ec.boolean(i, "showCalendar", true)
			r.body["readonly_calendar"], _ = ec.boolean(i, "readonlyCalendar", false)
			return nil
		}},
		"users/validateJwt": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
			jwt, _ := ec.str(i, "jwtToken", "")
			nc := &restClient{base: c.base, headers: map[string]string{"Authorization": "Bearer " + jwt}}
			v, err := nc.do(ec, &restRequest{method: "GET", path: "/uploadposts/users/validate-jwt"})
			return restItems(v), err
		}},
		"monitoring/getStatus":    {path: "/uploadposts/status", query: "requestId:request_id"},
		"monitoring/getJobStatus": {path: "/uploadposts/status", query: "jobId:job_id"},
		"monitoring/getHistory": {path: "/uploadposts/history", build: func(ec *execCtx, i int, r *restRequest) error {
			p, _ := ec.num(i, "historyPage", 1)
			if p < 1 {
				p = 1
			}
			l, _ := ec.num(i, "historyLimit", 20)
			best := 10
			for _, n := range []int{10, 20, 50, 100} {
				if abs(float64(n)-l) < abs(float64(best)-l) {
					best = n
				}
			}
			r.query.Set("page", fmt.Sprint(int(p)))
			r.query.Set("limit", fmt.Sprint(best))
			return nil
		}},
	}
	s := &restService{
		defResource: "uploads",
		defOps:      map[string]string{"uploads": "uploadPhotos", "users": "listUsers", "monitoring": "getStatus"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("uploadPostApi")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("uploadPost", "https://api.upload-post.com/api"),
				headers: map[string]string{"Authorization": "Apikey " + credString(c, "apiKey"), "X-Upload-Post-Source": "n8n"}}, nil
		},
		ops: ops,
	}
	registerREST("n8n-nodes-upload-post.uploadPost", s)
	registerREST("n8n-nodes-preview-upload-post.uploadPost", s)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// upPollInterval scales the status polling interval (seconds) in tests.
var upPollInterval = time.Second

var upPlatformSupport = map[string][]string{
	"uploadPhotos":   {"bluesky", "discord", "facebook", "instagram", "linkedin", "pinterest", "telegram", "threads", "tiktok", "x", "reddit", "google_business", "mastodon", "lemmy", "wordpress"},
	"uploadVideo":    {"bluesky", "discord", "facebook", "instagram", "linkedin", "pinterest", "reddit", "telegram", "threads", "tiktok", "x", "youtube", "google_business", "mastodon", "wordpress"},
	"uploadText":     {"bluesky", "discord", "facebook", "linkedin", "reddit", "telegram", "threads", "x", "google_business", "slack", "mastodon", "nostr", "lemmy", "devto", "hashnode", "wordpress", "whop", "listmonk"},
	"uploadDocument": {"linkedin", "telegram"},
}

// upOverride maps a per-platform parameter to a form field, optionally
// limited to some operations.
type upOverride struct {
	platform, param, field string
	ops                    []string
}

var upTitles = []upOverride{
	{"bluesky", "blueskyTitle", "bluesky_title", nil}, {"instagram", "instagramTitle", "instagram_title", nil}, {"facebook", "facebookTitle", "facebook_title", nil},
	{"tiktok", "tiktokTitle", "tiktok_title", nil}, {"linkedin", "linkedinTitle", "linkedin_title", nil}, {"x", "xTitle", "x_title", nil},
	{"youtube", "youtubeTitle", "youtube_title", []string{"uploadVideo"}}, {"pinterest", "pinterestTitle", "pinterest_title", nil},
	{"threads", "threadsTitle", "threads_title", nil}, {"slack", "slackTitle", "slack_title", nil}, {"mastodon", "mastodonTitle", "mastodon_title", nil},
	{"nostr", "nostrTitle", "nostr_title", nil}, {"lemmy", "lemmyTitle", "lemmy_title", nil}, {"devto", "devtoTitle", "devto_title", nil},
	{"hashnode", "hashnodeTitle", "hashnode_title", nil}, {"wordpress", "wordpressTitle", "wordpress_title", nil}, {"whop", "whopTitle", "whop_title", nil},
	{"listmonk", "listmonkTitle", "listmonk_title", nil},
}

var upDescriptions = []upOverride{
	{"linkedin", "linkedinDescription", "linkedin_description", []string{"uploadPhotos", "uploadVideo"}},
	{"youtube", "youtubeDescription", "youtube_description", []string{"uploadVideo"}},
	{"facebook", "facebookDescription", "facebook_description", []string{"uploadPhotos", "uploadVideo"}},
	{"tiktok", "tiktokDescription", "tiktok_description", []string{"uploadPhotos"}},
	{"pinterest", "pinterestDescription", "pinterest_description", []string{"uploadPhotos", "uploadVideo"}},
}

var upFirstComments = []upOverride{
	{"instagram", "instagramFirstComment", "instagram_first_comment", nil}, {"facebook", "facebookFirstComment", "facebook_first_comment", nil},
	{"x", "xFirstComment", "x_first_comment", nil}, {"threads", "threadsFirstComment", "threads_first_comment", nil},
	{"youtube", "youtubeFirstComment", "youtube_first_comment", []string{"uploadVideo"}}, {"reddit", "redditFirstComment", "reddit_first_comment", nil},
	{"bluesky", "blueskyFirstComment", "bluesky_first_comment", nil}, {"linkedin", "linkedinFirstComment", "linkedin_first_comment", nil},
	{"tiktok", "tiktokFirstComment", "tiktok_first_comment", []string{"uploadPhotos", "uploadVideo"}},
}

// upExtras are simple optional fields per platform (set when filled;
// booleans are sent as "true").
var upExtras = map[string][][2]string{
	"bluesky": {{"blueskyAltText", "bluesky_alt_text"}, {"blueskyLangs", "bluesky_langs"}, {"blueskyLabels", "bluesky_labels"}, {"blueskyThreadgate", "bluesky_threadgate"},
		{"blueskyQuoteUri", "bluesky_quote_uri"}, {"blueskyGallery", "bluesky_gallery"}},
	"discord": {{"discordThreadId", "discord_thread_id"}, {"discordThreadName", "discord_thread_name"}, {"discordUsername", "discord_username"},
		{"discordAvatarUrl", "discord_avatar_url"}, {"discordEmbeds", "discord_embeds"}, {"discordAltText", "discord_alt_text"}, {"discordTts", "discord_tts"}},
	"telegram": {{"telegramParseMode", "telegram_parse_mode"}, {"telegramMessageThreadId", "telegram_message_thread_id"}, {"telegramDisableNotification", "telegram_disable_notification"},
		{"telegramProtectContent", "telegram_protect_content"}, {"telegramHasSpoiler", "telegram_has_spoiler"}, {"telegramAsDocument", "telegram_as_document"}},
	"mastodon": {{"mastodonVisibility", "mastodon_visibility"}, {"mastodonSensitive", "mastodon_sensitive"}, {"mastodonSpoilerText", "mastodon_spoiler_text"},
		{"mastodonLanguage", "mastodon_language"}, {"mastodonAltText", "mastodon_alt_text"}, {"mastodonPollOptions", "mastodon_poll_options"},
		{"mastodonPollExpiresIn", "mastodon_poll_expires_in"}, {"mastodonPollMultiple", "mastodon_poll_multiple"}},
	"wordpress": {{"wordpressStatus", "wordpress_status"}, {"wordpressDate", "wordpress_date"}, {"wordpressCategories", "wordpress_categories"}, {"wordpressTags", "wordpress_tags"},
		{"wordpressExcerpt", "wordpress_excerpt"}, {"wordpressSlug", "wordpress_slug"}, {"wordpressAltText", "wordpress_alt_text"}},
	"lemmy": {{"lemmyCommunity", "lemmy_community"}, {"lemmyUrl", "lemmy_url"}, {"lemmyNsfw", "lemmy_nsfw"}, {"lemmyAltText", "lemmy_alt_text"}},
	"slack": {{"slackMarkdown", "slack_markdown"}, {"slackBlocks", "slack_blocks"}, {"slackMrkdwn", "slack_mrkdwn"}},
	"nostr": {{"nostrKind", "nostr_kind"}, {"nostrLongForm", "nostr_long_form"}},
	"threads": {{"threadsLongTextAsPost", "threads_long_text_as_post"}, {"threadsTopicTag", "threads_topic_tag"}, {"threadsReplyControl", "threads_reply_control"},
		{"threadsAltText", "threads_alt_text"}, {"threadsReplyToId", "threads_reply_to_id"}, {"threadsQuotePostId", "threads_quote_post_id"},
		{"threadsLinkAttachment", "threads_link_attachment"}, {"threadsPollOptions", "threads_poll_options"}},
	"google_business": {{"gbpLocationId", "gbp_location_id"}, {"gbpTopicType", "gbp_topic_type"}, {"gbpMediaUrl", "gbp_media_url"}, {"gbpMediaFormat", "gbp_media_format"},
		{"gbpCtaType", "gbp_cta_type"}, {"gbpCtaUrl", "gbp_cta_url"}, {"gbpLanguageCode", "gbp_language_code"}},
}

// upForm is the form being built: string values, string lists ("x[]")
// and binary files.
type upForm struct {
	ec    *execCtx
	i     int
	it    Item
	op    string
	plats map[string]bool
	vals  map[string][]string
	files map[string][]upFile
}

type upFile struct {
	name, mt string
	data     []byte
}

func (f *upForm) str(name, def string) string {
	v, _ := f.ec.param(f.i, name, def)
	if v == nil {
		return def
	}
	return toString(rlValue(v))
}

func (f *upForm) boolean(name string, def bool) bool {
	v, _ := f.ec.boolean(f.i, name, def)
	return v
}

func (f *upForm) set(field, v string) {
	if v != "" {
		f.vals[field] = []string{v}
	}
}

// filled mirrors setIfFilled: skip empty and false, booleans become "true".
func (f *upForm) filled(field string, v interface{}) {
	switch t := v.(type) {
	case nil:
	case bool:
		if t {
			f.vals[field] = []string{"true"}
		}
	case string:
		f.set(field, t)
	default:
		if s := toString(t); s != "" && s != "0" {
			f.vals[field] = []string{s}
		}
	}
}

func (f *upForm) has(p string) bool { return f.plats[p] }

// media adds a URL as a value or a binary property as a file.
func (f *upForm) media(field, ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	low := strings.ToLower(ref)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		f.vals[field] = append(f.vals[field], ref)
		return nil
	}
	data, mt, name, err := binaryOf(f.it, ref)
	if err != nil {
		return permanentError{fmt.Sprintf("%s: binary data '%s' was not found in item %d", f.ec.node.Name, ref, f.i)}
	}
	f.files[field] = append(f.files[field], upFile{name, mt, data})
	return nil
}

func (f *upForm) overrides(list []upOverride) {
	for _, o := range list {
		if !f.has(o.platform) || o.ops != nil && !containsStr(o.ops, f.op) {
			continue
		}
		f.set(o.field, f.str(o.param, ""))
	}
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func upUser(ec *execCtx, i int) string {
	v, _ := ec.param(i, "user", "")
	u := rlValue(v)
	if u == "__manual_user__" {
		m, _ := ec.str(i, "userManual", "")
		return m
	}
	return u
}

func upCSV(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func upUpload(op, path string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		f := &upForm{ec: ec, i: i, it: it, op: op, plats: map[string]bool{}, vals: map[string][]string{}, files: map[string][]upFile{}}
		if err := f.build(); err != nil {
			return nil, err
		}
		body := newCNForm()
		keys := make([]string, 0, len(f.vals)+len(f.files))
		for k := range f.vals {
			keys = append(keys, k)
		}
		for k := range f.files {
			if f.vals[k] == nil {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		h := sha256.New()
		for _, k := range keys {
			for _, v := range f.vals[k] {
				body.field(k, v)
				fmt.Fprintf(h, "%s=%s\n", k, v)
			}
			for _, fl := range f.files[k] {
				body.file(k, fl.name, fl.mt, fl.data)
				fmt.Fprintf(h, "%s=file:%x\n", k, sha256.Sum256(fl.data))
			}
		}
		req := body.request("POST", path)
		idem := f.str("idempotencyKey", "")
		if strings.TrimSpace(idem) == "" {
			idem = fmt.Sprintf("n8n:%s:%s:0:%d:%s", engine.ExecutionIDFromContext(ec.ctx), ec.node.ID, i, hex.EncodeToString(h.Sum(nil))[:16])
		}
		req.headers["Idempotency-Key"] = strings.TrimSpace(idem)
		v, err := c.do(ec, req)
		if err != nil {
			return nil, err
		}
		res := asMap(v)
		if f.boolean("waitForCompletion", false) && res["pending_approval"] != true {
			if id := toString(res["request_id"]); id != "" && res["request_id"] != nil {
				every, _ := ec.num(i, "pollInterval", 10)
				limit, _ := ec.num(i, "pollTimeout", 600)
				if v, err = upPoll(ec, c, id, every, limit); err != nil {
					return nil, err
				}
			}
		}
		return restItems(v), nil
	}
}

var upTerminal = map[string]bool{"completed": true, "failed": true, "error": true, "retryable": true, "scheduled": true, "not_found": true,
	"pending_approval": true, "draft": true, "rejected": true, "withdrawn": true, "expired": true}

// upPoll checks the upload status until it reaches a terminal state.
func upPoll(ec *execCtx, c *restClient, id string, every, limit float64) (interface{}, error) {
	if every < 1 {
		every = 1
	}
	if limit < 5 {
		limit = 5
	}
	deadline := time.Now().Add(time.Duration(limit * float64(upPollInterval)))
	var last interface{} = map[string]interface{}{"success": false, "message": "Polling timed out", "request_id": id}
	for time.Now().Before(deadline) {
		select {
		case <-ec.ctx.Done():
			return nil, ec.ctx.Err()
		case <-time.After(time.Duration(every * float64(upPollInterval))):
		}
		res, err := cnRaw(ec, c, &restRequest{method: "GET", path: "/uploadposts/status", query: url.Values{"request_id": {id}}})
		if err != nil && res.status != 404 && res.status != 429 {
			return nil, err
		}
		if res.status == 429 {
			continue
		}
		last = cnDecode(res)
		st := strings.ToLower(toString(asMap(last)["status"]))
		if res.status == 404 && asMap(last)["status"] == nil {
			st = "not_found"
		}
		if st != "not_found" && upTerminal[st] {
			break
		}
	}
	return last, nil
}

var (
	upDigits = regexp.MustCompile(`(\d+)$`)
	upTZ     = regexp.MustCompile(`[zZ]$|[+-]\d{2}:?\d{2}$`)
)

func (f *upForm) build() error {
	ec, i, op := f.ec, f.i, f.op
	f.vals["user"] = []string{upUser(ec, i)}
	f.set("title", f.str("title", ""))
	f.set("first_comment", f.str("firstComment", ""))
	if m := f.str("firstCommentMedia", ""); m != "" {
		f.vals["first_comment_media[]"] = upCSV(m)
	}
	f.set("reply_to_id", f.str("replyToId", ""))
	f.set("alt_text", f.str("altText", ""))
	if d := f.str("scheduledDate", ""); d != "" {
		if !upTZ.MatchString(d) {
			d += "Z"
		}
		f.set("scheduled_date", d)
	}
	f.set("timezone", f.str("timezone", ""))
	if f.boolean("addToQueue", false) {
		f.set("add_to_queue", "true")
		if n, _ := ec.num(i, "maxPostsPerSlot", 0); n > 0 {
			f.set("max_posts_per_slot", fmt.Sprint(int(n)))
		}
	}
	f.set("async_upload", fmt.Sprint(f.boolean("uploadAsync", true)))
	pv, _ := ec.param(i, "platform", nil)
	manual := false
	var plats []string
	for _, p := range asListOrOne(pv) {
		ps := toString(p)
		if ps == "__manual_platform__" {
			manual = true
		}
		if containsStr(upPlatformSupport[op], ps) {
			plats = append(plats, ps)
			f.plats[ps] = true
		}
	}
	f.vals["platform[]"] = plats
	f.overrides(upTitles)
	if d := f.str("description", ""); d != "" && (op == "uploadPhotos" || op == "uploadVideo") {
		for _, p := range []string{"linkedin", "facebook", "youtube", "pinterest", "tiktok"} {
			if f.has(p) {
				f.set("description", d)
				break
			}
		}
	}
	f.overrides(upDescriptions)
	f.overrides(upFirstComments)

	switch op {
	case "uploadPhotos":
		pv, _ := ec.param(i, "photos", "")
		var refs []string
		if l, ok := pv.([]interface{}); ok {
			for _, x := range l {
				if s := strings.TrimSpace(toString(x)); s != "" {
					refs = append(refs, s)
				}
			}
		} else {
			refs = upCSV(toString(pv))
		}
		for _, r := range refs {
			if err := f.media("photos[]", r); err != nil {
				return err
			}
		}
	case "uploadVideo":
		if err := f.media("video", f.str("video", "")); err != nil {
			return err
		}
	case "uploadDocument":
		if err := f.media("document", f.str("document", "")); err != nil {
			return err
		}
		if f.has("linkedin") || manual {
			f.linkedin(manual)
		}
		return nil
	}

	if f.has("pinterest") || manual {
		f.pinterest(manual)
	}
	if f.has("linkedin") || manual {
		f.linkedin(manual)
	}
	if f.has("facebook") || manual {
		f.facebook(manual)
	}
	if f.has("tiktok") {
		f.tiktok()
	}
	if f.has("instagram") {
		if err := f.instagram(); err != nil {
			return err
		}
	}
	if f.has("youtube") && op == "uploadVideo" {
		if err := f.youtube(); err != nil {
			return err
		}
	}
	if f.has("x") {
		f.x()
	}
	if f.has("threads") && op == "uploadPhotos" {
		f.set("threads_thread_media_layout", f.str("threadsThreadMediaLayout", ""))
	}
	if f.has("reddit") {
		f.vals["subreddit"] = []string{f.str("redditSubreddit", "")}
		f.set("flair_id", f.str("redditFlairId", ""))
		if op == "uploadText" {
			f.set("reddit_link_url", f.str("redditLinkUrl", ""))
		}
		f.filled("reddit_nsfw", f.boolean("redditNsfw", false))
		f.filled("reddit_spoiler", f.boolean("redditSpoiler", false))
		f.set("reddit_flair_text", f.str("redditFlairText", ""))
	}
	if f.has("bluesky") && op == "uploadText" {
		f.set("bluesky_link_url", f.str("blueskyLink", ""))
	}
	for p, fields := range upExtras {
		if !f.has(p) && (!manual || p == "threads" || p == "google_business") {
			continue
		}
		for _, pf := range fields {
			v, _ := ec.param(i, pf[0], nil)
			f.filled(pf[1], v)
		}
	}
	return nil
}

func (f *upForm) pinterest(manual bool) {
	board := f.str("pinterestBoardIdManualEntry", "")
	if !manual {
		if board = f.str("pinterestBoardId", ""); board == "__manual_pinterest__" {
			board = f.str("pinterestBoardIdManual", "")
		}
	}
	f.set("pinterest_board_id", board)
	f.set("pinterest_alt_text", f.str("pinterestAltText", ""))
	f.set("pinterest_link", f.str("pinterestLink", ""))
	f.set("pinterest_board_section_id", f.str("pinterestBoardSectionId", ""))
	if f.op == "uploadVideo" {
		if u := f.str("pinterestCoverImageUrl", ""); u != "" {
			f.set("pinterest_cover_image_url", u)
		} else if ct, d := f.str("pinterestCoverImageContentType", ""), f.str("pinterestCoverImageData", ""); ct != "" && d != "" {
			f.set("pinterest_cover_image_content_type", ct)
			f.set("pinterest_cover_image_data", d)
		} else {
			f.vals["pinterest_cover_image_key_frame_time"] = []string{f.str("pinterestCoverImageKeyFrameTime", "0")}
		}
	}
}

func (f *upForm) linkedin(manual bool) {
	page := f.str("targetLinkedinPageIdManualEntry", "")
	if !manual {
		if page = f.str("targetLinkedinPageId", ""); page == "__manual_linkedin__" {
			page = f.str("targetLinkedinPageIdManual", "")
		}
	}
	if page != "" && page != "me" {
		if m := upDigits.FindString(page); m != "" {
			page = m
		}
		f.set("target_linkedin_page_id", page)
	}
	vis := f.str("linkedinVisibility", "PUBLIC")
	switch f.op {
	case "uploadPhotos":
		if vis == "PUBLIC" {
			f.set("visibility", "PUBLIC")
		}
	case "uploadVideo", "uploadDocument":
		f.set("visibility", vis)
	case "uploadText":
		f.set("linkedin_link_url", f.str("linkedinLink", ""))
	}
	f.set("linkedin_alt_text", f.str("linkedinAltText", ""))
	f.filled("linkedin_disable_reshare", f.boolean("linkedinDisableReshare", false))
	f.set("linkedin_link_title", f.str("linkedinLinkTitle", ""))
	f.set("linkedin_link_description", f.str("linkedinLinkDescription", ""))
	if f.op == "uploadDocument" {
		f.set("description", f.str("documentDescription", ""))
	}
}

func (f *upForm) facebook(manual bool) {
	page := f.str("facebookPageIdManualEntry", "")
	if !manual {
		if page = f.str("facebookPageId", ""); page == "__manual_facebook__" {
			page = f.str("facebookPageIdManual", "")
		}
	}
	f.set("facebook_page_id", page)
	switch f.op {
	case "uploadVideo":
		f.set("video_state", f.str("facebookVideoState", ""))
		mt := f.str("facebookMediaType", "")
		f.set("facebook_media_type", mt)
		if mt == "VIDEO" {
			f.set("thumbnail_url", f.str("facebookThumbnailUrl", ""))
		}
		f.filled("facebook_is_ai_generated", f.boolean("facebookIsAiGenerated", false))
	case "uploadPhotos":
		if mt := f.str("facebookMediaTypePhoto", "POSTS"); mt != "POSTS" {
			f.set("facebook_media_type", mt)
		}
		f.set("facebook_alt_text", f.str("facebookAltText", ""))
	case "uploadText":
		f.set("facebook_link_url", f.str("facebookLink", ""))
		f.set("facebook_call_to_action", f.str("facebookCallToAction", ""))
	}
	f.set("facebook_place_id", f.str("facebookPlaceId", ""))
}

func (f *upForm) tiktok() {
	b := func(k string) string { return fmt.Sprint(f.boolean(k, false)) }
	shared := func() {
		f.set("tiktok_music_id", f.str("tiktokMusicId", ""))
		if id, name := f.str("tiktokLocationId", ""), f.str("tiktokLocationName", ""); id != "" && name != "" {
			f.set("tiktok_location_id", id)
			f.set("tiktok_location_name", name)
		}
		f.filled("tiktok_is_ai_generated", f.boolean("tiktokIsAiGenerated", false))
	}
	switch f.op {
	case "uploadPhotos":
		f.set("auto_add_music", b("tiktokAutoAddMusic"))
		f.set("disable_comment", b("tiktokDisableComment"))
		f.set("brand_content_toggle", b("brand_content_toggle"))
		f.set("brand_organic_toggle", b("brand_organic_toggle"))
		f.set("photo_cover_index", f.str("tiktokPhotoCoverIndex", "0"))
		if d := f.str("tiktokPhotoDescription", ""); d != "" && f.vals["description"] == nil {
			f.set("description", d)
		}
		shared()
	case "uploadVideo":
		f.set("privacy_level", f.str("tiktokPrivacyLevel", ""))
		f.set("disable_duet", b("tiktokDisableDuet"))
		f.set("disable_comment", b("tiktokDisableComment"))
		f.set("disable_stitch", b("tiktokDisableStitch"))
		f.set("cover_timestamp", f.str("tiktokCoverTimestamp", "1000"))
		f.set("brand_content_toggle", b("brand_content_toggle"))
		f.set("brand_organic_toggle", b("brand_organic_toggle"))
		f.set("is_aigc", b("tiktokIsAigc"))
		f.set("post_mode", f.str("tiktokPostMode", ""))
		shared()
		if f.vals["tiktok_music_id"] != nil {
			f.set("tiktok_music_volume", f.str("tiktokMusicVolume", "50"))
			f.set("tiktok_original_sound_volume", f.str("tiktokOriginalSoundVolume", "50"))
			for k, fld := range map[string]string{"tiktokMusicStart": "tiktok_music_start", "tiktokMusicEnd": "tiktok_music_end"} {
				if n, _ := f.ec.num(f.i, k, 0); n > 0 {
					f.set(fld, toString(n))
				}
			}
		}
		f.set("tiktok_cover_image_url", f.str("tiktokCoverImageUrl", ""))
		f.filled("tiktok_upload_to_draft", f.boolean("tiktokUploadToDraft", false))
		f.filled("tiktok_is_ads_only", f.boolean("tiktokIsAdsOnly", false))
		f.set("tiktok_tto_invite_link", f.str("tiktokTtoInviteLink", ""))
	}
}

func (f *upForm) instagram() error {
	mt := f.str("instagramMediaType", "")
	switch f.op {
	case "uploadPhotos":
		if mt != "IMAGE" && mt != "STORIES" {
			mt = "IMAGE"
		}
	case "uploadVideo":
		if mt != "REELS" && mt != "STORIES" {
			mt = "REELS"
		}
	}
	f.set("media_type", mt)
	if f.op == "uploadVideo" || f.op == "uploadPhotos" {
		f.set("collaborators", f.str("instagramCollaborators", ""))
		f.set("user_tags", f.str("instagramUserTags", ""))
		f.set("location_id", f.str("instagramLocationId", ""))
	}
	if f.op == "uploadVideo" {
		f.set("share_to_feed", fmt.Sprint(f.boolean("instagramShareToFeed", true)))
		if m := f.str("instagramShareMode", "CUSTOM"); m != "CUSTOM" {
			f.set("share_mode", m)
		}
		if c := f.str("instagramCoverUrl", ""); c != "" {
			low := strings.ToLower(c)
			if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
				f.set("cover_url", c)
			} else if err := f.media("cover_image", c); err != nil {
				return err
			}
		}
		f.set("audio_name", f.str("instagramAudioName", ""))
		f.set("thumb_offset", f.str("instagramThumbOffset", ""))
	}
	if f.op == "uploadPhotos" {
		f.set("instagram_alt_text", f.str("instagramAltText", ""))
	}
	return nil
}

func (f *upForm) youtube() error {
	if t := f.str("youtubeTags", ""); t != "" {
		f.vals["tags[]"] = upCSV(t)
	}
	f.set("categoryId", f.str("youtubeCategoryId", ""))
	f.set("privacyStatus", f.str("youtubePrivacyStatus", ""))
	f.set("embeddable", fmt.Sprint(f.boolean("youtubeEmbeddable", true)))
	f.set("license", f.str("youtubeLicense", ""))
	f.set("publicStatsViewable", fmt.Sprint(f.boolean("youtubePublicStatsViewable", true)))
	if th := f.str("youtubeThumbnail", ""); th != "" {
		low := strings.ToLower(th)
		if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
			f.set("thumbnail_url", th)
		} else if err := f.media("thumbnail", th); err != nil {
			return err
		}
	}
	f.set("selfDeclaredMadeForKids", fmt.Sprint(f.boolean("youtubeSelfDeclaredMadeForKids", false)))
	f.set("containsSyntheticMedia", fmt.Sprint(f.boolean("youtubeContainsSyntheticMedia", false)))
	f.set("defaultLanguage", f.str("youtubeDefaultLanguage", ""))
	f.set("defaultAudioLanguage", f.str("youtubeDefaultAudioLanguage", ""))
	f.set("allowedCountries", f.str("youtubeAllowedCountries", ""))
	f.set("blockedCountries", f.str("youtubeBlockedCountries", ""))
	f.set("hasPaidProductPlacement", fmt.Sprint(f.boolean("youtubeHasPaidProductPlacement", false)))
	f.set("recordingDate", f.str("youtubeRecordingDate", ""))
	f.set("youtube_playlist_id", f.str("youtubePlaylistId", ""))
	langs, files := upCSV(f.str("youtubeSubtitleLanguages", "")), upCSV(f.str("youtubeSubtitleFiles", ""))
	names := strings.Split(f.str("youtubeSubtitleNames", ""), ",")
	for k := 0; k < len(langs) && k < len(files); k++ {
		f.set(fmt.Sprintf("youtube_subtitle_language_%d", k), langs[k])
		if k < len(names) {
			f.set(fmt.Sprintf("youtube_subtitle_name_%d", k), strings.TrimSpace(names[k]))
		}
		if err := f.media(fmt.Sprintf("youtube_subtitle_file_%d", k), files[k]); err != nil {
			return err
		}
	}
	f.set("youtube_notify_subscribers", fmt.Sprint(f.boolean("youtubeNotifySubscribers", true)))
	f.set("youtube_publish_at", f.str("youtubePublishAt", ""))
	return nil
}

func (f *upForm) x() {
	f.set("quote_tweet_id", f.str("xQuoteTweetId", ""))
	f.set("geo_place_id", f.str("xGeoPlaceId", ""))
	f.filled("for_super_followers_only", f.boolean("xForSuperFollowersOnly", false))
	f.set("community_id", f.str("xCommunityId", ""))
	f.filled("share_with_followers", f.boolean("xShareWithFollowers", false))
	f.set("direct_message_deep_link", f.str("xDirectMessageDeepLink", ""))
	f.set("card_uri", f.str("xCardUri", ""))
	if r := f.str("xReplySettings", "everyone"); r != "everyone" {
		f.set("reply_settings", r)
	}
	if f.op == "uploadText" {
		f.set("post_url", f.str("xPostUrlText", ""))
		f.filled("x_long_text_as_post", f.boolean("xLongTextAsPost", false))
		f.set("x_article_title", f.str("xArticleTitle", ""))
		f.set("x_article_body", f.str("xArticleBody", ""))
	} else {
		if t := f.str("xTaggedUserIds", ""); t != "" {
			f.vals["tagged_user_ids[]"] = upCSV(t)
		}
		f.set("nullcast", fmt.Sprint(f.boolean("xNullcastVideo", false)))
		f.filled("x_long_text_as_post", f.boolean("xLongTextAsPost", false))
		if f.op == "uploadPhotos" {
			f.set("x_thread_image_layout", f.str("xThreadImageLayout", ""))
		}
		if f.op == "uploadVideo" {
			f.set("place_id", f.str("xPlaceIdVideo", ""))
		}
		f.set("x_alt_text", f.str("xAltText", ""))
		f.set("x_subtitles_url", f.str("xSubtitlesUrl", ""))
	}
	f.filled("x_paid_partnership", f.boolean("xPaidPartnership", false))
}
