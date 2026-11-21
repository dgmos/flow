package n8n

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// HubSpot (n8n-nodes-base.hubspot, versions 1–2.x), on the same legacy and
// CRM v3 endpoints n8n uses so outputs keep their shape.

type hubspotAPI struct {
	ec      *execCtx
	base    string
	headers map[string]string
	query   url.Values
}

func (ec *execCtx) hubspot() (*hubspotAPI, error) {
	auth, _ := ec.str(0, "authentication", "apiKey")
	credType := map[string]string{"apiKey": "hubspotApi", "appToken": "hubspotAppToken", "oAuth2": "hubspotOAuth2Api", "developerApi": "hubspotDeveloperApi"}[auth]
	if credType == "" {
		credType = "hubspotApi"
	}
	c, err := ec.credential(credType)
	if err != nil {
		return nil, err
	}
	h := &hubspotAPI{ec: ec, base: ec.apiBase("hubspot", "https://api.hubapi.com"), headers: map[string]string{}, query: url.Values{}}
	switch {
	case credType == "hubspotAppToken" || credString(c, "appToken") != "":
		h.headers["Authorization"] = "Bearer " + credString(c, "appToken", "accessToken")
	case credType == "hubspotApi":
		// Legacy API keys go in the query; private-app tokens ("pat-…") as bearer.
		if k := credString(c, "apiKey"); strings.HasPrefix(k, "pat-") {
			h.headers["Authorization"] = "Bearer " + k
		} else {
			h.query.Set("hapikey", k)
		}
	default:
		t := accessToken(c)
		if t == "" {
			return nil, permanentError{credType + " credential has no access token"}
		}
		h.headers["Authorization"] = "Bearer " + t
	}
	return h, nil
}

func (h *hubspotAPI) call(method, path string, qs url.Values, body interface{}) (map[string]interface{}, []byte, error) {
	q := url.Values{}
	for k, v := range h.query {
		q[k] = v
	}
	for k, v := range qs {
		q[k] = v
	}
	u := h.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return h.ec.apiCall(method, u, h.headers, body, "")
}

