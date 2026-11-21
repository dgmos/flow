package n8n

import (
	"encoding/base64"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
)

// Brevo, formerly Sendinblue (n8n-nodes-base.sendInBlue), versions 1 and
// 1.1: contacts, contact attributes, senders and transactional email at
// https://api.brevo.com/v3. Version 1 spells recipients "receipients".

func init() {
	brevoSuccess := func(*execCtx, int, interface{}) (interface{}, error) {
		return map[string]interface{}{"success": true}, nil
	}
	registerREST("sendInBlue", &restService{
		defResource: "email",
		defOps:      map[string]string{"email": "send", "contact": "create", "attribute": "create", "sender": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("sendInBlueApi")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("sendInBlue", "https://api.brevo.com"), headers: map[string]string{"api-key": credString(c, "apiKey")}}, nil
		},
		ops: map[string]*restOp{
			"email/send":         {method: "POST", path: "/v3/smtp/email", build: brevoEmail(false)},
			"email/sendTemplate": {method: "POST", path: "/v3/smtp/email", build: brevoEmail(true)},

			"contact/create": {method: "POST", path: "/v3/contacts", body: "email", build: brevoAttributes("createContactAttributes.attributesValues", false)},
			"contact/upsert": {method: "POST", path: "/v3/contacts", body: "email", post: brevoSuccess,
				build: brevoAttributes("upsertAttributes.upsertAttributesValues", true)},
			"contact/update": {method: "PUT", path: "/v3/contacts/{identifier}", post: brevoSuccess,
				build: brevoAttributes("updateAttributes.updateAttributesValues", false)},
			"contact/delete": {method: "DELETE", path: "/v3/contacts/{identifier}", success: true},
			"contact/get":    {path: "/v3/contacts/{identifier}"},
			"contact/getAll": {path: "/v3/contacts", list: true, listKey: "contacts", sizeParam: "limit", maxSize: 1000,
				query: "@options sort @filters modifiedSince", pager: brevoOffset(1000)},

			"attribute/create": {method: "POST", path: "/v3/contacts/attributes/{attributeCategory}/{attributeName}", post: brevoSuccess,
				build: brevoAttribute("attributeCategory", "attributeValue", "attributeCategoryList.categoryEnumeration.attributesValues")},
			"attribute/update": {method: "PUT", path: "/v3/contacts/attributes/{updateAttributeCategory}/{updateAttributeName}",
				build: brevoAttribute("updateAttributeCategory", "updateAttributeValue", "updateAttributeCategoryList.updateCategoryEnumeration.updateAttributesValues")},
			"attribute/delete": {method: "DELETE", path: "/v3/contacts/attributes/{deleteAttributeCategory}/{deleteAttributeName}", success: true},
			"attribute/getAll": {path: "/v3/contacts/attributes", list: true, listKey: "attributes"},

			"sender/create": {method: "POST", path: "/v3/senders", body: "name email"},
			"sender/delete": {method: "DELETE", path: "/v3/senders/{id}", success: true},
			"sender/getAll": {path: "/v3/senders", list: true, listKey: "senders", build: func(ec *execCtx, _ int, _ *restRequest) error {
				restDefaultLimit(ec, 10)
				return nil
			}},
		},
	})
}

// brevoOffset pages with limit/offset until a short page.
func brevoOffset(size int) restPager {
	return func(p *restPage, _ interface{}, _ http.Header) bool {
		if len(p.last) < size {
			return false
		}
		p.query.Set("limit", strconv.Itoa(size))
		p.query.Set("offset", strconv.Itoa(p.got))
		return true
	}
}

// brevoAttributes sets attributes.<fieldName> from a fieldName/fieldValue list.
func brevoAttributes(path string, upsert bool) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		v, err := ec.param(i, path, nil)
		if err != nil {
			return err
		}
		attrs := map[string]interface{}{}
		for _, x := range asListOrOne(v) {
			m := asMap(x)
			if k := toString(m["fieldName"]); k != "" {
				attrs[k] = m["fieldValue"]
			}
		}
		if len(attrs) > 0 {
			r.body["attributes"] = attrs
		}
		if upsert {
			r.body["updateEnabled"] = true
		}
		return nil
	}
}

