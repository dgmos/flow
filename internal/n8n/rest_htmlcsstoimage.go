package n8n

import "strings"

// HTML to Image (community node n8n-nodes-htmlcsstoimage.htmlCssToImage):
// POST https://htmlcsstoimg.com/api/v1/generateImage. JSON answers become the
// item; image answers (png/image formats) become a binary "data" property.

func init() {
	registerREST("n8n-nodes-htmlcsstoimage.htmlCssToImage", &restService{
		defOps: map[string]string{"": "htmlToImage"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("htmlcsstoimgApi")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("htmlCssToImage", "https://htmlcsstoimg.com"), headers: map[string]string{"CLIENT-API-KEY": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"/htmlToImage": {run: hctiRun(false)},
			"/urlToImage":  {run: hctiRun(true)},
		},
	})
}

func hctiRun(url bool) func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		a := &cmtyArgs{ec: ec, i: i}
		b := map[string]interface{}{}
		if url {
			b["url"] = a.get("url", "")
			b["full_page"] = a.get("full_page", true)
			b["wait_till"] = a.get("wait_till", 10000.0)
		} else {
			b["html_content"] = a.get("html_content", "")
			b["css_content"] = a.get("css_content", "")
		}
		b["viewPortWidth"] = a.get("viewPortWidth", 1080.0)
		b["viewPortHeight"] = a.get("viewPortHeight", 720.0)
		if url {
			b["response_format"] = a.get("response_format_url", "image")
		} else {
			b["response_format"] = a.get("response_format_html", "url")
		}
		if a.err != nil {
			return nil, a.err
		}
		res, err := cnRaw(ec, c, &restRequest{method: "POST", path: "/api/v1/generateImage", body: b})
		if err != nil {
			return nil, err
		}
		if mt := strings.ToLower(res.contentType); strings.HasPrefix(mt, "image/") || strings.HasPrefix(mt, "application/octet-stream") {
			mt = strings.TrimSpace(strings.SplitN(res.contentType, ";", 2)[0])
			if mt == "application/octet-stream" {
				mt = sniffMime(res.body)
			}
			return []Item{cnBinaryItem(nil, "data", res.body, mt, "image."+firstNonEmpty(extFromMime(mt), "png"))}, nil
		}
		return []Item{{JSON: cmtyObject(cnDecode(res))}}, nil
	}
}
