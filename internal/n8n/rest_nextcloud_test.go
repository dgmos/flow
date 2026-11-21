package n8n

import (
	"encoding/base64"
	"strings"
	"testing"
)

const ncPropfind = `<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
 <d:response><d:href>/remote.php/webdav/Docs/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype><d:getetag>"e0"</d:getetag></d:prop></d:propstat></d:response>
 <d:response><d:href>/remote.php/webdav/Docs/a%20b.txt</d:href><d:propstat><d:prop><d:getlastmodified>Mon, 01 Jan 2024 00:00:00 GMT</d:getlastmodified><d:getcontentlength>4</d:getcontentlength><d:resourcetype/><d:getcontenttype>text/plain</d:getcontenttype><d:getetag>"e1"</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
 <d:response><d:href>/remote.php/webdav/Docs/Sub/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype><d:getetag>"e2"</d:getetag></d:prop></d:propstat></d:response>
</d:multistatus>`

func TestNextCloudNode(t *testing.T) {
	f := newCmtyFake(t, func(r cmtyReq) (int, string, interface{}) {
		switch {
		case r.method == "PROPFIND" && r.path == "/remote.php/webdav/Docs":
			return 207, "application/xml", ncPropfind
		case r.method == "GET" && r.path == "/remote.php/webdav/Docs/report.pdf":
			return 200, "application/pdf", "%PDF-1.4"
		case r.method == "PUT", r.method == "MOVE", r.method == "MKCOL":
			return 201, "text/plain", ""
		case r.method == "POST" && r.path == "/ocs/v2.php/apps/files_sharing/api/v1/shares":
			return 200, "text/xml", `<?xml version="1.0"?><ocs><meta><status>ok</status><statuscode>200</statuscode></meta><data><id>9</id><url>https://cloud/s/abc</url></data></ocs>`
		case r.method == "GET" && r.path == "/ocs/v1.php/cloud/users":
			return 200, "text/xml", `<?xml version="1.0"?><ocs><meta><status>ok</status></meta><data><users><element>alice</element><element>bob</element></users></data></ocs>`
		}
		return 404, "text/plain", r.path
	})
	bases := map[string]string{"nextCloud": "/remote.php/webdav"}
	creds := map[string]map[string]interface{}{"nextCloudApi": {"webDavUrl": "https://cloud.example.com/remote.php/webdav/", "user": "u", "password": "p"}}
	typ := "n8n-nodes-base.nextCloud"

	out := cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "folder", "operation": "list", "path": "/Docs"}, creds, nil)
	if len(out) != 2 || out[0].JSON["path"] != "Docs/a%20b.txt" || out[0].JSON["type"] != "file" || out[0].JSON["eTag"] != "e1" ||
		out[0].JSON["contentLength"] != "4" || out[0].JSON["contentType"] != "text/plain" || out[1].JSON["type"] != "folder" {
		t.Fatalf("list: %v", out)
	}
	if a := f.find(t, "PROPFIND", "/remote.php/webdav/Docs").header.Get("Authorization"); a != basicAuth("u", "p")["Authorization"] {
		t.Fatalf("auth %q", a)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "download", "path": "Docs/report.pdf", "binaryPropertyName": "file"}, creds, nil)
	bin := asMap(out[0].Binary["file"])
	if d, _ := base64.StdEncoding.DecodeString(toString(bin["data"])); string(d) != "%PDF-1.4" || bin["fileName"] != "report.pdf" || bin["mimeType"] != "application/pdf" {
		t.Fatalf("download: %v", out)
	}

	cmtyRun(t, f, bases, typ, map[string]interface{}{"path": "Docs/up.txt", "binaryDataUpload": true}, creds, &cmtyInput{name: "up.txt", mimeType: "text/plain", content: "hello"})
	if b := f.find(t, "PUT", "/remote.php/webdav/Docs/up.txt").body; b != "hello" {
		t.Fatalf("upload body %q", b)
	}
	cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "folder", "path": "Docs/New Folder"}, creds, nil)
	f.find(t, "MKCOL", "/remote.php/webdav/Docs/New Folder")

	cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "move", "path": "Docs/up.txt", "toPath": "Archive/up 1.txt"}, creds, nil)
	if d := f.find(t, "MOVE", "/remote.php/webdav/Docs/up.txt").header.Get("Destination"); !strings.HasSuffix(d, "/remote.php/webdav/Archive/up%201.txt") {
		t.Fatalf("destination %q", d)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"operation": "share", "path": "/Docs/a.txt", "shareType": 3, "options": map[string]interface{}{"permissions": 1}}, creds, nil)
	r := f.find(t, "POST", "/ocs/v2.php/apps/files_sharing/api/v1/shares")
	if out[0].JSON["url"] != "https://cloud/s/abc" || r.header.Get("OCS-APIRequest") != "true" || !strings.Contains(r.body, "shareType=3") ||
		!strings.Contains(r.body, "path=%2FDocs%2Fa.txt") || !strings.Contains(r.body, "permissions=1") {
		t.Fatalf("share: %v %s", out, r.body)
	}

	out = cmtyRun(t, f, bases, typ, map[string]interface{}{"resource": "user", "operation": "getAll", "limit": 10}, creds, nil)
	if len(out) != 2 || out[1].JSON["id"] != "bob" || f.find(t, "GET", "/ocs/v1.php/cloud/users").query.Get("limit") != "10" {
		t.Fatalf("users: %v", out)
	}
}
