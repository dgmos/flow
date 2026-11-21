package n8n

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// PDF.co (n8n-nodes-pdfco, type "PDFco Api" with a space): PDF conversion
// and editing. Calls run asynchronously (async: true) and are polled through
// /v1/job/check until done, unless a callback URL is given. Operations are
// named by their display labels; there is no resource parameter.

func init() {
	ops := map[string]*restOp{}
	for name, fn := range pdfcoActions {
		fn := fn
		ops["/"+name] = &restOp{run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
			v, err := fn(ec, i, it, c)
			if err != nil {
				return nil, err
			}
			return restItems(v), nil
		}}
	}
	registerREST("n8n-nodes-pdfco.PDFco Api", &restService{
		defOps: map[string]string{"": "AI Invoice Parser"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			auth, _ := ec.str(0, "authentication", "apiKey")
			if auth == "oAuth2" {
				c, err := ec.credential("pdfcoOAuth2Api")
				if err != nil {
					return nil, err
				}
				base := ec.apiBase("pdfco", strings.TrimRight(firstNonEmpty(credString(c, "baseUrl"), "https://api.pdf.co"), "/"))
				cl := bearerClient(base, c)
				// The OAuth2 credential yields the API key through its userinfo endpoint.
				info, _, err := ec.apiCall("GET", credString(c, "userInfoUrl"), cl.headers, nil, "")
				if err != nil {
					return nil, permanentError{ec.node.Name + ": PDF.co OAuth credentials are not connected or need to be reconnected"}
				}
				key := toString(info["api_key"])
				if key == "" {
					return nil, permanentError{ec.node.Name + ": PDF.co OAuth userinfo response did not include api_key"}
				}
				return &restClient{base: base, headers: map[string]string{"x-api-key": key, "user-agent": "n8n/1.0.6"}}, nil
			}
			c, err := ec.credential("pdfcoApi")
			if err != nil {
				return nil, err
			}
			base := ec.apiBase("pdfco", strings.TrimRight(firstNonEmpty(credString(c, "baseUrl"), "https://api.pdf.co"), "/"))
			return &restClient{base: base, headers: map[string]string{"x-api-key": credString(c, "apiKey"), "user-agent": "n8n/1.0.6"}}, nil
		},
		ops: ops,
	})
}

type pdfcoAction func(ec *execCtx, i int, it Item, c *restClient) (interface{}, error)

// pdfcoPollInterval is the delay between job checks.
var pdfcoPollInterval = 3 * time.Second

// pdfcoCall sends a request and, for async jobs, waits for the result.
func pdfcoCall(ec *execCtx, c *restClient, path string, body map[string]interface{}) (map[string]interface{}, error) {
	pdfcoProfiles(body)
	v, err := c.do(ec, &restRequest{method: "POST", path: path, body: body})
	if err != nil {
		return nil, err
	}
	res := asMap(v)
	if e, ok := res["error"]; ok && toBool(e) {
		return nil, permanentError{fmt.Sprintf("%s: %v", ec.node.Name, firstNonNil(res["message"], "PDF.co error"))}
	}
	job := toString(res["jobId"])
	if job == "" || job == "<nil>" {
		return pdfcoClean(res), nil
	}
	if toString(body["callback"]) != "" {
		return res, nil
	}
	deadline := time.Now().Add(30 * time.Minute)
	for {
		jv, err := c.do(ec, &restRequest{method: "POST", path: "/v1/job/check", body: map[string]interface{}{"jobid": job}})
		if err != nil {
			return nil, err
		}
		check := asMap(jv)
		switch toString(check["status"]) {
		case "working":
			if time.Now().After(deadline) {
				return nil, permanentError{fmt.Sprintf("%s: PDF.co job %s did not finish within 30 minutes", ec.node.Name, job)}
			}
			select {
			case <-ec.ctx.Done():
				return nil, ec.ctx.Err()
			case <-time.After(pdfcoPollInterval):
			}
			continue
		case "success":
			if res["name"] != nil {
				check["name"] = res["name"]
			}
			return pdfcoClean(check), nil
		}
		return nil, permanentError{fmt.Sprintf("%s: PDF.co job %s %v: %s", ec.node.Name, job, check["status"], mustJSON(check))}
	}
}

func pdfcoClean(m map[string]interface{}) map[string]interface{} {
	for _, k := range []string{"jobId", "status", "message", "credits", "remainingCredits"} {
		delete(m, k)
	}
	return m
}