// all pages through a legacy or v3 list endpoint, collecting key.
func (h *hubspotAPI) all(method, path, key string, qs url.Values, body map[string]interface{}, limit int) ([]interface{}, error) {
	if qs == nil {
		qs = url.Values{}
	}
	var out []interface{}
	for page := 0; page < 1000; page++ {
		var b interface{}
		if body != nil {
			b = body
		}
		res, _, err := h.call(method, path, qs, b)
		if err != nil {
			return nil, err
		}
		out = append(out, asList(res[key])...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		more := toBool(res["hasMore"]) || toBool(res["has-more"])
		if off := res["offset"]; off != nil && more {
			qs.Set("offset", toString(off))
		}
		if vo := res["vid-offset"]; vo != nil && more {
			qs.Set("vidOffset", toString(vo))
		}
		if next := asMap(asMap(res["paging"])["next"]); next != nil && body != nil {
			body["after"] = next["after"]
			more = true
		}
		if !more {
			break
		}
	}
	return out, nil
}

// hubspotProps builds the property list of a write operation from n8n's
// named fields and custom properties.
func hubspotProps(fields map[string]interface{}, mapping map[string]hubspotField, nameKey string) []interface{} {
	var props []interface{}
	add := func(prop string, v interface{}) {
		props = append(props, map[string]interface{}{nameKey: prop, "value": v})
	}
	for k, f := range mapping {
		v, ok := fields[k]
		if !ok || v == nil || v == "" || v == false {
			continue
		}
		if f.date {
			if t, ok := timeOf(v); ok {
				v = t.UnixMilli()
			}
		} else if n, ok := v.(float64); ok && f.prop == "annualrevenue" {
			v = strconv.FormatFloat(n, 'f', -1, 64)
		}
		add(f.prop, v)
	}
	for _, raw := range asList(asMap(fields["customPropertiesUi"])["customPropertiesValues"]) {
		m := asMap(raw)
		add(toString(m["property"]), m["value"])
	}
	return props
}

// hubspotPropertiesQuery adds a propertiesCollection or properties list.
func hubspotPropertiesQuery(qs url.Values, fields map[string]interface{}, key string) {
	if pc := asMap(asMap(fields["propertiesCollection"])["propertiesValues"]); pc != nil {
		for _, p := range listOrCSV(pc["properties"]) {
			qs.Add(key, p)
		}
		if m := toString(pc["propertyMode"]); m != "" {
			qs.Set("propertyMode", snakeCase(m))
		}
	}
	for _, p := range listOrCSV(fields["properties"]) {
		qs.Add(key, p)
	}
	for _, p := range listOrCSV(fields["propertiesWithHistory"]) {
		qs.Add("propertiesWithHistory", p)
	}
}

func listOrCSV(v interface{}) []string {
	switch t := v.(type) {
	case []interface{}:
		var out []string
		for _, x := range t {
			out = append(out, toString(x))
		}
		return out
	case string:
		return splitList(t)
	}
	return nil
}

func snakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hubspotNode(ec *execCtx) ([][]Item, error) {
	h, err := ec.hubspot()
	if err != nil {
		return nil, err
	}
	resource, _ := ec.str(0, "resource", "contact")
	if resource == "contactList" {
		op, _ := ec.str(0, "operation", "add")
		listID, _ := ec.str(0, "listId", "")
		body := map[string]interface{}{"vids": []interface{}{}}
		by, _ := ec.str(0, "by", "email")
		if op == "add" {
			body["emails"] = []interface{}{}
		}
		for i := range ec.items() {
			if op == "add" && by != "id" {
				e, _ := ec.str(i, "email", "")
				body["emails"] = append(body["emails"].([]interface{}), e)
			} else {
				id, _ := ec.str(i, "id", "")
				n, _ := strconv.Atoi(id)
				body["vids"] = append(body["vids"].([]interface{}), n)
			}
		}
		res, _, err := h.call("POST", "/contacts/v1/lists/"+url.PathEscape(listID)+"/"+op, nil, body)
		if err != nil {
			return nil, err
		}
		return [][]Item{{{JSON: res}}}, nil
	}
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		_, op := ec.resourceOpBy(i, "contact", map[string]string{"contact": "upsert", "company": "create", "deal": "create", "engagement": "create", "ticket": "create", "form": "getFields"})
		res, err := h.run(i, resource, op)
		if err != nil {
			return nil, err
		}
		switch r := res.(type) {
		case []interface{}:
			return listItems(r), nil
		case map[string]interface{}:
			return one(r), nil
		}
		return nil, nil
	})
}

func (h *hubspotAPI) id(i int, param string) string {
	v, _ := h.ec.param(i, param, "")
	return url.PathEscape(rlValue(v))
}

func (h *hubspotAPI) limit(i int) int {
	if all, _ := h.ec.boolean(0, "returnAll", false); all {
		return 0
	}
	n, _ := h.ec.num(0, "limit", 100)
	return int(n)
}

