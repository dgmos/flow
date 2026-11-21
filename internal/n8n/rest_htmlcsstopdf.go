package n8n

import (
	"strings"
)

// HTML to PDF / PDFMunk (community node n8n-nodes-htmlcsstopdf.htmlcsstopdf).
// Package versions before 3.0 had no resource (only HTML/URL to PDF).

// pdfMunkOp describes one PDFMunk call: the parameter prefix names its
// <p>_input_type / <p>_url / <p>_file_binary_property parameters, out names
// the output-format parameter.
type pdfMunkOp struct {
	path, prefix, urlParam, fileField string
	multiFile                         bool // merge: several binary properties
	out, outDef, nameParam, nameDef   string
	fileExt                           string // fallback extension of binary outputs
	fields                            func(a *cmtyArgs, body map[string]interface{}) error
}

func init() {
	ops := map[string]*pdfMunkOp{
		"pdfCreation/htmlToPdf": {path: "/generatePdf", out: "output_format", outDef: "url", nameParam: "output_filename", nameDef: "document", fileExt: ".pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				b["output_filename"] = a.str("output_filename", "document")
				b["html_content"] = a.get("html_content", "<html>Hello HTML</html>")
				b["css_content"] = a.get("css_content", "")
				b["viewPortWidth"] = a.get("viewPortWidth", 1080.0)
				b["viewPortHeight"] = a.get("viewPortHeight", 720.0)
				b["output_format"] = a.str("output_format", "url")
				b["wait_until"] = a.get("wait_until", "load")
				b["wait_till"] = a.get("wait_till", 0.0)
				var dp []interface{}
				for _, x := range asList(asMap(a.get("dynamic_params", nil))["params"]) {
					m := asMap(x)
					if k := toString(m["key"]); k != "" {
						v := m["value"]
						if v == nil {
							v = ""
						}
						dp = append(dp, map[string]interface{}{k: v})
					}
				}
				if len(dp) > 0 {
					b["dynamic_params"] = dp
				}
				return nil
			}},
		"pdfCreation/urlToPdf": {path: "/generatePdf", out: "output_format", outDef: "url", nameParam: "output_filename", nameDef: "document", fileExt: ".pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				b["output_filename"] = a.str("output_filename", "document")
				b["url"] = a.get("url", "https://example.com")
				b["full_page"] = a.get("full_page", true)
				b["wait_till"] = a.get("wait_till", 10000.0)
				b["viewPortWidth"] = a.get("viewPortWidth", 1080.0)
				b["viewPortHeight"] = a.get("viewPortHeight", 720.0)
				b["output_format"] = a.str("output_format", "url")
				return nil
			}},
		"pdfManipulation/mergePdfs": {path: "/pdf/merge", prefix: "merge", fileField: "files", multiFile: true, out: "merge_output", outDef: "url", nameDef: "merged.pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				if a.str("merge_input_type", "url") == "url" {
					var urls []interface{}
					for _, u := range strings.Split(a.str("pdf_urls", "https://pdfmunk.com/sample.pdf, https://pdfmunk.com/sample1.pdf"), ",") {
						if u = strings.TrimSpace(u); u != "" {
							urls = append(urls, u)
						}
					}
					if len(urls) < 2 {
						return permanentError{"At least 2 PDF URLs are required for merging"}
					}
					if len(urls) > 25 {
						return permanentError{"Maximum 25 PDF URLs allowed for merging"}
					}
					b["urls"] = urls
				}
				return nil
			}},
		"pdfManipulation/splitPdf": {path: "/pdf/split", prefix: "split", urlParam: "split_url", fileField: "file", out: "split_output", outDef: "url", nameDef: "split.pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				switch a.str("split_mode", "pages") {
				case "pages":
					b["pages"] = a.get("pages", "1-3")
				case "each":
					b["mode"] = "each"
				case "chunks":
					b["chunks"] = a.get("chunks", 2.0)
				}
				return nil
			}},
		"pdfManipulation/compressPdf": {path: "/compressPdf", prefix: "compress", urlParam: "compress_url", fileField: "file", out: "compress_output", outDef: "file",
			nameParam: "compress_output_name", nameDef: "compressed.pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				b["compression"] = a.get("compression", "high")
				b["output_name"] = a.str("compress_output_name", "compressed.pdf")
				return nil
			}},
		"pdfManipulation/watermarkPdf": {path: "/watermark", prefix: "watermark", fileField: "file", out: "watermark_output_format", outDef: "file", nameDef: "watermarked.pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				text := a.get("watermark_text", "CONFIDENTIAL")
				b["output_format"], b["text"], b["watermark_text"] = b["output"], text, text
				b["opacity"] = a.get("watermark_opacity", 0.15)
				b["angle"] = a.get("watermark_angle", 30.0)
				if a.str("watermark_input_type", "url") == "url" {
					u := a.get("watermark_file_url", "https://pdfmunk.com/sample.pdf")
					b["file_url"], b["url"] = u, u
				}
				if fs := a.num("watermark_font_size", 0); fs > 0 {
					b["font_size"] = fs
				}
				return nil
			}},
		"pdfManipulation/convertPdfToImage": {path: "/convert/pdf/image", prefix: "convert_pdf_image", urlParam: "convert_pdf_image_url", fileField: "file",
			out: "convert_pdf_image_output", outDef: "url",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				if a.str("convert_pdf_image_input_type", "url") == "url" {
					b["url"] = a.get("convert_pdf_image_url", "https://pdfmunk.com/sample.pdf")
				}
				b["image_format"] = a.str("convert_pdf_image_format", "png")
				b["dpi"] = a.get("convert_pdf_image_dpi", 150.0)
				b["quality"] = a.get("convert_pdf_image_quality", 85.0)
				if p := strings.TrimSpace(a.str("convert_pdf_image_pages", "1")); p != "" {
					b["pages"] = p
				} else if pg := a.num("convert_pdf_image_page", 1); pg > 0 {
					b["page"] = pg
				}
				return nil
			}},
		"pdfManipulation/convertImageToPdf": {path: "/convert/image/pdf", out: "convert_image_pdf_output", outDef: "url", nameParam: "convert_image_pdf_filename", nameDef: "combined-images.pdf", fileExt: ".pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				var urls []interface{}
				for _, u := range strings.Split(a.str("convert_image_pdf_urls", ""), ",") {
					if u = strings.TrimSpace(u); u != "" {
						urls = append(urls, u)
					}
				}
				if len(urls) == 0 {
					return permanentError{"At least one image URL is required"}
				}
				if len(urls) > 100 {
					return permanentError{"Maximum 100 image URLs are allowed"}
				}
				b["output_filename"] = a.str("convert_image_pdf_filename", "combined-images.pdf")
				if len(urls) == 1 {
					b["image_url"] = urls[0]
				} else {
					b["image_urls"] = urls
				}
				return nil
			}},
		"pdfSecurity/lockPdf": {path: "/lockPdf", prefix: "lock", urlParam: "lock_url", fileField: "file", out: "lock_output", outDef: "file", nameParam: "lock_output_name", nameDef: "locked.pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				b["password"] = a.get("lock_password", "")
				b["output_name"] = a.str("lock_output_name", "locked.pdf")
				if p := a.str("lock_input_password", ""); p != "" {
					b["input_password"] = p
				}
				return nil
			}},
		"pdfSecurity/unlockPdf": {path: "/unlockPdf", prefix: "unlock", urlParam: "unlock_url", fileField: "file", out: "unlock_output", outDef: "file", nameParam: "unlock_output_name", nameDef: "unlocked.pdf",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				b["password"] = a.get("unlock_password", "")
				b["output_name"] = a.str("unlock_output_name", "unlocked.pdf")
				return nil
			}},
		"pdfParsing/parsePdf": {path: "/pdf/parse", prefix: "parse", urlParam: "parse_url", fileField: "file",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				b["mode"] = a.get("parse_mode", "full")
				b["pages"] = a.get("parse_pages", "all")
				return nil
			}},
		"pdfParsing/parsePdfOcr": {path: "/pdf/ocr/parse", prefix: "parse", urlParam: "parse_url", fileField: "file",
			fields: func(a *cmtyArgs, b map[string]interface{}) error {
				b["pages"] = a.get("parse_pages", "all")
				b["lang"] = a.get("lang", "eng")
				b["dpi"] = a.get("dpi", 200.0)
				b["psm"] = a.get("psm", 3.0)
				b["oem"] = a.get("oem", 3.0)
				return nil
			}},
	}
	rops := map[string]*restOp{}
	for k, op := range ops {
		rops[k] = &restOp{run: op.run}
	}
	registerREST("n8n-nodes-htmlcsstopdf.htmlcsstopdf", &restService{
		defResource: "pdfCreation",
		defOps:      map[string]string{"pdfCreation": "htmlToPdf", "pdfManipulation": "mergePdfs", "pdfSecurity": "lockPdf", "pdfParsing": "parsePdf"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "htmlcsstopdfApi", nil)
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("htmlcsstopdf", "https://pdfmunk.com/api/v1"), headers: map[string]string{"CLIENT-API-KEY": credString(c, "apiKey")}}, nil
		},
		ops: rops,
	})
}

