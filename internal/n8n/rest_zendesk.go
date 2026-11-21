package n8n

// Zendesk (n8n-nodes-base.zendesk), Support API v2. Paths end in ".json"
// like n8n's; query arrays use brackets (role[]=…).

func init() {
	ticketFields := "type externalId:external_id subject status recipient group:group_id tags customFieldsUi:custom_fields:fixed=customFieldsValues"
	userFields := func(coll string) string {
		return "@" + coll + " userFieldsUi:user_fields:kv=userFieldValues *"
	}
	orgFields := func(coll string) string {
		return "@" + coll + " organizationFieldsUi:organization_fields:kv=organizationFieldValues *"
	}
	registerREST("zendesk", &restService{
		defResource: "ticket",
		defOps:      map[string]string{"ticket": "create", "ticketField": "get", "user": "create", "organization": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			def := "zendeskApi"
			if ec.version >= 1.1 {
				def = "zendeskOAuth2Api"
			}
			c, t, err := ec.restCred(i, def, map[string]string{"apiToken": "zendeskApi", "oAuth2": "zendeskOAuth2Api"})
			if err != nil {
				return nil, err
			}
			restDefaultLimit(ec, 100)
			var h map[string]string
			if t == "zendeskApi" {
				h = basicAuth(credString(c, "email")+"/token", credString(c, "apiToken"))
			} else {
				h = map[string]string{"Authorization": "Bearer " + accessToken(c)}
			}
			if credString(c, "marketplaceName") != "" && credString(c, "marketplaceOrganizationId") != "" && credString(c, "marketplaceAppId") != "" {
				h["X-Zendesk-Marketplace-Name"] = credString(c, "marketplaceName")
				h["X-Zendesk-Marketplace-Organization-Id"] = credString(c, "marketplaceOrganizationId")
				h["X-Zendesk-Marketplace-App-Id"] = credString(c, "marketplaceAppId")
			}
			base := ec.apiBase("zendesk", "https://"+credString(c, "subdomain")+".zendesk.com/api/v2")
			return &restClient{base: base, headers: h}, nil
		},
		pager: nextURLPager("next_page"),
		ops: map[string]*restOp{
			"ticket/create": {method: "POST", path: "/tickets.json", wrap: "ticket", out: "ticket", body: "description:comment.body",
				build: zendeskTicketBody("additionalFields", "@additionalFields "+ticketFields)},
			"ticket/update": {method: "PUT", path: "/tickets/{id}.json", wrap: "ticket", out: "ticket",
				build: zendeskTicketBody("updateFields", "@updateFields "+ticketFields+" assigneeEmail:assignee_email internalNote:comment.html_body publicReply:comment.body")},
			"ticket/get": {path: "/tickets/{id}.json", build: zendeskSuspended("/suspended_tickets/{id}.json"),
				post: func(_ *execCtx, _ int, v interface{}) (interface{}, error) {
					m := asMap(v)
					if m["ticket"] != nil {
						return m["ticket"], nil
					}
					return m["suspended_ticket"], nil
				}},
			"ticket/getAll": {path: "/search.json", list: true, sizeParam: "per_page", build: func(ec *execCtx, i int, r *restRequest) error {
				o := fieldsOf(ec, i, "options")
				q := "type:ticket"
				if s := toString(o["query"]); s != "" {
					q += " " + s
				}
				if s := toString(o["status"]); s != "" {
					q += " status:" + s
				}
				if s := toString(o["group"]); s != "" {
					q += " group:" + s
				}
				r.query.Set("query", q)
				if s := toString(o["sortBy"]); s != "" {
					r.query.Set("sort_by", s)
				}
				if s := toString(o["sortOrder"]); s != "" {
					r.query.Set("sort_order", s)
				}
				if t, _ := ec.str(i, "ticketType", "regular"); t == "suspended" {
					r.path = "/suspended_tickets.json"
				}
				return nil
			}},
			"ticket/delete":  {method: "DELETE", path: "/tickets/{id}.json", success: true, build: zendeskSuspended("/suspended_tickets/{id}.json")},
			"ticket/recover": {method: "PUT", path: "/suspended_tickets/{id}/recover.json", out: "ticket"},

			"ticketField/get":    {path: "/ticket_fields/{ticketFieldId}.json", out: "ticket_field"},
			"ticketField/getAll": {path: "/ticket_fields.json", list: true, listKey: "ticket_fields", sizeParam: "limit"},

			"user/create":           {method: "POST", path: "/users.json", wrap: "user", out: "user", body: "name " + userFields("additionalFields")},
			"user/update":           {method: "PUT", path: "/users/{id}.json", wrap: "user", out: "user", body: userFields("updateFields")},
			"user/get":              {path: "/users/{id}.json", out: "user"},
			"user/getAll":           {path: "/users.json", list: true, listKey: "users", sizeParam: "per_page", query: "@filters role:role[] *"},
			"user/search":           {path: "/users/search.json", list: true, listKey: "users", sizeParam: "per_page", query: "@filters query external_id *"},
			"user/getOrganizations": {path: "/users/{id}/organizations.json", out: "organizations"},
			"user/getRelatedData":   {path: "/users/{id}/related.json", out: "user_related"},
			"user/delete":           {method: "DELETE", path: "/users/{id}.json", out: "user"},

			"organization/create":         {method: "POST", path: "/organizations.json", wrap: "organization", out: "organization", body: "name " + orgFields("additionalFields")},
			"organization/update":         {method: "PUT", path: "/organizations/{id}.json", wrap: "organization", out: "organization", body: orgFields("updateFields")},
			"organization/get":            {path: "/organizations/{id}.json", out: "organization"},
			"organization/getAll":         {path: "/organizations.json", list: true, listKey: "organizations", sizeParam: "per_page"},
			"organization/count":          {path: "/organizations/count.json", out: "count"},
			"organization/getRelatedData": {path: "/organizations/{id}/related.json", out: "organization_related"},
			"organization/delete":         {method: "DELETE", path: "/organizations/{id}.json", success: true},
		},
	})
}

