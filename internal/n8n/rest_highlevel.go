package n8n

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// HighLevel (n8n-nodes-base.highLevel). Version 2 uses the LeadConnector API
// (OAuth2, Version header, locationId from the token data); version 1 the
// old rest.gohighlevel.com API with an API key.

var highLevelDefOps = map[string]string{"contact": "create", "opportunity": "create", "task": "create", "calendar": "bookAppointment"}

const (
	hlContactCreate = "email phone @additionalFields address1 city customFields:customFields:fixed=values dnd firstName lastName name postalCode source state tags timezone:timezone:rl website"
	hlContactUpdate = "@updateFields address1 city customFields:customFields:fixed=values dnd email firstName lastName name phone postalCode state tags timezone:timezone:rl website"
)

// The version specs, set in init (they refer to functions that use them).
var highLevelV1, highLevelV2 *restService

func init() {
	highLevelV2 = &restService{
		pager: highLevelPager,
		ops: map[string]*restOp{
			"contact/create": {method: "POST", path: "/contacts/upsert/", body: hlContactCreate, run: highLevelContactUpsert,
				build: hlBuild(false, func(ec *execCtx, i int, r *restRequest) error {
					r.body["locationId"] = highLevelLocation(ec)
					return nil
				})},
			"contact/update": {method: "PUT", path: "/contacts/{contactId}/", body: hlContactUpdate, out: "contact", build: hlBuild(false, nil)},
			"contact/delete": {method: "DELETE", path: "/contacts/{contactId}/", success: true},
			"contact/get":    {path: "/contacts/{contactId}/", out: "contact"},
			"contact/getAll": {path: "/contacts/", list: true, listKey: "contacts", sizeParam: "limit", maxSize: 100, query: "@filters query @options order sortBy",
				build: func(ec *execCtx, i int, r *restRequest) error {
					r.query.Set("locationId", highLevelLocation(ec))
					return nil
				}},

			"opportunity/create": {method: "POST", path: "/opportunities/", body: "=status:open pipelineId contactId name status @additionalFields assignedTo companyName monetaryValue tags stageId:pipelineStageId",
				build: hlBuild(false, func(ec *execCtx, i int, r *restRequest) error {
					r.body["locationId"] = highLevelLocation(ec)
					return nil
				})},
			"opportunity/update": {method: "PUT", path: "/opportunities/{opportunityId}", body: "@updateFields assignedTo monetaryValue name pipelineId stageId:pipelineStageId status"},
			"opportunity/delete": {method: "DELETE", path: "/opportunities/{opportunityId}", success: true},
			"opportunity/get":    {path: "/opportunities/{opportunityId}"},
			"opportunity/getAll": {path: "/opportunities/search", list: true, listKey: "opportunities", sizeParam: "limit", maxSize: 100,
				query: "@filters assignedTo:assigned_to campaignId endDate:endDate:ms pipelineId:pipeline_id stageId:pipeline_stage_id startDate:date:ms status query:q",
				build: func(ec *execCtx, i int, r *restRequest) error {
					r.query.Set("location_id", highLevelLocation(ec))
					return nil
				}},

			"task/create": {method: "POST", path: "/contacts/{contactId}/tasks/", body: "=completed:false title dueDate:dueDate:iso completed @additionalFields assignedTo body",
				build: hlDueDate, post: hlTaskContact},
			"task/update": {method: "PUT", path: "/contacts/{contactId}/tasks/{taskId}/", body: "@updateFields assignedTo completed body dueDate:dueDate:iso title",
				run: hlFill("/contacts/{contactId}/tasks/{taskId}/", map[string]string{"title": "title", "dueDate": "dueDate"}, hlTaskContact)},
			"task/delete": {method: "DELETE", path: "/contacts/{contactId}/tasks/{taskId}/", success: true},
			"task/get":    {path: "/contacts/{contactId}/tasks/{taskId}/", post: hlTaskContact},
			"task/getAll": {path: "/contacts/{contactId}/tasks/", list: true, listKey: "tasks", sizeParam: "limit", maxSize: 100, pager: hlNoPager, post: hlTaskContact},

			"calendar/bookAppointment": {method: "POST", path: "/calendars/events/appointments",
				body: "calendarId locationId contactId startTime @additionalFields endTime title appointmentStatus assignedUserId address ignoreDateRange toNotify"},
			"calendar/getFreeSlots": {path: "/calendars/{calendarId}/free-slots", query: "startDate endDate @additionalFields timezone userId userIds.userIds:userIds enableLookBusy"},
		},
	}
}