func (h *hubspotAPI) run(i int, resource, op string) (interface{}, error) {
	ec := h.ec
	fields := func(name string) map[string]interface{} { return fieldsOf(ec, i, name) }
	get := func(path string, qs url.Values) (interface{}, error) {
		res, _, err := h.call("GET", path, qs, nil)
		return res, err
	}
	list := func(method, path, key, limitKey string, qs url.Values, body map[string]interface{}) (interface{}, error) {
		if qs == nil {
			qs = url.Values{}
		}
		lim := h.limit(i)
		if lim > 0 {
			if body != nil {
				body["limit"] = lim
			} else {
				qs.Set(limitKey, strconv.Itoa(lim))
			}
			var b interface{}
			if body != nil {
				b = body
			}
			res, _, err := h.call(method, path, qs, b)
			if err != nil {
				return nil, err
			}
			l := asList(res[key])
			if len(l) > lim {
				l = l[:lim]
			}
			return l, nil
		}
		if body != nil {
			body["limit"] = 100
		} else {
			qs.Set("count", "100")
			qs.Set("limit", "250")
		}
		return h.all(method, path, key, qs, body, 0)
	}
	del := func(path, id string) (interface{}, error) {
		res, _, err := h.call("DELETE", path, nil, nil)
		if err != nil {
			return nil, err
		}
		if len(res) == 0 {
			return map[string]interface{}{"vid": id, "deleted": true}, nil
		}
		return res, nil
	}
	search := func(object string) (interface{}, error) {
		af := fields("additionalFields")
		sortBy := firstNonEmpty(toString(af["sortBy"]), "createdate")
		dir := firstNonEmpty(toString(af["direction"]), "DESCENDING")
		body := map[string]interface{}{"sorts": []interface{}{map[string]interface{}{"propertyName": sortBy, "direction": dir}}}
		var groups []interface{}
		for _, g := range asList(fields("filterGroupsUi")["filterGroupsValues"]) {
			var filters []interface{}
			for _, f := range asList(asMap(asMap(g)["filtersUi"])["filterValues"]) {
				fm := cloneJSON(asMap(f))
				delete(fm, "type")
				fm["propertyName"] = strings.Split(toString(fm["propertyName"]), "|")[0]
				filters = append(filters, fm)
			}
			if len(filters) > 0 {
				groups = append(groups, map[string]interface{}{"filters": filters})
			}
		}
		if len(groups) > 3 {
			return nil, permanentError{"HubSpot: you can only have 3 filter groups"}
		}
		if groups != nil {
			body["filterGroups"] = groups
		}
		for k, v := range af {
			if k == "properties" {
				body[k] = listOrCSV(v)
				continue
			}
			body[k] = v
		}
		return list("POST", "/crm/v3/objects/"+object+"/search", "results", "", nil, body)
	}

	switch resource + "." + op {
	// ---------------------------------------------------------------- contact
	case "contact.upsert":
		email, err := ec.str(i, "email", "")
		if err != nil {
			return nil, err
		}
		af := fields("additionalFields")
		res, _, err := h.call("POST", "/contacts/v1/contact/createOrUpdate/email/"+url.PathEscape(email), nil,
			map[string]interface{}{"properties": hubspotProps(af, hubspotFields["contact"], "property")})
		if err != nil {
			return nil, err
		}
		vid := toString(res["vid"])
		if cid := af["associatedCompanyId"]; cid != nil && cid != "" {
			if _, _, err := h.call("PUT", "/crm-associations/v1/associations/create-batch", nil, []interface{}{map[string]interface{}{
				"fromObjectId": res["vid"], "toObjectId": cid, "category": "HUBSPOT_DEFINED", "definitionId": 1}}); err != nil {
				return nil, err
			}
		}
		if resolve, _ := ec.boolean(i, "options.resolveData", true); resolve {
			isNew := res["isNew"]
			qs := url.Values{}
			for _, p := range listOrCSV(af["properties"]) {
				qs.Add("property", p)
			}
			prof, _, err := h.call("GET", "/contacts/v1/contact/vid/"+url.PathEscape(vid)+"/profile", qs, nil)
			if err != nil {
				return nil, err
			}
			prof["isNew"] = isNew
			return prof, nil
		}
		return res, nil
	case "contact.get":
		af := fields("additionalFields")
		qs := url.Values{}
		if m := toString(af["formSubmissionMode"]); m != "" {
			qs.Set("formSubmissionMode", m)
		}
		if toBool(af["listMemberships"]) {
			qs.Set("showListMemberships", "true")
		}
		hubspotPropertiesQuery(qs, af, "properties")
		return get("/contacts/v1/contact/vid/"+h.id(i, "contactId")+"/profile", qs)
	case "contact.getAll", "contact.getRecentlyCreatedUpdated":
		af := fields("additionalFields")
		qs := url.Values{}
		if m := toString(af["formSubmissionMode"]); m != "" {
			qs.Set("formSubmissionMode", m)
		}
		if toBool(af["listMemberships"]) {
			qs.Set("showListMemberships", "true")
		}
		path, key := "/contacts/v1/lists/all/contacts/all", "property"
		if op == "getRecentlyCreatedUpdated" {
			path, key = "/contacts/v1/lists/recently_updated/contacts/recent", "properties"
		}
		hubspotPropertiesQuery(qs, af, key)
		return list("GET", path, "contacts", "count", qs, nil)
	case "contact.delete":
		id := h.id(i, "contactId")
		return del("/contacts/v1/contact/vid/"+id, id)
	case "contact.search":
		return search("contacts")

	// ---------------------------------------------------------------- company
	case "company.create":
		name, err := ec.str(i, "name", "")
		if err != nil {
			return nil, err
		}
		props := append([]interface{}{map[string]interface{}{"name": "name", "value": name}}, hubspotProps(fields("additionalFields"), hubspotFields["companyCreate"], "name")...)
		res, _, err := h.call("POST", "/companies/v2/companies", nil, map[string]interface{}{"properties": props})
		return res, err
	case "company.update":
		res, _, err := h.call("PUT", "/companies/v2/companies/"+h.id(i, "companyId"), nil,
			map[string]interface{}{"properties": hubspotProps(fields("updateFields"), hubspotFields["companyUpdate"], "name")})
		return res, err
	case "company.get":
		qs := url.Values{}
		if toBool(fields("additionalFields")["includeMergeAudits"]) {
			qs.Set("includeMergeAudits", "true")
		}
		return get("/companies/v2/companies/"+h.id(i, "companyId"), qs)
	case "company.getAll":
		o := fields("options")
		qs := url.Values{}
		if m := toString(o["formSubmissionMode"]); m != "" {
			qs.Set("formSubmissionMode", m)
		}
		hubspotPropertiesQuery(qs, o, "properties")
		return list("GET", "/companies/v2/companies/paged", "companies", "limit", qs, nil)
	case "company.getRecentlyCreatedUpdated", "company.getRecentlyModified", "company.getRecentlyCreated":
		af := fields("additionalFields")
		qs := url.Values{}
		if t, ok := timeOf(af["since"]); ok {
			qs.Set("since", strconv.FormatInt(t.UnixMilli(), 10))
		}
		hubspotPropertiesQuery(qs, af, "properties")
		path := "/companies/v2/companies/recent/modified"
		if op == "getRecentlyCreated" {
			path = "/companies/v2/companies/recent/created"
		}
		return list("GET", path, "results", "count", qs, nil)
	case "company.searchByDomain":
		domain, err := ec.str(i, "domain", "")
		if err != nil {
			return nil, err
		}
		domain = strings.TrimPrefix(strings.TrimPrefix(domain, "https://"), "http://")
		body := map[string]interface{}{"requestOptions": map[string]interface{}{}}
		if p := listOrCSV(fields("options")["properties"]); len(p) > 0 {
			body["requestOptions"] = map[string]interface{}{"properties": p}
		}
		return list("POST", "/companies/v2/domains/"+url.PathEscape(domain)+"/companies", "results", "", nil, body)
	case "company.delete":
		id := h.id(i, "companyId")
		return del("/crm/v3/objects/companies/"+id, id)
	case "company.search":
		return search("companies")

	// ---------------------------------------------------------------- deal
	case "deal.create":
		af := fields("additionalFields")
		var props []interface{}
		if stage, _ := ec.str(i, "stage", ""); stage != "" {
			props = append(props, map[string]interface{}{"name": "dealstage", "value": stage})
		}
		props = append(props, hubspotProps(af, hubspotFields["dealCreate"], "name")...)
		if o := af["dealOwner"]; o != nil && o != "" {
			props = append(props, map[string]interface{}{"name": "hubspot_owner_id", "value": rlValue(o)})
		}
		assoc := map[string]interface{}{}
		if v := af["associatedCompany"]; v != nil {
			assoc["associatedCompanyIds"] = v
		}
		if v := af["associatedVids"]; v != nil {
			assoc["associatedVids"] = v
		}
		res, _, err := h.call("POST", "/deals/v1/deal", nil, map[string]interface{}{"properties": props, "associations": assoc})
		return res, err
	case "deal.update":
		uf := fields("updateFields")
		props := hubspotProps(uf, hubspotFields["dealUpdate"], "name")
		if o := uf["dealOwner"]; o != nil && o != "" {
			props = append(props, map[string]interface{}{"name": "hubspot_owner_id", "value": rlValue(o)})
		}
		res, _, err := h.call("PUT", "/deals/v1/deal/"+h.id(i, "dealId"), nil, map[string]interface{}{"properties": props})
		return res, err
	case "deal.get":
		qs := url.Values{}
		f := fields("filters")
		if toBool(f["includePropertyVersions"]) {
			qs.Set("includePropertyVersions", "true")
		}
		return get("/deals/v1/deal/"+h.id(i, "dealId"), qs)
	case "deal.getAll":
		f := fields("filters")
		qs := url.Values{}
		if toBool(f["includeAssociations"]) {
			qs.Set("includeAssociations", "true")
		}
		hubspotPropertiesQuery(qs, f, "properties")
		return list("GET", "/deals/v1/deal/paged", "deals", "limit", qs, nil)
	case "deal.getRecentlyCreatedUpdated", "deal.getRecentlyCreated", "deal.getRecentlyModified":
		f := fields("filters")
		qs := url.Values{}
		if t, ok := timeOf(f["since"]); ok {
			qs.Set("since", strconv.FormatInt(t.UnixMilli(), 10))
		}
		if toBool(f["includePropertyVersions"]) {
			qs.Set("includePropertyVersions", "true")
		}
		path := "/deals/v1/deal/recent/created"
		if op == "getRecentlyModified" {
			path = "/deals/v1/deal/recent/modified"
		}
		return list("GET", path, "results", "count", qs, nil)
	case "deal.delete":
		id := h.id(i, "dealId")
		return del("/deals/v1/deal/"+id, id)
	case "deal.search":
		return search("deals")

	// ---------------------------------------------------------------- engagement
	case "engagement.create":
		typ, _ := ec.str(i, "type", "")
		meta := fields("metadata")
		if len(meta) == 0 {
			return nil, permanentError{"HubSpot: at least one metadata field needs to be set"}
		}
		assoc := asMap(fields("additionalFields")["associations"])
		eng := map[string]interface{}{"type": strings.ToUpper(typ)}
		if o, ok := assoc["ownerId"].(float64); ok {
			eng["ownerId"] = o
		}
		pick := func(keys ...string) map[string]interface{} {
			out := map[string]interface{}{}
			for _, k := range keys {
				if v := meta[k]; v != nil && v != "" {
					out[k] = v
				}
			}
			return out
		}
		var md map[string]interface{}
		switch typ {
		case "email":
			md = pick("subject", "html", "text")
			from := map[string]interface{}{}
			for k, to := range map[string]string{"fromEmail": "email", "firstName": "firstName", "lastName": "lastName"} {
				if v := meta[k]; v != nil && v != "" {
					from[to] = v
				}
			}
			md["from"] = from
			for _, k := range []string{"cc", "bcc"} {
				var l []interface{}
				for _, e := range listOrCSV(meta[k]) {
					l = append(l, map[string]interface{}{"email": e})
				}
				md[k] = l
			}
		case "task":
			md = pick("body", "subject", "status", "forObjectType")
			if d, _ := ec.param(i, "dueDate", nil); d != nil && d != "" {
				if t, ok := timeOf(d); ok {
					eng["timestamp"] = t.UnixMilli()
				}
			}
		case "meeting":
			md = pick("body", "title", "internalMeetingNotes")
			for _, k := range []string{"startTime", "endTime"} {
				if t, ok := timeOf(meta[k]); ok {
					md[k] = t.Unix()
				}
			}
		case "call":
			md = pick("toNumber", "fromNumber", "status", "durationMilliseconds", "recordingUrl", "body")
		default:
			md = meta
		}
		as := map[string]interface{}{}
		for _, k := range []string{"companyIds", "contactIds", "dealIds", "ownerIds", "ticketIds"} {
			if v := assoc[k]; v != nil && v != "" {
				as[k] = listOrCSV(toString(v))
			}
		}
		res, _, err := h.call("POST", "/engagements/v1/engagements", nil, map[string]interface{}{"engagement": eng, "metadata": md, "associations": as})
		return res, err
	case "engagement.get":
		return get("/engagements/v1/engagements/"+h.id(i, "engagementId"), nil)
	case "engagement.getAll":
		return list("GET", "/engagements/v1/engagements/paged", "results", "limit", nil, nil)
	case "engagement.delete":
		id := h.id(i, "engagementId")
		return del("/engagements/v1/engagements/"+id, id)

	// ---------------------------------------------------------------- ticket
	case "ticket.create", "ticket.update":
		var props []interface{}
		path, method := "/crm-objects/v1/objects/tickets", "POST"
		var af map[string]interface{}
		if op == "create" {
			for param, prop := range map[string]string{"pipelineId": "hs_pipeline", "stageId": "hs_pipeline_stage", "ticketName": "subject"} {
				v, _ := ec.str(i, param, "")
				props = append(props, map[string]interface{}{"name": prop, "value": v})
			}
			af = fields("additionalFields")
			props = append(props, hubspotProps(af, hubspotFields["ticketCreate"], "name")...)
		} else {
			af = fields("updateFields")
			props = hubspotProps(af, hubspotFields["ticketUpdate"], "name")
			path, method = path+"/"+h.id(i, "ticketId"), "PUT"
		}
		res, _, err := h.call(method, path, nil, props)
		if err != nil {
			return nil, err
		}
		for key, def := range map[string]float64{"associatedCompanyIds": 26, "associatedContactIds": 16} {
			var batch []interface{}
			for _, id := range asList(af[key]) {
				batch = append(batch, map[string]interface{}{"fromObjectId": res["objectId"], "toObjectId": id, "category": "HUBSPOT_DEFINED", "definitionId": def})
			}
			if len(batch) > 0 {
				if _, _, err := h.call("PUT", "/crm-associations/v1/associations/create-batch", nil, batch); err != nil {
					return nil, err
				}
			}
		}
		return res, nil
	case "ticket.get":
		af := fields("additionalFields")
		qs := url.Values{}
		hubspotPropertiesQuery(qs, af, "properties")
		if toBool(af["includeDeleted"]) {
			qs.Set("includeDeleted", "true")
		}
		return get("/crm-objects/v1/objects/tickets/"+h.id(i, "ticketId"), qs)
	case "ticket.getAll":
		qs := url.Values{}
		hubspotPropertiesQuery(qs, fields("additionalFields"), "properties")
		return h.all("GET", "/crm-objects/v1/objects/tickets/paged", "objects", qs, nil, h.limit(i))
	case "ticket.delete":
		id := h.id(i, "ticketId")
		if _, _, err := h.call("DELETE", "/crm-objects/v1/objects/tickets/"+id, nil, nil); err != nil {
			return nil, err
		}
		return map[string]interface{}{"success": true}, nil

	// ---------------------------------------------------------------- owner
	case "owner.getAll":
		_, raw, err := h.call("GET", "/owners/v2/owners", nil, nil)
		if err != nil {
			return nil, err
		}
		var l []interface{}
		if err := json.Unmarshal(raw, &l); err != nil {
			return nil, permanentError{"HubSpot: unexpected owners response"}
		}
		return l, nil
	}
	return nil, unsupportedOp("HubSpot", resource, op)
}
