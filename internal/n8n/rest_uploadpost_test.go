package n8n

import (
	"mime"
	"mime/multipart"
	"strings"
	"testing"
	"time"
)

// uploadPostForm parses a recorded multipart body into values and file names.
func uploadPostForm(t *testing.T, r recorded) (map[string][]string, map[string]string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.ctype)
	if err != nil {
		t.Fatalf("content type %q: %v", r.ctype, err)
	}
	form, err := multipart.NewReader(strings.NewReader(r.body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for k, fh := range form.File {
		files[k] = fh[0].Filename
	}
	return form.Value, files
}

func TestUploadPostNode(t *testing.T) {
	old := upPollInterval
	upPollInterval = time.Millisecond
	t.Cleanup(func() { upPollInterval = old })
	polls := 0
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		switch r.path {
		case "/upload", "/upload_photos", "/upload_text":
			return 200, "application/json", map[string]interface{}{"success": true, "request_id": "req1"}
		case "/uploadposts/status":
			polls++
			if polls == 1 {
				return 200, "application/json", map[string]interface{}{"status": "in_progress"}
			}
			return 200, "application/json", map[string]interface{}{"status": "completed", "results": []interface{}{}}
		case "/uploadposts/users":
			return 200, "application/json", map[string]interface{}{"success": true, "profile": map[string]interface{}{"username": "acme"}}
		}
		return 404, "application/json", map[string]interface{}{"error": "nope"}
	})
	idem := headerSpy(f, "Idempotency-Key")
	creds := map[string]map[string]interface{}{"uploadPostApi": {"apiKey": "up-key"}}
	const typ = "n8n-nodes-upload-post.uploadPost"

	// Video from binary data to TikTok + YouTube, waiting for completion.
	out := communityRun(t, f, "uploadPost", typ, 1, map[string]interface{}{"operation": "uploadVideo", "user": "acme", "title": "Launch",
		"platform": []interface{}{"tiktok", "youtube", "snapchat"}, "video": "data", "waitForCompletion": true, "pollInterval": 1,
		"youtubeTitle": "YT title", "youtubeTags": "a, b", "youtubePrivacyStatus": "unlisted", "tiktokTitle": "TT"}, creds, "MP4DATA", "v.mp4")
	if out[0].JSON["status"] != "completed" || polls != 2 {
		t.Fatalf("video: %+v (polls %d)", out, polls)
	}
	r := f.find(t, "POST", "/upload")
	vals, files := uploadPostForm(t, r)
	if r.auth != "Apikey up-key" || files["video"] != "v.mp4" || strings.Join(vals["platform[]"], ",") != "tiktok,youtube" || vals["user"][0] != "acme" ||
		vals["youtube_title"][0] != "YT title" || strings.Join(vals["tags[]"], ",") != "a,b" || vals["privacyStatus"][0] != "unlisted" ||
		vals["tiktok_title"][0] != "TT" || vals["async_upload"][0] != "true" || vals["disable_duet"][0] != "false" || !strings.HasPrefix(idem("/upload"), "n8n:") {
		t.Fatalf("video form: %v %v", vals, files)
	}

	// Photos by URL (default operation) to Instagram and Facebook.
	communityRun(t, f, "uploadPost", typ, 1, map[string]interface{}{"user": "acme", "title": "Pics", "photos": "https://img/1.jpg, https://img/2.jpg",
		"platform": []interface{}{"instagram", "facebook"}, "facebookPageId": "123", "instagramTitle": "IG"}, creds, "", "")
	vals, _ = uploadPostForm(t, f.find(t, "POST", "/upload_photos"))
	if len(vals["photos[]"]) != 2 || vals["media_type"][0] != "IMAGE" || vals["facebook_page_id"][0] != "123" || vals["instagram_title"][0] != "IG" {
		t.Fatalf("photos form: %v", vals)
	}

	// Text to LinkedIn page and X.
	communityRun(t, f, "uploadPost", typ, 1, map[string]interface{}{"resource": "uploads", "operation": "uploadText", "user": "acme", "title": "Hello",
		"platform": []interface{}{"linkedin", "x"}, "targetLinkedinPageId": "urn:li:organization:777", "xReplySettings": "following", "uploadAsync": false}, creds, "", "")
	vals, _ = uploadPostForm(t, f.find(t, "POST", "/upload_text"))
	if vals["target_linkedin_page_id"][0] != "777" || vals["reply_settings"][0] != "following" || vals["async_upload"][0] != "false" {
		t.Fatalf("text form: %v", vals)
	}

	out = communityRun(t, f, "uploadPost", typ, 1, map[string]interface{}{"resource": "users", "operation": "createUser", "newUser": "acme"}, creds, "", "")
	if out[0].JSON["success"] != true || jsonBody(t, f.find(t, "POST", "/uploadposts/users"))["username"] != "acme" {
		t.Fatalf("create user: %+v", out)
	}
}
