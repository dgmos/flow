package n8n

import (
	"bytes"
	"encoding/base64"
	"mime/multipart"
	"net/http"
	"strings"
)

// Small REST services: SendGrid, Mailgun, DeepL, Google Translate,
// Clearbit and Jina AI.

func init() {
	registerREST("sendGrid", &restService{
		defResource: "list",
		defOps:      map[string]string{"list": "create", "contact": "upsert", "mail": "send"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("sendGridApi")
			if err != nil {
				return nil, err
			}
			return bearerClient(ec.apiBase("sendGrid", "https://api.sendgrid.com/v3"), c, "apiKey"), nil
		},
		ops: map[string]*restOp{
			"mail/send": {run: sendGridMail},
			"contact/getAll": {path: "/marketing/contacts", list: true, listKey: "result", pager: nextURLPager("_metadata.next"),
				build: func(ec *execCtx, i int, r *restRequest) error {
					if q, _ := ec.str(i, "filters.query", ""); q != "" {
						r.method, r.path, r.body = "POST", "/marketing/contacts/search", map[string]interface{}{"query": q}
					}
					return nil
				}},
			"contact/get": {path: "/marketing/contacts/{contactId?}",
				build: func(ec *execCtx, i int, r *restRequest) error {
					if by, _ := ec.str(0, "by", "id"); by == "email" {
						email, _ := ec.str(i, "email", "")
						r.method, r.path = "POST", "/marketing/contacts/search"
						r.body = map[string]interface{}{"query": "email LIKE '" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, "%", `\%`, "_", `\_`).Replace(email) + "' "}
					}
					return nil
				},
				post: func(_ *execCtx, _ int, v interface{}) (interface{}, error) {
					if r := jpath(v, "result"); r != nil {
						v = r
					}
					if l, ok := v.([]interface{}); ok {
						if len(l) == 0 {
							return nil, nil
						}
						return l[0], nil
					}
					return v, nil
				}},
			"contact/upsert": {method: "PUT", path: "/marketing/contacts", build: func(ec *execCtx, i int, r *restRequest) error {
				contact := map[string]interface{}{}
				if err := ec.restFields(i, "email @additionalFields addressUi.addressValues.address1:address_line_1 addressUi.addressValues.address2:address_line_2 city country "+
					"firstName:first_name lastName:last_name postalCode:postal_code stateProvinceRegion:state_province_region alternateEmails:alternate_emails:csv", contact); err != nil {
					return err
				}
				// customFieldValues use fieldId/fieldValue.
				if v, _ := ec.param(i, "additionalFields.customFieldsUi.customFieldValues", nil); v != nil {
					cf := map[string]interface{}{}
					for _, x := range asList(v) {
						m := asMap(x)
						cf[toString(m["fieldId"])] = m["fieldValue"]
					}
					contact["custom_fields"] = cf
				}
				r.body = map[string]interface{}{"contacts": []interface{}{contact}}
				if l, _ := ec.param(i, "additionalFields.listIdsUi.listIdValues.listIds", nil); l != nil {
					r.body["list_ids"] = asListOrOne(l)
				}
				return nil
			}},
			"contact/delete": {method: "DELETE", path: "/marketing/contacts", build: func(ec *execCtx, i int, r *restRequest) error {
				if all, _ := ec.boolean(i, "deleteAll", false); all {
					r.query.Set("delete_all_contacts", "true")
				}
				ids, _ := ec.str(i, "ids", "")
				r.query.Set("ids", strings.Join(strings.Fields(ids), ""))
				return nil
			}},
			"list/getAll": {path: "/marketing/lists", list: true, listKey: "result", pager: nextURLPager("_metadata.next")},
			"list/get":    {path: "/marketing/lists/{listId}", query: "contactSample:contact_sample:bool"},
			"list/create": {method: "POST", path: "/marketing/lists", body: "name"},
			"list/delete": {method: "DELETE", path: "/marketing/lists/{listId}", query: "deleteContacts:delete_contacts:bool", success: true},
			"list/update": {method: "PATCH", path: "/marketing/lists/{listId}", body: "name"},
		},
	})

	registerREST("mailgun", &restService{
		client: func(*execCtx, int) (*restClient, error) { return &restClient{}, nil },
		ops:    map[string]*restOp{"/": {run: mailgunSend}},
	})

	registerREST("deepL", &restService{
		defResource: "language",
		defOps:      map[string]string{"language": "translate"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("deepLApi")
			if err != nil {
				return nil, err
			}
			base := "https://api-free.deepl.com/v2"
			if credString(c, "apiPlan") == "pro" {
				base = "https://api.deepl.com/v2"
			}
			return &restClient{base: ec.apiBase("deepL", base), headers: map[string]string{"Authorization": "DeepL-Auth-Key " + credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"language/translate": {method: "POST", path: "/translate", form: true, out: "translations.0",
				body: "text translateTo:target_lang @additionalFields sourceLang:source_lang splitSentences:split_sentences preserveFormatting:preserve_formatting formality",
				build: func(_ *execCtx, _ int, r *restRequest) error {
					if s := toString(r.body["source_lang"]); s == "EN-GB" || s == "EN-US" {
						r.body["source_lang"] = "EN"
					}
					return nil
				}},
		},
	})

	registerREST("googleTranslate", &restService{
		defResource: "language",
		defOps:      map[string]string{"language": "translate"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			h, err := ec.googleAuth("translate", "googleTranslateOAuth2Api")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("translate", "https://translation.googleapis.com"), headers: h}, nil
		},
		ops: map[string]*restOp{
			"language/translate": {method: "POST", path: "/language/translate/v2", body: "text:q translateTo:target", out: "data.translations.0"},
		},
	})

	registerREST("clearbit", &restService{
		defResource: "company",
		defOps:      map[string]string{"company": "enrich", "person": "enrich"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("clearbitApi")
			if err != nil {
				return nil, err
			}
			r, o := ec.resourceOpBy(i, "company", map[string]string{"company": "enrich", "person": "enrich"})
			host := r + "-stream"
			if o == "autocomplete" {
				host = "autocomplete"
			}
			return &restClient{base: ec.apiBase("clearbit", "https://"+host+".clearbit.com"), headers: map[string]string{"Authorization": "Bearer " + credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"person/enrich":        {path: "/v2/people/find", query: "email @additionalFields givenName:given_name familyName:family_name ipAddress:ip_address location company companyDomain:company_domain linkedIn:linkedin twitter facebook"},
			"company/enrich":       {path: "/v2/companies/find", query: "domain @additionalFields companyName:company_name linkedin twitter facebook"},
			"company/autocomplete": {path: "/v1/companies/suggest", query: "name:query"},
		},
	})

	jina := &restService{
		defResource: "reader",
		defOps:      map[string]string{"reader": "read", "research": "deepResearch"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("jinaAiApi")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("jinaAi", ""), headers: map[string]string{"Authorization": "Bearer " + credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"reader/read":           {run: jinaRun},
			"reader/search":         {run: jinaRun},
			"research/deepResearch": {run: jinaRun},
		},
	}
	registerREST("jinaAi", jina)
}

func sendGridMail(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	emails := func(s string) []interface{} {
		var out []interface{}
		for _, e := range strings.Split(s, ",") {
			if e = strings.TrimSpace(e); e != "" {
				out = append(out, map[string]interface{}{"email": e})
			}
		}
		return out
	}
	to, _ := ec.str(i, "toEmail", "")
	from, _ := ec.str(i, "fromEmail", "")
	fromName, _ := ec.str(i, "fromName", "")
	af := fieldsOf(ec, i, "additionalFields")
	pers := map[string]interface{}{"to": emails(to)}
	body := map[string]interface{}{
		"personalizations": []interface{}{pers},
		"from":             map[string]interface{}{"email": strings.TrimSpace(from), "name": fromName},
		"mail_settings":    map[string]interface{}{"sandbox_mode": map[string]interface{}{"enable": toBool(af["enableSandbox"])}},
	}
	if dyn, _ := ec.boolean(i, "dynamicTemplate", false); dyn {
		body["template_id"], _ = ec.str(i, "templateId", "")
		if f, _ := ec.param(i, "dynamicTemplateFields.fields", nil); f != nil {
			data := map[string]interface{}{}
			for _, x := range asList(f) {
				m := asMap(x)
				data[toString(m["key"])] = m["value"]
			}
			pers["dynamic_template_data"] = data
		}
	} else {
		pers["subject"], _ = ec.str(i, "subject", "")
		ct, _ := ec.str(i, "contentType", "text/plain")
		cv, _ := ec.str(i, "contentValue", "")
		body["content"] = []interface{}{map[string]interface{}{"type": ct, "value": cv}}
	}
	if a := toString(af["attachments"]); a != "" {
		var atts []interface{}
		for _, p := range splitList(a) {
			data, mt, name, err := binaryOf(it, p)
			if err != nil {
				return nil, err
			}
			atts = append(atts, map[string]interface{}{"content": base64.StdEncoding.EncodeToString(data), "filename": firstNonEmpty(name, "unknown"), "type": mt})
		}
		body["attachments"] = atts
	}
	if s := toString(af["bccEmail"]); s != "" {
		pers["bcc"] = emails(s)
	}
	if s := toString(af["ccEmail"]); s != "" {
		pers["cc"] = emails(s)
	}
	if h := asList(asMap(af["headers"])["details"]); len(h) > 0 {
		hs := map[string]interface{}{}
		for _, x := range h {
			m := asMap(x)
			hs[toString(m["key"])] = m["value"]
		}
		body["headers"] = hs
	}
	if s := toString(af["categories"]); s != "" {
		body["categories"] = strings.Split(s, ",")
	}
	if s := toString(af["ipPoolName"]); s != "" {
		body["ip_pool_name"] = s
	}
	if v := af["sendAt"]; !restEmpty(v) {
		if t, ok := timeOf(v); ok {
			pers["send_at"] = t.Unix()
		}
	}
	if s := toString(af["replyToEmail"]); s != "" {
		body["reply_to_list"] = emails(s)
	}
	_, hdr, err := c.doH(ec, &restRequest{method: "POST", path: "/mail/send", body: body}, "")
	if err != nil {
		return nil, err
	}
	return []Item{{JSON: map[string]interface{}{"messageId": hdr.Get("X-Message-Id")}}}, nil
}

func mailgunSend(ec *execCtx, i int, it Item, _ *restClient) ([]Item, error) {
	c, err := ec.credential("mailgunApi")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	field := func(k, v string) { _ = w.WriteField(k, v) }
	str := func(k string) string { s, _ := ec.str(i, k, ""); return s }
	field("from", str("fromEmail"))
	field("to", str("toEmail"))
	field("subject", str("subject"))
	field("text", str("text"))
	field("html", str("html"))
	if s := str("ccEmail"); s != "" {
		field("cc", s)
	}
	if s := str("bccEmail"); s != "" {
		field("bcc", s)
	}
	if s := strings.TrimSpace(str("replyTo")); s != "" {
		field("h:Reply-To", s)
	}
	if h, _ := ec.param(i, "customHeaders.headers", nil); h != nil {
		for _, x := range asList(h) {
			m := asMap(x)
			if n := strings.TrimSpace(toString(m["name"])); n != "" {
				field("h:"+n, toString(m["value"]))
			}
		}
	}
	for _, t := range splitList(str("tags")) {
		field("o:tag", t)
	}
	if a := str("attachments"); a != "" && it.Binary != nil {
		for _, p := range splitList(a) {
			data, _, name, err := binaryOf(it, p)
			if err != nil {
				return nil, err
			}
			fw, _ := w.CreateFormFile("attachment", firstNonEmpty(name, "unknown"))
			_, _ = fw.Write(data)
		}
	}
	_ = w.Close()
	base := ec.apiBase("mailgun", "https://"+firstNonEmpty(credString(c, "apiDomain"), "api.mailgun.net"))
	u := base + "/v3/" + credString(c, "emailDomain") + "/messages"
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("api:"+credString(c, "apiKey")))
	res, _, err := ec.apiCall(http.MethodPost, u, map[string]string{"Authorization": auth}, buf.Bytes(), w.FormDataContentType())
	if err != nil {
		return nil, err
	}
	return []Item{{JSON: res}}, nil
}

func jinaRun(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	r, o := ec.resourceOpBy(i, "reader", map[string]string{"reader": "read", "research": "deepResearch"})
	opts := fieldsOf(ec, i, "options")
	simplify, _ := ec.boolean(i, "simplify", true)
	req := &restRequest{method: "GET", headers: map[string]string{}}
	setH := func(k string, v interface{}) {
		if !restEmpty(v) {
			req.headers[k] = toString(v)
		}
	}
	base := c.base
	switch r + "/" + o {
	case "reader/read":
		u, _ := ec.str(i, "url", "")
		req.path = "/" + u
		base = firstNonEmpty(base, "https://r.jina.ai")
		setH("X-Return-Format", opts["outputFormat"])
		setH("X-Target-Selector", opts["targetSelector"])
		setH("X-Remove-Selector", opts["excludeSelector"])
		if toBool(opts["enableImageCaptioning"]) {
			setH("X-With-Generated-Alt", "true")
		}
		setH("X-Wait-For-Selector", opts["waitForSelector"])
	case "reader/search":
		q, _ := ec.str(i, "searchQuery", "")
		req.path = "/?q=" + urlQueryEscape(q)
		if p := opts["pageNumber"]; !restEmpty(p) {
			req.path += "&page=" + toString(p)
		}
		base = firstNonEmpty(base, "https://s.jina.ai")
		setH("X-Return-Format", opts["outputFormat"])
		setH("X-Site", opts["siteFilter"])
	case "research/deepResearch":
		q, _ := ec.str(i, "researchQuery", "")
		req.method, req.path = "POST", "/v1/chat/completions"
		base = firstNonEmpty(base, "https://deepsearch.jina.ai")
		req.body = map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "user", "content": q}}}
		if v := opts["maxReturnedSources"]; !restEmpty(v) {
			req.body["max_returned_urls"] = v
		}
		for k, f := range map[string]string{"prioritizeSources": "boost_hostnames", "excludeSources": "bad_hostnames", "siteFilter": "only_hostnames"} {
			if s := toString(opts[k]); s != "" {
				req.body[f] = splitList(s)
			}
		}
	default:
		return nil, unsupportedOp(ec.node.Name, r, o)
	}
	cl := *c
	cl.base = base
	v, err := cl.do(ec, req)
	if err != nil {
		return nil, err
	}
	if simplify {
		if o == "deepResearch" {
			v = map[string]interface{}{"content": jpath(v, "choices.0.message.content"), "annotations": jpath(v, "choices.0.message.annotations"), "usage": jpath(v, "usage")}
		} else if d := jpath(v, "data"); d != nil {
			v = d
		}
	}
	return restItems(v), nil
}

