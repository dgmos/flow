package n8n

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Upload to URL (community node n8n-nodes-uploadtourl.uploadToUrl). Older
// package versions had no resource and called the upload "uploadFile".

var uploadToURLMime = map[string]string{"jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp",
	"svg": "image/svg+xml", "pdf": "application/pdf", "txt": "text/plain", "csv": "text/csv", "json": "application/json",
	"xml": "application/xml", "zip": "application/zip", "mp4": "video/mp4", "mp3": "audio/mpeg"}

func init() {
	registerREST("n8n-nodes-uploadtourl.uploadToUrl", &restService{
		defResource: "fileActions",
		defOps:      map[string]string{"fileActions": "upload"},
		aliases:     map[string]string{"fileActions/uploadFile": "fileActions/upload"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "uploadToUrlApi", nil)
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("uploadToUrl", "https://uploadtourl.com/api"), headers: map[string]string{"x-api-key": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"fileActions/retrieve":          {path: "/file/{fileId}"},
			"fileActions/delete":            {method: "DELETE", path: "/file/{fileId}"},
			"fileActions/download":          {run: uploadToURLDownload},
			"fileActions/upload":            {run: uploadToURLUpload("upload")},
			"fileActions/uploadMultiple":    {run: uploadToURLUpload("uploadMultiple")},
			"fileActions/uploadFileFromUrl": {run: uploadToURLUpload("uploadFileFromUrl")},
		},
	})
}

// uploadToURLFetch downloads fileUrl without credentials.
func uploadToURLFetch(ec *execCtx, i int) ([]byte, string, string, error) {
	u, err := ec.str(i, "fileUrl", "")
	if err != nil {
		return nil, "", "", err
	}
	res, err := ec.doRequest("GET", u, nil, nil, "")
	if err != nil {
		return nil, "", "", err
	}
	ct := strings.TrimSpace(strings.Split(res.contentType, ";")[0])
	if ct == "" {
		ct = "application/octet-stream"
	}
	name := cmtyHeaderFileName(res.header)
	if name == "" {
		name = "file"
		if pu, err := url.Parse(u); err == nil {
			if b := path.Base(pu.Path); strings.Contains(b, ".") {
				name = b
			}
		}
	}
	return res.body, name, ct, nil
}

func uploadToURLDownload(ec *execCtx, i int, it Item, _ *restClient) ([]Item, error) {
	data, name, ct, err := uploadToURLFetch(ec, i)
	if err != nil {
		return nil, err
	}
	prop, err := ec.str(i, "dataPropertyName", "data")
	if err != nil {
		return nil, err
	}
	return []Item{{JSON: it.JSON, Binary: map[string]interface{}{prop: binaryData(data, ct, name)}}}, nil
}

// uploadToURLUpload runs the upload operations (o names which).
func uploadToURLUpload(o string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		return uploadToURLRun(ec, i, it, c, o)
	}
}

func uploadToURLRun(ec *execCtx, i int, it Item, c *restClient, o string) ([]Item, error) {
	a := &cmtyArgs{ec: ec, i: i}
	expiry := "never"
	switch t := a.str("expiryType", "7"); t {
	case "never":
	case "custom":
		expiry = toString(a.get("expiryDays", 30.0))
	default:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		expiry = strconv.Itoa(n)
	}
	upload := func(data []byte, name, ct string) (interface{}, error) {
		body, mct := cmtyMultipart([][2]string{{"expiry_days", expiry}, {"source", "n8n"}}, cmtyFile{"file", name, ct, data})
		res, err := cmtyRaw(ec, c, "POST", "/upload", nil, body, mct)
		if err != nil {
			return nil, err
		}
		return decodeJSONBody(res.body), nil
	}
	if o == "uploadFileFromUrl" {
		data, name, ct, err := uploadToURLFetch(ec, i)
		if err != nil {
			return nil, err
		}
		v, err := upload(data, name, ct)
		if err != nil {
			return nil, err
		}
		return []Item{{JSON: cmtyObject(v)}}, nil
	}
	if o == "upload" && a.str("inputType", "binary") == "base64" {
		name := a.str("fileName", "file.bin")
		ct := a.str("mimeType", "auto")
		switch ct {
		case "auto":
			ext := strings.ToLower(name[strings.LastIndex(name, ".")+1:])
			ct = firstNonEmpty(uploadToURLMime[ext], "application/octet-stream")
		case "custom":
			ct = a.str("customMimeType", "")
		}
		b64 := regexp.MustCompile(`^data:[^;]+;base64,`).ReplaceAllString(a.str("base64Data", ""), "")
		if a.err != nil {
			return nil, a.err
		}
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, permanentError{"Upload to URL: base64 data is invalid"}
		}
		v, err := upload(data, name, ct)
		if err != nil {
			return nil, err
		}
		return []Item{{JSON: cmtyObject(v)}}, nil
	}
	if len(it.Binary) == 0 {
		return nil, permanentError{"No binary data found in item"}
	}
	var names []string
	all := make([]string, 0, len(it.Binary))
	for k := range it.Binary {
		all = append(all, k)
	}
	sort.Strings(all)
	strict := false
	if o == "upload" {
		if a.bool("uploadAllBinary", true) {
			names = all
		} else {
			names, strict = []string{a.str("binaryPropertyName", "data")}, true
		}
	} else {
		switch a.str("selectionMode", "all") {
		case "all":
			names = all
		case "names":
			for _, x := range asList(asMap(a.get("binaryPropertyNames", nil))["propertyList"]) {
				names = append(names, toString(asMap(x)["name"]))
			}
		case "regex":
			re, err := regexp.Compile(a.str("propertyNameRegex", ""))
			if err != nil {
				return nil, permanentError{fmt.Sprintf("Upload to URL: invalid regex: %v", err)}
			}
			for _, k := range all {
				if re.MatchString(k) {
					names = append(names, k)
				}
			}
		}
	}
	if a.err != nil {
		return nil, a.err
	}
	if len(names) == 0 {
		msg := "No binary properties found"
		if o != "upload" {
			msg = "No binary properties matched the selection criteria"
		}
		return []Item{{JSON: map[string]interface{}{"message": msg}}}, nil
	}
	var results []interface{}
	for _, p := range names {
		if _, ok := it.Binary[p]; !ok {
			if strict {
				return nil, permanentError{fmt.Sprintf("Binary property %s not found", p)}
			}
			continue
		}
		data, ct, name, err := binaryOf(it, p)
		if err != nil {
			return nil, err
		}
		if toString(asMap(it.Binary[p])["fileName"]) == "" {
			name = "file"
		}
		v, err := upload(data, name, ct)
		if err != nil {
			return nil, err
		}
		results = append(results, map[string]interface{}{"property": p, "fileName": name, "data": v})
	}
	if o == "upload" && len(results) == 1 {
		return []Item{{JSON: cmtyObject(asMap(results[0])["data"])}}, nil
	}
	return []Item{{JSON: map[string]interface{}{"results": results}}}, nil
}