func init() {
	highLevelV1 = &restService{
		pager: highLevelPager,
		ops: map[string]*restOp{
			"contact/create": {method: "POST", path: "/contacts", body: hlContactCreate, out: "contact", build: hlBuild(true, nil)},
			"contact/update": {method: "PUT", path: "/contacts/{contactId}", body: hlContactUpdate, out: "contact", build: hlBuild(true, nil)},
			"contact/delete": {method: "DELETE", path: "/contacts/{contactId}", success: true},
			"contact/get":    {path: "/contacts/{contactId}", out: "contact"},
			"contact/getAll": {path: "/contacts", list: true, listKey: "contacts", sizeParam: "limit", maxSize: 100, query: "@filters query @options order sortBy"},
			"contact/lookup": {path: "/contacts/lookup", query: "email phone", out: "contacts"},

			"opportunity/create": {method: "POST", path: "/pipelines/{pipelineId}/opportunities", body: "=status:open stageId title status @additionalFields assignedTo companyName monetaryValue name tags",
				build: hlBuild(false, func(ec *execCtx, i int, r *restRequest) error {
					v, _ := ec.str(i, "contactIdentifier", "")
					hlContactIdentifier(r, v)
					return nil
				})},
			"opportunity/update": {method: "PUT", path: "/pipelines/{pipelineId}/opportunities/{opportunityId}",
				body: "@updateFields assignedTo companyName monetaryValue name stageId status title tags",
				build: hlBuild(false, func(ec *execCtx, i int, r *restRequest) error {
					if v, _ := ec.str(i, "updateFields.contactIdentifier", ""); v != "" {
						hlContactIdentifier(r, v)
					}
					return nil
				}),
				run: hlFill("/pipelines/{pipelineId}/opportunities/{opportunityId}", map[string]string{"status": "status", "title": "name"}, nil)},
			"opportunity/delete": {method: "DELETE", path: "/pipelines/{pipelineId}/opportunities/{opportunityId}", success: true},
			"opportunity/get":    {path: "/pipelines/{pipelineId}/opportunities/{opportunityId}"},
			"opportunity/getAll": {path: "/pipelines/{pipelineId}/opportunities", list: true, listKey: "opportunities", sizeParam: "limit", maxSize: 100,
				query: "@filters assignedTo campaignId endDate:endDate:ms stageId startDate:startDate:ms status query"},

			"task/create": {method: "POST", path: "/contacts/{contactId}/tasks", body: "title dueDate:dueDate:iso @additionalFields assignedTo description status",
				build: hlDueDate, post: hlTaskContact},
			"task/update": {method: "PUT", path: "/contacts/{contactId}/tasks/{taskId}", body: "@updateFields assignedTo description dueDate:dueDate:iso status title",
				run: hlFill("/contacts/{contactId}/tasks/{taskId}", map[string]string{"title": "title", "dueDate": "dueDate"}, hlTaskContact)},
			"task/delete": {method: "DELETE", path: "/contacts/{contactId}/tasks/{taskId}", success: true},
			"task/get":    {path: "/contacts/{contactId}/tasks/{taskId}", post: hlTaskContact},
			"task/getAll": {path: "/contacts/{contactId}/tasks", list: true, listKey: "tasks", sizeParam: "limit", maxSize: 100, pager: hlNoPager, post: hlTaskContact},
		},
	}
}

func init() {
	ops := map[string]*restOp{}
	for _, s := range []*restService{highLevelV1, highLevelV2} {
		s.aliases = map[string]string{"contact/upsert": "contact/create"}
		for k := range s.ops {
			ops[k] = &restOp{run: highLevelRun}
		}
	}
	registerREST("highLevel", &restService{
		defResource: "contact",
		defOps:      highLevelDefOps,
		aliases:     map[string]string{"contact/upsert": "contact/create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			if ec.version < 2 {
				c, err := ec.credential("highLevelApi")
				if err != nil {
					return nil, err
				}
				return &restClient{base: ec.apiBase("highLevel", "https://rest.gohighlevel.com/v1"), headers: map[string]string{"Authorization": "Bearer " + credString(c, "apiKey")}}, nil
			}
			c, err := ec.credential("highLevelOAuth2Api")
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("highLevel", "https://services.leadconnectorhq.com"),
				headers: map[string]string{"Authorization": "Bearer " + accessToken(c), "Version": "2021-07-28"}}, nil
		},
		ops: ops,
	})
}

// highLevelRun dispatches to the operation of the node's version.
func highLevelRun(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	s := highLevelV2
	if ec.version < 2 {
		s = highLevelV1
	}
	r, o := ec.resourceOpBy(i, "contact", highLevelDefOps)
	op := s.op(r, o)
	if op == nil {
		return nil, unsupportedOp(ec.node.Name, r, o)
	}
	if r != "contact" || ec.version < 2 {
		restDefaultLimit(ec, 20)
	}
	if op.run != nil {
		return op.run(ec, i, it, c)
	}
	return s.exec(ec, i, op, c)
}

// highLevelPager follows meta.startAfterId/startAfter until meta.total
// records are read.
func highLevelPager(p *restPage, res interface{}, _ http.Header) bool {
	meta := asMap(asMap(res)["meta"])
	total, _ := toFloat(meta["total"])
	if float64(p.got) >= total {
		return false
	}
	p.query.Set("startAfterId", toString(meta["startAfterId"]))
	p.query.Set("startAfter", toString(meta["startAfter"]))
	return true
}

func hlNoPager(*restPage, interface{}, http.Header) bool { return false }

