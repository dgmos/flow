package n8n

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGoogleCloudStorageNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if r.auth != "Bearer gcs-tok" {
			t.Errorf("auth %q", r.auth)
		}
		switch {
		case r.method == "GET" && r.path == "/storage/v1/b/":
			if strings.Contains(r.query, "pageToken=t2") {
				return 200, "application/json", map[string]interface{}{"items": []interface{}{map[string]interface{}{"name": "c"}}}
			}
			return 200, "application/json", map[string]interface{}{"items": []interface{}{map[string]interface{}{"name": "a"}, map[string]interface{}{"name": "b"}}, "nextPageToken": "t2"}
		case r.method == "POST" && r.path == "/storage/v1/b/":
			return 200, "application/json", map[string]interface{}{"name": "newb"}
		case r.method == "POST" && r.path == "/upload/storage/v1/b/bk/o/":
			return 200, "application/json", map[string]interface{}{"name": "dir/a.txt", "size": "5"}
		case r.method == "GET" && r.path == "/storage/v1/b/bk/o/dir%2Fa.txt", r.method == "GET" && r.path == "/storage/v1/b/bk/o/dir/a.txt":
			return 200, "text/plain", "hello"
		case r.method == "GET" && r.path == "/storage/v1/b/bk/o/":
			return 200, "application/json", map[string]interface{}{"items": []interface{}{map[string]interface{}{"name": "x"}}, "nextPageToken": "more"}
		case r.method == "DELETE":
			return 204, "application/json", ""
		}
		return 404, "application/json", map[string]interface{}{"error": r.path}
	})
	cred := map[string]map[string]interface{}{"googleCloudStorageOAuth2Api": {"oauthTokenData": map[string]interface{}{"access_token": "gcs-tok"}}}

	// Default: bucket/getAll; returnAll follows nextPageToken.
	out := restRun(t, f, "googleCloudStorage", "googleCloudStorage", map[string]interface{}{"projectId": "proj", "returnAll": true}, cred)
	if len(out) != 3 || out[2]["name"] != "c" {
		t.Fatalf("bucket getAll: %v", out)
	}
	if q := f.find(t, "GET", "/storage/v1/b/").query; !strings.Contains(q, "project=proj") || !strings.Contains(q, "projection=noAcl") || strings.Contains(q, "maxResults") {
		t.Fatalf("bucket getAll query %q", q)
	}

	out = restRun(t, f, "googleCloudStorage", "googleCloudStorage", map[string]interface{}{"operation": "create", "projectId": "proj", "bucketName": "newb",
		"createAcl": map[string]interface{}{"predefinedAcl": "private"}, "createBody": map[string]interface{}{"labels": `{"env":"dev"}`, "storageClass": "STANDARD"}}, cred)
	r := f.find(t, "POST", "/storage/v1/b/")
	if out[0]["name"] != "newb" || mustJSON(jsonBody(t, r)) != `{"labels":{"env":"dev"},"name":"newb","storageClass":"STANDARD"}` || !strings.Contains(r.query, "predefinedAcl=private") {
		t.Fatalf("bucket create: %v %+v", out, r)
	}

	// object/create uploads the binary as multipart/related.
	items := restRunFileInput(t, f, "googleCloudStorage", "googleCloudStorage", map[string]interface{}{"resource": "object", "operation": "create", "bucketName": "bk",
		"objectName": "dir/a.txt", "createData": map[string]interface{}{"metadata": `{"k":"v"}`}}, cred, "hello", "a.txt", "text/plain")
	if items[0].JSON["name"] != "dir/a.txt" {
		t.Fatalf("object create: %v", items[0].JSON)
	}
	up := f.find(t, "POST", "/upload/storage/v1/b/bk/o/")
	if !strings.HasPrefix(up.ctype, "multipart/related; boundary=") || !strings.Contains(up.body, `{"k":"v"}`) || !strings.Contains(up.body, "hello") ||
		!strings.Contains(up.query, "uploadType=multipart") || !strings.Contains(up.query, "name=dir%2Fa.txt") || !strings.Contains(up.query, "projection=full") {
		t.Fatalf("upload %+v", up)
	}

	// object/get with alt=media downloads to binary.
	items = restRunItems(t, f, "googleCloudStorage", "googleCloudStorage", 1, map[string]interface{}{"resource": "object", "operation": "get", "bucketName": "bk",
		"objectName": "dir/a.txt", "alt": "media", "binaryPropertyName": "file"}, cred)
	raw, _ := base64.StdEncoding.DecodeString(toString(asMap(items[0].Binary["file"])["data"]))
	if !strings.Contains(string(raw), "hello") {
		t.Fatalf("object get: %v", items[0].Binary)
	}

	// object/getAll without returnAll: one page with maxResults.
	out = restRun(t, f, "googleCloudStorage", "googleCloudStorage", map[string]interface{}{"resource": "object", "bucketName": "bk", "maxResults": 5,
		"listFilters": map[string]interface{}{"prefix": "dir/"}}, cred)
	if len(out) != 1 {
		t.Fatalf("object getAll: %v", out)
	}
	q := ""
	for _, r := range f.reqs {
		if r.path == "/storage/v1/b/bk/o/" {
			q = r.query
		}
	}
	if !strings.Contains(q, "maxResults=5") || !strings.Contains(q, "prefix=dir%2F") {
		t.Fatalf("object getAll query %q", q)
	}

	out = restRun(t, f, "googleCloudStorage", "googleCloudStorage", map[string]interface{}{"resource": "object", "operation": "delete", "bucketName": "bk", "objectName": "x"}, cred)
	if out[0]["success"] != true {
		t.Fatalf("delete: %v", out)
	}
}
