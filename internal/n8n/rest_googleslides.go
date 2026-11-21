package n8n

// Google Slides (n8n-nodes-base.googleSlides), Slides API v1.

func init() {
	registerREST("googleSlides", &restService{
		defResource: "presentation",
		defOps:      map[string]string{"presentation": "create", "page": "get"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			h, err := googleAuthDefault(ec, "docs", "googleSlidesOAuth2Api")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("googleSlides", "https://slides.googleapis.com/v1"), headers: h}, nil
		},
		ops: map[string]*restOp{
			"presentation/create": {method: "POST", path: "/presentations", body: "title"},
			"presentation/get":    {path: "/presentations/{presentationId}"},
			"presentation/getSlides": {path: "/presentations/{presentationId}", query: "=fields:slides",
				post: func(ec *execCtx, i int, v interface{}) (interface{}, error) {
					slides := asList(asMap(v)["slides"])
					if all, _ := ec.boolean(0, "returnAll", false); !all {
						n, _ := ec.num(i, "limit", 100)
						if int(n) >= 0 && int(n) < len(slides) {
							slides = slides[:int(n)]
						}
					}
					return slides, nil
				}},
			"presentation/replaceText": {method: "POST", path: "/presentations/{presentationId}:batchUpdate",
				build: func(ec *execCtx, i int, r *restRequest) error {
					v, err := ec.param(i, "textUi.textValues", nil)
					if err != nil {
						return err
					}
					reqs := []interface{}{}
					for _, x := range asList(v) {
						t := asMap(x)
						ids := t["pageObjectIds"]
						if ids == nil {
							ids = []interface{}{}
						}
						reqs = append(reqs, map[string]interface{}{"replaceAllText": map[string]interface{}{
							"replaceText":   t["replaceText"],
							"pageObjectIds": ids,
							"containsText":  map[string]interface{}{"text": t["text"], "matchCase": t["matchCase"]},
						}})
					}
					// n8n builds writeControl from options.revisionId but never sends it.
					r.body = map[string]interface{}{"requests": reqs}
					return nil
				}},
			"page/get":          {path: "/presentations/{presentationId}/pages/{pageObjectId}"},
			"page/getThumbnail": {run: slidesThumbnail},
		},
	})
}

// googleAuthDefault is googleAuth for nodes whose version 1 defaults the
// authentication parameter to serviceAccount (Slides, BigQuery v1).
func googleAuthDefault(ec *execCtx, service, oauthType string) (map[string]string, error) {
	if _, set := ec.params["authentication"]; !set && ec.version < 2 {
		c, err := ec.credential("googleApi")
		if err != nil {
			return nil, err
		}
		tok, err := ec.serviceAccountToken(c, googleScopes[service])
		if err != nil {
			return nil, err
		}
		return map[string]string{"Authorization": "Bearer " + tok}, nil
	}
	return ec.googleAuth(service, oauthType)
}

func slidesThumbnail(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	r, err := (&restOp{path: "/presentations/{presentationId}/pages/{pageObjectId}/thumbnail"}).request(ec, i)
	if err != nil {
		return nil, err
	}
	v, err := c.do(ec, r)
	if err != nil {
		return nil, err
	}
	res := asMap(v)
	if dl, _ := ec.boolean(0, "download", false); !dl {
		return restItems(v), nil
	}
	raw, err := ec.doRequest("GET", toString(res["contentUrl"]), nil, nil, "")
	if err != nil {
		return nil, err
	}
	prop, _ := ec.str(i, "binaryProperty", "data")
	page, _ := ec.str(i, "pageObjectId", "")
	mt := raw.contentType
	if mt == "" {
		mt = "image/png"
	}
	return []Item{{JSON: res, Binary: map[string]interface{}{firstNonEmpty(prop, "data"): binaryData(raw.body, mt, page+".png")}}}, nil
}