// zendeskTicketBody fills a ticket from the field list, or from the JSON
// parameter (additionalFieldsJson / updateFieldsJson) in JSON mode.
func zendeskTicketBody(coll, fields string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if j, _ := ec.boolean(i, "jsonParameters", false); j {
			v, _ := ec.param(i, coll+"Json", "")
			x, err := restConv("json", v)
			if err != nil {
				return permanentError{ec.node.Name + ": Additional fields must be a valid JSON"}
			}
			for k, val := range asMap(x) {
				r.body[k] = val
			}
			return nil
		}
		if err := ec.restFields(i, fields, r.body); err != nil {
			return err
		}
		if c := asMap(r.body["comment"]); coll == "updateFields" && len(c) > 0 {
			// A public reply wins over an internal note, as in n8n.
			if c["body"] != nil {
				r.body["comment"] = map[string]interface{}{"body": c["body"], "public": true}
			} else {
				r.body["comment"] = map[string]interface{}{"html_body": c["html_body"], "public": false}
			}
		}
		return nil
	}
}

// zendeskSuspended switches to the suspended-ticket path when ticketType is
// "suspended".
func zendeskSuspended(path string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if t, _ := ec.str(i, "ticketType", "regular"); t != "suspended" {
			return nil
		}
		p, err := ec.restPath(i, path)
		r.path = p
		return err
	}
}

// restDefaultLimit gives "limit" the node's default when a workflow leaves
// it unset (n8n fills parameter defaults; the engine assumes 50).
func restDefaultLimit(ec *execCtx, n int) {
	if _, ok := ec.params["limit"]; ok {
		return
	}
	p := make(map[string]interface{}, len(ec.params)+1)
	for k, v := range ec.params {
		p[k] = v
	}
	p["limit"] = n
	ec.params = p
}