// pdfcoProfiles wraps a profiles string in braces as the package does.
func pdfcoProfiles(body map[string]interface{}) {
	p := strings.TrimSpace(toString(body["profiles"]))
	if body["profiles"] == nil || p == "" {
		delete(body, "profiles")
		return
	}
	if !strings.HasPrefix(p, "{") {
		p = "{ " + p
	}
	if !strings.HasSuffix(p, "}") {
		p += " }"
	}
	body["profiles"] = p
}

// pdfcoOpts copies the set fields of an options collection into body.
func pdfcoOpts(ec *execCtx, i int, coll string, body map[string]interface{}, rename map[string]string) {
	for k, v := range fieldsOf(ec, i, coll) {
		if v == nil || v == "" {
			continue
		}
		if r, ok := rename[k]; ok {
			k = r
		}
		body[k] = v
	}
}

func pdfcoStr(ec *execCtx, i int, name string) string {
	v, _ := ec.param(i, name, "")
	if v == nil {
		return ""
	}
	return toString(v)
}

// pdfcoInline downloads the result file into "body" (parsed as JSON when asked).
func pdfcoInline(ec *execCtx, res map[string]interface{}, parse bool) error {
	u := toString(res["url"])
	if u == "" || u == "<nil>" {
		return nil
	}
	r, err := ec.doRequest("GET", u, nil, nil, "")
	if err != nil {
		return err
	}
	var v interface{} = string(r.body)
	if parse {
		var j interface{}
		if json.Unmarshal(r.body, &j) == nil {
			v = j
		}
	}
	res["body"] = v
	return nil
}

func pdfcoSimple(path, coll string, inline bool, params ...string) pdfcoAction {
	return func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "async": true}
		if inline {
			body["inline"] = true
		}
		for _, p := range params {
			if v := pdfcoStr(ec, i, p); v != "" {
				body[p] = v
			}
		}
		pdfcoOpts(ec, i, coll, body, nil)
		return pdfcoCall(ec, c, path, body)
	}
}

var pdfcoConvertFrom = map[string]string{"toCsv": "/v1/pdf/convert/to/csv", "toHtml": "/v1/pdf/convert/to/html", "toJpg": "/v1/pdf/convert/to/jpg",
	"toJson": "/v1/pdf/convert/to/json", "toJsonMeta": "/v1/pdf/convert/to/json-meta", "toJson2": "/v1/pdf/convert/to/json2", "toPng": "/v1/pdf/convert/to/png",
	"toText": "/v1/pdf/convert/to/text", "toTextSimple": "/v1/pdf/convert/to/text-simple", "toTiff": "/v1/pdf/convert/to/tiff", "toXls": "/v1/pdf/convert/to/xls",
	"toXlsx": "/v1/pdf/convert/to/xlsx", "toXml": "/v1/pdf/convert/to/xml", "toWebp": "/v1/pdf/convert/to/webp"}

var pdfcoConvertFromOpts = map[string]string{"toJpg": "advancedOptions_Image", "toPng": "advancedOptions_Image", "toWebp": "advancedOptions_Image",
	"toTiff": "advancedOptions_Tiff", "toTextSimple": "advancedOptions_TextSimple", "toXls": "advancedOptions_Excel", "toXlsx": "advancedOptions_Excel"}