func (op *pdfMunkOp) run(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	a := &cmtyArgs{ec: ec, i: i}
	body := map[string]interface{}{}
	output := ""
	if op.out != "" {
		output = a.str(op.out, op.outDef)
		if op.out != "output_format" {
			body["output"] = output
		}
	}
	input := "url"
	if op.prefix != "" {
		input = a.str(op.prefix+"_input_type", "url")
		if input == "url" && op.urlParam != "" {
			body["url"] = a.get(op.urlParam, "")
		}
	}
	if err := op.fields(a, body); err != nil {
		return nil, err
	}
	if a.err != nil {
		return nil, a.err
	}
	var raw []byte
	ct := ""
	if input == "file" && op.fileField != "" {
		var files []cmtyFile
		var props []string
		if op.multiFile {
			props = strings.Split(a.str("merge_file_binary_properties", "data,data"), ",")
		} else {
			props = []string{a.str(op.prefix+"_file_binary_property", "data")}
		}
		for _, p := range props {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			data, mt, name, err := binaryOf(it, p)
			if err != nil {
				return nil, permanentError{"Binary property \"" + p + "\" not found"}
			}
			if toString(asMap(it.Binary[p])["fileName"]) == "" {
				name = p + ".pdf"
			}
			if toString(asMap(it.Binary[p])["mimeType"]) == "" {
				mt = "application/pdf"
			}
			files = append(files, cmtyFile{op.fileField, name, mt, data})
		}
		if op.multiFile && len(files) < 2 {
			return nil, permanentError{"At least 2 binary PDF properties are required"}
		}
		var fields [][2]string
		for _, k := range cmtyKeys(body) {
			fields = append(fields, [2]string{k, toString(body[k])})
		}
		raw, ct = cmtyMultipart(fields, files...)
	}
	if output != "file" {
		if raw != nil {
			res, err := cmtyRaw(ec, c, "POST", op.path, nil, raw, ct)
			if err != nil {
				return nil, err
			}
			return []Item{{JSON: cmtyObject(decodeJSONBody(res.body))}}, nil
		}
		v, err := cmtyCall(ec, c, "POST", op.path, nil, body)
		if err != nil {
			return nil, err
		}
		return []Item{{JSON: cmtyObject(v)}}, nil
	}
	if raw == nil {
		raw, ct = []byte(mustJSON(body)), "application/json"
	}
	res, err := cmtyRaw(ec, c, "POST", op.path, nil, raw, ct)
	if err != nil {
		return nil, err
	}
	mt := strings.TrimSpace(strings.Split(res.contentType, ";")[0])
	if mt == "" {
		mt = "application/pdf"
		if op.prefix == "convert_pdf_image" {
			mt = "application/octet-stream"
		}
	}
	name := op.nameDef
	if op.nameParam != "" {
		name = a.str(op.nameParam, op.nameDef)
	}
	switch {
	case op.prefix == "convert_pdf_image":
		name = "pdf-page." + a.str("convert_pdf_image_format", "png")
	case op.out == "output_format":
		name += ".pdf"
	case op.fileExt != "" && !strings.HasSuffix(name, op.fileExt):
		name += op.fileExt
	}
	if name == "" {
		name = "output"
	}
	if !strings.Contains(name, ".") && strings.Contains(mt, "/") {
		switch {
		case strings.Contains(mt, "pdf"):
			name += ".pdf"
		case strings.Contains(mt, "zip"):
			name += ".zip"
		default:
			name += ".bin"
		}
	}
	return []Item{{JSON: map[string]interface{}{"success": true}, Binary: map[string]interface{}{"data": binaryData(res.body, mt, name)}}}, nil
}