func brevoAttribute(catParam, valueParam, enumPath string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		cat, _ := ec.str(i, catParam, "")
		switch cat {
		case "normal":
			if t, _ := ec.str(i, "attributeType", ""); t != "" {
				r.body["type"] = t
			} else {
				r.body["type"] = "boolean"
			}
		case "category":
			r.body["type"] = "category"
		case "transactional":
			r.body["type"] = "id"
		}
		if cat == "global" || cat == "calculated" {
			if v, err := ec.str(i, valueParam, ""); err != nil {
				return err
			} else if v != "" {
				r.body["value"] = v
			}
		}
		if cat == "category" {
			v, err := ec.param(i, enumPath, nil)
			if err != nil {
				return err
			}
			var enum []interface{}
			for _, x := range asListOrOne(v) {
				m := asMap(x)
				val := m["attributeCategoryValue"]
				if val == nil {
					val = 1.0
				}
				enum = append(enum, map[string]interface{}{"value": val, "label": toString(m["attributeCategoryLabel"])})
			}
			if len(enum) > 0 {
				r.body["enumeration"] = enum
			}
		}
		return nil
	}
}

// brevoEmail builds a transactional email (free content or a template).
func brevoEmail(template bool) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		a := &cmtyArgs{ec: ec, i: i}
		legacy := ec.version < 1.1
		rcpt := "recipients"
		if legacy {
			rcpt = "receipients"
		}
		if template {
			id := a.get("templateId", "")
			if f, ok := toFloat(id); ok {
				id = int64(f)
			}
			r.body["templateId"] = id
		} else {
			r.body["sender"] = brevoFirst(brevoAddresses(a.str("sender", "")))
			r.body["subject"] = a.str("subject", "")
			if a.bool("sendHTML", false) {
				r.body["htmlContent"] = a.str("htmlContent", "")
			} else {
				r.body["textContent"] = a.str("textContent", "")
			}
		}
		r.body["to"] = brevoAddresses(a.str(rcpt, ""))
		af := "additionalFields."
		if !template {
			cc, bcc := af+"recipientsCC.recipientCc.cc", af+"recipientsBCC.recipientBcc.bcc"
			if legacy {
				cc, bcc = af+"receipientsCC.receipientCc.cc", af+"receipientsBCC.receipientBcc.bcc"
			}
			if a.has(strings.TrimSuffix(cc, ".cc")) {
				r.body["cc"] = brevoAddresses(a.str(cc, ""))
			}
			if a.has(strings.TrimSuffix(bcc, ".bcc")) {
				r.body["bcc"] = brevoAddresses(a.str(bcc, ""))
			}
		}
		if a.has(af + "emailTags.tags") {
			tags := []interface{}{}
			for _, t := range strings.Split(a.str(af+"emailTags.tags.tag", ""), ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
			r.body["tags"] = tags
		}
		if template && a.has(af+"templateParameters.parameterValues") {
			params := map[string]interface{}{}
			for _, p := range strings.Split(a.str(af+"templateParameters.parameterValues.parameters", ""), ",") {
				if kv := strings.Split(p, "="); len(kv) == 2 {
					params[kv[0]] = kv[1]
				}
			}
			r.body["params"] = params
		}
		if a.has(af + "emailAttachments.attachment") {
			var it Item
			if items := ec.items(); i < len(items) {
				it = items[i]
			}
			var att []interface{}
			for _, prop := range strings.Split(a.str(af+"emailAttachments.attachment.binaryPropertyName", ""), ",") {
				prop = strings.TrimSpace(prop)
				data, mt, _, err := binaryOf(it, prop)
				if err != nil {
					return permanentError{ec.node.Name + ": " + err.Error()}
				}
				b := asMap(it.Binary[prop])
				ext := toString(b["fileExtension"])
				if ext == "" {
					_, ext, _ = strings.Cut(mt, "/")
				}
				name := toString(b["fileName"])
				if name == "" {
					name = "file-" + strconv.Itoa(i) + "." + ext
				} else if !strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(ext)) {
					name += "." + ext
				}
				att = append(att, map[string]interface{}{"content": base64.StdEncoding.EncodeToString(data), "name": name})
			}
			r.body["attachment"] = att
		}
		return a.err
	}
}

func brevoFirst(l []interface{}) interface{} {
	if len(l) == 0 {
		return nil
	}
	return l[0]
}

// brevoAddresses parses "Name <a@b.c>, d@e.f" into [{email, name}].
func brevoAddresses(s string) []interface{} {
	out := []interface{}{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	if list, err := mail.ParseAddressList(s); err == nil {
		for _, a := range list {
			m := map[string]interface{}{"email": a.Address}
			if a.Name != "" {
				m["name"] = a.Name
			}
			out = append(out, m)
		}
		return out
	}
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		m := map[string]interface{}{"email": p}
		if lt := strings.LastIndex(p, "<"); lt >= 0 && strings.HasSuffix(p, ">") {
			m["email"] = strings.TrimSpace(p[lt+1 : len(p)-1])
			if n := strings.Trim(strings.TrimSpace(p[:lt]), `"`); n != "" {
				m["name"] = n
			}
		}
		out = append(out, m)
	}
	return out
}