var pdfcoActions = map[string]pdfcoAction{
	"AI Invoice Parser": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "customfield": pdfcoStr(ec, i, "customfield"), "async": true}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, "/v1/ai-invoice-parser", body)
	},
	"Merge PDF": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		raw, _ := ec.param(i, "url", "")
		var urls []string
		for _, x := range asListOrOne(raw) {
			for _, u := range strings.Split(toString(x), ",") {
				if u = strings.TrimSpace(u); u != "" {
					urls = append(urls, u)
				}
			}
		}
		path := "/v1/pdf/merge"
		if auto, _ := ec.boolean(i, "autoConvert", false); auto {
			path = "/v1/pdf/merge2"
		}
		body := map[string]interface{}{"url": strings.Join(urls, ","), "async": true}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, path, body)
	},
	"Split PDF": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "async": true, "inline": true}
		path := "/v1/pdf/split2"
		switch by, _ := ec.str(i, "splitBy", "pageNumber"); by {
		case "searchText":
			if s := pdfcoStr(ec, i, "searchString"); s != "" {
				body["searchString"] = s
			}
			pdfcoOpts(ec, i, "advancedOptionsTextSearch", body, nil)
		case "barcode":
			if s := pdfcoStr(ec, i, "barcodeSearchString"); s != "" {
				body["searchString"] = s
			}
			pdfcoOpts(ec, i, "advancedOptionsBarcode", body, nil)
		default:
			path = "/v1/pdf/split"
			if p := pdfcoStr(ec, i, "pages"); p != "" {
				body["pages"] = p
			}
			pdfcoOpts(ec, i, "advancedOptionsPageNumber", body, nil)
		}
		return pdfcoCall(ec, c, path, body)
	},
	"URL/HTML to PDF": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		ct, _ := ec.str(i, "convertType", "urlToPDF")
		path := "/v1/pdf/convert/from/html"
		body := map[string]interface{}{"async": true}
		set := func(k string) {
			if v, _ := ec.param(i, k, ""); !restEmpty(v) {
				body[k] = v
			}
		}
		switch ct {
		case "htmlTemplateToPDF":
			set("templateId")
			set("templateData")
		case "htmlToPDF":
			set("html")
		default:
			path = "/v1/pdf/convert/from/url"
			set("url")
		}
		pdfcoOpts(ec, i, "advancedOptions", body, map[string]string{"custom": "paperSize"})
		return pdfcoCall(ec, c, path, body)
	},
	"Convert to PDF": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		ct, _ := ec.str(i, "convertType", "fromDocument")
		body := map[string]interface{}{"async": true}
		if u := pdfcoStr(ec, i, "url"); u != "" {
			body["url"] = u
		}
		path, coll := "", "advancedOptionsCommon"
		switch ct {
		case "fromDocument":
			path = "/v1/pdf/convert/from/doc"
		case "fromCsv":
			path, coll = "/v1/pdf/convert/from/csv", "advancedOptionsCsv"
		case "fromXls", "fromXlsx":
			path = "/v1/" + strings.ToLower(strings.TrimPrefix(ct, "from")) + "/convert/to/pdf"
			if w := pdfcoStr(ec, i, "worksheetIndex"); w != "" {
				var n float64
				if _, err := fmt.Sscan(w, &n); err == nil {
					path, body["worksheetIndex"] = "/v1/xls/convert/to/pdf", w
				}
			}
		case "fromImage":
			path = "/v1/pdf/convert/from/image"
		case "fromEmail":
			path, coll = "/v1/pdf/convert/from/email", "advancedOptionsEmail"
		default:
			return nil, permanentError{ec.node.Name + ": unsupported conversion type " + ct}
		}
		pdfcoOpts(ec, i, coll, body, map[string]string{"custom": "paperSize"})
		return pdfcoCall(ec, c, path, body)
	},
	"Convert from PDF": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		ct, _ := ec.str(i, "convertType", "toText")
		path, ok := pdfcoConvertFrom[ct]
		if !ok {
			return nil, permanentError{ec.node.Name + ": unsupported conversion type " + ct}
		}
		body := map[string]interface{}{"async": true, "inline": true}
		if u := pdfcoStr(ec, i, "url"); u != "" {
			body["url"] = u
		}
		pdfcoOpts(ec, i, firstNonEmpty(pdfcoConvertFromOpts[ct], "advancedOptions"), body, nil)
		res, err := pdfcoCall(ec, c, path, body)
		if err != nil {
			return nil, err
		}
		if toBool(body["inline"]) && ct != "toTiff" && ct != "toXls" && ct != "toXlsx" {
			if err := pdfcoInline(ec, res, ct == "toJpg" || ct == "toPng" || ct == "toWebp"); err != nil {
				return nil, err
			}
		}
		return res, nil
	},
	"Add Text/Images to PDF": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "inline": true, "async": true}
		if v, _ := ec.param(i, "annotations.metadataValues", nil); v != nil {
			var list []interface{}
			for _, x := range asList(v) {
				e := asMap(x)
				a := map[string]interface{}{"text": toString(pick(e, "text")), "x": e["x"], "y": e["y"]}
				pdfcoCopy(e, a, "size", "color", "fontBold", "fontItalic", "fontStrikeout", "fontUnderline", "fontName", "pages", "link", "width", "height", "alignment")
				if t, ok := e["transparent"]; ok {
					a["transparent"] = t
				}
				list = append(list, a)
			}
			body["annotations"] = list
		}
		if v, _ := ec.param(i, "images.metadataValues", nil); v != nil {
			var list []interface{}
			for _, x := range asList(v) {
				e := asMap(x)
				a := map[string]interface{}{"url": e["imageUrl"], "x": e["x"], "y": e["y"]}
				pdfcoCopy(e, a, "width", "height", "pages", "link")
				if k, ok := e["keepAspectRatio"]; ok {
					a["keepAspectRatio"] = k
				}
				list = append(list, a)
			}
			body["images"] = list
		}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, "/v1/pdf/edit/add", body)
	},
	"Fill a PDF Form": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "inline": true, "async": true}
		if v, _ := ec.param(i, "fields.metadataValues", nil); v != nil {
			var list []interface{}
			for _, x := range asList(v) {
				e := asMap(x)
				f := map[string]interface{}{"fieldName": e["fieldName"]}
				if t, ok := e["text"]; ok && t != nil && t != "" {
					f["text"] = toString(t)
				}
				pdfcoCopy(e, f, "pages", "size", "fontBold", "fontItalic", "fontStrikeout", "fontUnderline", "fontName")
				list = append(list, f)
			}
			body["fields"] = list
		}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, "/v1/pdf/edit/add", body)
	},
	"PDF Information & Form Fields": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		path := "/v1/pdf/info"
		if f, _ := ec.boolean(i, "fields", false); f {
			path = "/v1/pdf/info/fields"
		}
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "async": true, "inline": true}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, path, body)
	},
	"Compress PDF": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "async": true, "inline": true}
		pdfcoOpts(ec, i, "advancedOptions", body, map[string]string{"config": "profiles"})
		pdfcoProfiles(body)
		if p, ok := body["profiles"].(string); ok {
			var cfg interface{}
			if err := json.Unmarshal([]byte(p), &cfg); err != nil {
				return nil, permanentError{ec.node.Name + ": compression config is not valid JSON"}
			}
			body["config"] = cfg
			delete(body, "profiles")
		}
		return pdfcoCall(ec, c, "/v2/pdf/compress", body)
	},
	"PDF Security": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "async": true, "inline": true}
		if mode, _ := ec.str(i, "mode", "add_security"); mode == "add_security" {
			body["ownerPassword"], body["userPassword"] = pdfcoStr(ec, i, "ownerPassword"), pdfcoStr(ec, i, "userPassword")
			pdfcoOpts(ec, i, "advancedOptionsAddSecurity", body, nil)
			return pdfcoCall(ec, c, "/v1/pdf/security/add", body)
		}
		body["password"] = pdfcoStr(ec, i, "passwordForRemoveSecurity")
		pdfcoOpts(ec, i, "advancedOptionsRemoveSecurity", body, nil)
		return pdfcoCall(ec, c, "/v1/pdf/security/remove", body)
	},
	"Rotate PDF Pages": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "async": true, "inline": true}
		path := "/v1/pdf/edit/rotate/auto"
		if mode, _ := ec.str(i, "mode", "manual"); mode == "manual" {
			path = "/v1/pdf/edit/rotate"
			var angle int
			_, _ = fmt.Sscan(firstNonEmpty(pdfcoStr(ec, i, "angle"), "90"), &angle)
			body["angle"] = angle
			if p := pdfcoStr(ec, i, "pages"); p != "" {
				body["pages"] = p
			}
		}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, path, body)
	},
	"Delete PDF Pages": pdfcoSimple("/v1/pdf/edit/delete-pages", "advancedOptions", true, "pages"),
	"Search in PDF": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		regex, _ := ec.boolean(i, "regexSearch", false)
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "searchString": pdfcoStr(ec, i, "searchString"), "regexSearch": regex, "inline": true, "async": true}
		if p := pdfcoStr(ec, i, "pages"); p != "" {
			body["pages"] = p
		}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		res, err := pdfcoCall(ec, c, "/v1/pdf/find", body)
		if err == nil && toBool(body["inline"]) {
			err = pdfcoInline(ec, res, true)
		}
		return res, err
	},
	"Search & Replace Text or Delete": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "async": true, "inline": true}
		path := "/v1/pdf/edit/replace-text"
		switch op, _ := ec.str(i, "operationType", "replace"); op {
		case "delete":
			path = "/v1/pdf/edit/delete-text"
			v, _ := ec.param(i, "deleteOperations.metadataValues", nil)
			var s []interface{}
			for _, x := range asList(v) {
				if t := asMap(x)["searchString"]; t != nil && t != "" {
					s = append(s, toString(t))
				}
			}
			body["searchStrings"] = s
		case "replaceWithImage":
			path = "/v1/pdf/edit/replace-text-with-image"
			body["searchString"], body["replaceImage"] = pdfcoStr(ec, i, "searchStringForReplaceWithImage"), pdfcoStr(ec, i, "replaceImage")
		default:
			v, _ := ec.param(i, "replaceOperations.metadataValues", nil)
			var s, r []interface{}
			for _, x := range asList(v) {
				e := asMap(x)
				if t := e["searchString"]; t != nil && t != "" {
					s = append(s, toString(t))
					r = append(r, toString(pick(e, "replaceString")))
				}
			}
			body["searchStrings"], body["replaceStrings"] = s, r
		}
		if p := pdfcoStr(ec, i, "pages"); p != "" {
			body["pages"] = p
		}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, path, body)
	},
	"Barcode Reader": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		tv, _ := ec.param(i, "types", nil)
		var types []string
		for _, t := range asListOrOne(tv) {
			types = append(types, toString(t))
		}
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "types": strings.Join(types, ","), "async": true, "inline": true}
		if p := pdfcoStr(ec, i, "pages"); p != "" {
			body["pages"] = p
		}
		pdfcoOpts(ec, i, "advancedOptions", body, map[string]string{"opticalMarks": "types"})
		return pdfcoCall(ec, c, "/v1/barcode/read/from/url", body)
	},
	"Barcode Generator": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		typ, _ := ec.str(i, "type", "QRCode")
		body := map[string]interface{}{"value": pdfcoStr(ec, i, "value"), "type": typ, "async": true}
		if d := pdfcoStr(ec, i, "decorationImage"); typ == "QRCode" && d != "" {
			body["decorationImage"] = d
		}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, "/v1/barcode/generate", body)
	},
	"Make PDF Searchable or Unsearchable": func(ec *execCtx, i int, _ Item, c *restClient) (interface{}, error) {
		body := map[string]interface{}{"url": pdfcoStr(ec, i, "url"), "async": true}
		path := "/v1/pdf/makeunsearchable"
		if opt, _ := ec.str(i, "searchableOptions", "makeSearchable"); opt == "makeSearchable" {
			path, body["lang"] = "/v1/pdf/makesearchable", pdfcoStr(ec, i, "lang")
		}
		pdfcoOpts(ec, i, "advancedOptions", body, nil)
		return pdfcoCall(ec, c, path, body)
	},
	"Upload File to PDF.co": pdfcoUpload,
}