func init() {
	registerREST("mistralAi", &restService{
		defResource: "document",
		defOps:      map[string]string{"document": "extractText"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("mistralCloudApi")
			if err != nil {
				return nil, err
			}
			return bearerClient(ec.apiBase("mistral", "https://api.mistral.ai"), c, "apiKey"), nil
		},
		ops: map[string]*restOp{
			// Batch mode gives the same results; each item is sent on its own.
			"document/extractText": {run: func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
				model, _ := ec.str(i, "model", "mistral-ocr-latest")
				typ, _ := ec.str(i, "documentType", "document_url")
				doc := map[string]interface{}{"type": typ}
				if in, _ := ec.str(i, "inputType", "binary"); in == "url" {
					doc[typ], _ = ec.str(i, "url", "")
				} else {
					prop, _ := ec.str(i, "binaryProperty", "data")
					data, mt, name, err := binaryOf(it, prop)
					if err != nil {
						return nil, err
					}
					doc[typ] = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data)
					if typ == "document_url" && name != "" {
						doc["document_name"] = name
					}
				}
				v, err := c.do(ec, &restRequest{method: "POST", path: "/v1/ocr", body: map[string]interface{}{"model": model, "document": doc}})
				if err != nil {
					return nil, err
				}
				res := asMap(v)
				var texts []string
				for _, p := range asList(res["pages"]) {
					texts = append(texts, toString(asMap(p)["markdown"]))
				}
				res["extractedText"] = strings.Join(texts, "\n\n")
				res["pageCount"] = float64(len(texts))
				return []Item{{JSON: res}}, nil
			}},
		},
	})
}