func highLevelLocation(ec *execCtx) string {
	c, err := ec.credential("highLevelOAuth2Api")
	if err != nil {
		return ""
	}
	return firstNonEmpty(credString(asMap(c["oauthTokenData"]), "locationId"), credString(c, "locationId"))
}

// hlBuild splits comma-separated tags and formats custom fields (v1:
// customField.{id}: value; v2: [{id, fieldValue}]), then runs more.
func hlBuild(v1 bool, more func(ec *execCtx, i int, r *restRequest) error) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if s, ok := r.body["tags"].(string); ok {
			var tags []interface{}
			for _, t := range strings.Split(s, ",") {
				tags = append(tags, strings.TrimSpace(t))
			}
			r.body["tags"] = tags
		}
		if cf, ok := r.body["customFields"].([]interface{}); ok {
			delete(r.body, "customFields")
			if v1 {
				m := map[string]interface{}{}
				for _, x := range cf {
					f := asMap(x)
					m[rlValue(f["fieldId"])] = f["fieldValue"]
				}
				r.body["customField"] = m
			} else {
				out := make([]interface{}, 0, len(cf))
				for _, x := range cf {
					f := asMap(x)
					out = append(out, map[string]interface{}{"id": rlValue(f["fieldId"]), "fieldValue": f["fieldValue"]})
				}
				r.body["customFields"] = out
			}
		}
		if more != nil {
			return more(ec, i, r)
		}
		return nil
	}
}

func hlDueDate(ec *execCtx, i int, r *restRequest) error {
	if r.body["dueDate"] == nil {
		return permanentError{ec.node.Name + ": dueDate is required"}
	}
	return nil
}

// hlTaskContact adds the contactId parameter to task results.
func hlTaskContact(ec *execCtx, i int, v interface{}) (interface{}, error) {
	id, _ := ec.str(i, "contactId", "")
	add := func(x interface{}) {
		if m, ok := x.(map[string]interface{}); ok {
			m["contactId"] = id
		}
	}
	if l, ok := v.([]interface{}); ok {
		for _, x := range l {
			add(x)
		}
	} else {
		add(v)
	}
	return v, nil
}

var (
	hlEmailRe = regexp.MustCompile(`^[^\s@<>()\[\],;:"]+@([A-Za-z0-9-]+\.)+[A-Za-z]{2,}$`)
	hlPhoneRe = regexp.MustCompile(`^\+?[0-9][0-9 ()\-]{6,}$`)
)

// hlContactIdentifier sends an email, phone or contact id, whichever the
// identifier looks like.
func hlContactIdentifier(r *restRequest, v string) {
	switch {
	case hlEmailRe.MatchString(strings.ToLower(v)):
		r.body["email"] = v
	case hlPhoneRe.MatchString(v):
		r.body["phone"] = v
	default:
		r.body["contactId"] = v
	}
}

// hlFill runs an update after filling required fields the API wants (body
// field → field of the current record) from a GET of the record.
func hlFill(getPath string, fill map[string]string, post func(ec *execCtx, i int, v interface{}) (interface{}, error)) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		s := highLevelV2
		if ec.version < 2 {
			s = highLevelV1
		}
		r, o := ec.resourceOpBy(i, "contact", highLevelDefOps)
		op := s.op(r, o)
		req, err := op.request(ec, i)
		if err != nil {
			return nil, err
		}
		missing := false
		for k := range fill {
			if restEmpty(req.body[k]) {
				missing = true
			}
		}
		if missing {
			p, err := ec.restPath(i, getPath)
			if err != nil {
				return nil, err
			}
			cur, err := c.do(ec, &restRequest{method: "GET", path: p})
			if err != nil {
				return nil, err
			}
			m := asMap(cur)
			for k, src := range fill {
				if restEmpty(req.body[k]) {
					v := m[src]
					if k == "dueDate" {
						if t, ok := timeOf(v); ok {
							v = t.UTC().Format("2006-01-02T15:04:05Z07:00")
						}
					}
					req.body[k] = v
				}
			}
		}
		v, err := c.do(ec, req)
		if err != nil {
			return nil, err
		}
		if post != nil {
			if v, err = post(ec, i, v); err != nil {
				return nil, err
			}
		}
		return restItems(v), nil
	}
}

// highLevelContactUpsert upserts a contact and adds additionalFields.notes
// as a note on it.
func highLevelContactUpsert(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	req, err := highLevelV2.ops["contact/create"].request(ec, i)
	if err != nil {
		return nil, err
	}
	v, err := c.do(ec, req)
	if err != nil {
		return nil, err
	}
	contact := asMap(asMap(v)["contact"])
	if note, _ := ec.str(i, "additionalFields.notes", ""); note != "" {
		id := toString(contact["id"])
		if id == "" {
			return nil, permanentError{ec.node.Name + ": no contact id to add the note to"}
		}
		n := &restRequest{method: "POST", path: "/contacts/" + url.PathEscape(id) + "/notes", body: map[string]interface{}{"userId": contact["locationId"], "body": note}}
		if _, err := c.do(ec, n); err != nil {
			return nil, err
		}
	}
	return restItems(asMap(v)["contact"]), nil
}