func pdfcoCopy(src, dst map[string]interface{}, keys ...string) {
	for _, k := range keys {
		if v := src[k]; v != nil && v != "" && v != false && v != 0.0 {
			dst[k] = v
		}
	}
}

func pdfcoUpload(ec *execCtx, i int, it Item, c *restClient) (interface{}, error) {
	name := pdfcoStr(ec, i, "name")
	if m, _ := ec.str(i, "uploadMethod", "presignedUrl"); m == "base64" {
		v, err := c.do(ec, &restRequest{method: "POST", path: "/v1/file/upload/base64", body: map[string]interface{}{"file": pdfcoStr(ec, i, "base64Content"), "name": name}})
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"url": asMap(v)["url"]}, nil
	}
	v, err := c.do(ec, &restRequest{method: "GET", path: "/v1/file/upload/get-presigned-url", query: url.Values{"name": {name}}})
	if err != nil {
		return nil, err
	}
	res := asMap(v)
	var data []byte
	if b, _ := ec.boolean(i, "binaryData", false); b {
		prop, _ := ec.str(i, "binaryPropertyName", "data")
		if data, _, _, err = binaryOf(it, prop); err != nil {
			return nil, err
		}
	} else {
		data = []byte(pdfcoStr(ec, i, "fileContent"))
	}
	if _, err := ec.doRequest("PUT", toString(res["presignedUrl"]), nil, data, "application/octet-stream"); err != nil {
		return nil, err
	}
	return map[string]interface{}{"url": res["url"]}, nil
}
