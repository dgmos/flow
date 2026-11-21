package n8n

import (
	"strconv"
	"strings"
)

// ClickUp (n8n-nodes-base.clickUp), API v2.

func init() {
	registerREST("clickUp", &restService{
		defResource: "task",
		defOps:      map[string]string{"task": "create", "list": "create", "folder": "create", "comment": "create", "checklist": "create", "checklistItem": "create", "taskTag": "add", "taskList": "add", "taskDependency": "create", "timeEntry": "create", "spaceTag": "create", "goal": "create", "goalKeyResult": "create", "guest": "create", "timeEntryTag": "add"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, t, err := ec.restCred(i, "clickUpApi", map[string]string{"accessToken": "clickUpApi", "oAuth2": "clickUpOAuth2Api"})
			if err != nil {
				return nil, err
			}
			tok := accessToken(c)
			if t == "clickUpOAuth2Api" {
				tok = "Bearer " + tok
			}
			return &restClient{base: ec.apiBase("clickUp", "https://api.clickup.com/api/v2"), headers: map[string]string{"Authorization": tok}}, nil
		},
		ops: map[string]*restOp{
			"task/create": {method: "POST", path: "/list/{list}/task", body: "name @additionalFields customFieldsJson:custom_fields:json content assignees:assignees:csvint tags:tags:csv status priority:priority:int " +
				"dueDate:due_date:ms dueDateTime:due_date_time startDate:start_date:ms startDateTime:start_date_time notifyAll:notify_all parentId:parent timeEstimate:time_estimate:num",
				build: clickUpTaskBody("additionalFields")},
			"task/update": {method: "PUT", path: "/task/{id}", body: "@updateFields content priority:priority:int dueDate:due_date:ms dueDateTime:due_date_time startDate:start_date:ms startDateTime:start_date_time notifyAll:notify_all name parentId:parent status timeEstimate:time_estimate:num " +
				"addAssignees:assignees.add:csvint removeAssignees:assignees.rem:csvint",
				build: clickUpTaskBody("updateFields")},
			"task/get": {path: "/task/{id}", build: func(ec *execCtx, i int, r *restRequest) error {
				if b, _ := ec.boolean(i, "includeSubtasks", false); b {
					r.query.Set("include_subtasks", "true")
				}
				if b, _ := ec.boolean(i, "includeMarkdownDescription", false); b {
					r.query.Set("include_markdown_description", "true")
				}
				return nil
			}},
			"task/getAll": {path: "/list/{list}/task", list: true, listKey: "tasks", pager: pageNumber("page", 0),
				query: "@filters archived subtasks includeClosed:include_closed orderBy:order_by statuses:statuses[] assignees:assignees[] tags:tags[] " +
					"dueDateGt:due_date_gt:ms dueDateLt:due_date_lt:ms dateCreatedGt:date_created_gt:ms dateCreatedLt:date_created_lt:ms dateUpdatedGt:date_updated_gt:ms dateUpdatedLt:date_updated_lt:ms",
				build: func(ec *execCtx, i int, r *restRequest) error {
					r.query.Set("page", "0")
					var cf []interface{}
					for _, x := range asList(asMap(fieldsOf(ec, i, "filters")["customFieldsUi"])["customFieldsValues"]) {
						m := asMap(x)
						op := toString(m["operator"])
						if op == "equal" {
							op = "="
						}
						cf = append(cf, map[string]interface{}{"field_id": m["fieldId"], "operator": op, "value": m["value"]})
					}
					if len(cf) > 0 {
						r.query.Set("custom_fields", mustJSON(cf))
					}
					return nil
				}},
			"task/member": {path: "/task/{id}/member", list: true, listKey: "members"},
			"task/delete": {method: "DELETE", path: "/task/{id}", success: true},
			"task/setCustomField": {method: "POST", path: "/task/{task}/field/{field}", build: func(ec *execCtx, i int, r *restRequest) error {
				v, _ := ec.param(i, "value", "")
				if b, _ := ec.boolean(i, "jsonParse", false); b {
					x, err := restConv("json", v)
					if err != nil {
						return permanentError{"ClickUp: Value is invalid JSON!"}
					}
					r.body["value"] = x
				} else if n, err := strconv.Atoi(strings.TrimSpace(toString(v))); err == nil {
					r.body["value"] = n
				} else {
					r.body["value"] = v
				}
				return nil
			}},
			"taskTag/add":           {method: "POST", path: "/task/{taskId}/tag/{tagName}", query: "@additionalFields *", success: true},
			"taskTag/remove":        {method: "DELETE", path: "/task/{taskId}/tag/{tagName}", query: "@additionalFields *", success: true},
			"taskList/add":          {method: "POST", path: "/list/{listId}/task/{taskId}", success: true},
			"taskList/remove":       {method: "DELETE", path: "/list/{listId}/task/{taskId}", success: true},
			"taskDependency/create": {method: "POST", path: "/task/{task}/dependency", body: "dependsOnTask:depends_on", success: true},
			"taskDependency/delete": {method: "DELETE", path: "/task/{task}/dependency", query: "dependsOnTask:depends_on", success: true},
			"comment/create":        {method: "POST", path: "/{commentOn}/{id}/comment", body: "commentText:comment_text @additionalFields assignee:assignee:int notifyAll:notify_all"},
			"comment/delete":        {method: "DELETE", path: "/comment/{comment}", success: true},
			"comment/getAll":        {path: "/{commentsOn}/{id}/comment", list: true, listKey: "comments"},
			"comment/update":        {method: "PUT", path: "/comment/{comment}", body: "@updateFields commentText:comment_text assignee:assignee:int resolved", success: true},
			"checklist/create":      {method: "POST", path: "/task/{task}/checklist", body: "name", out: "checklist"},
			"checklist/delete":      {method: "DELETE", path: "/checklist/{checklist}", success: true},
			"checklist/update":      {method: "PUT", path: "/checklist/{checklist}", body: "@updateFields name position:position:int", out: "checklist"},
			"checklistItem/create":  {method: "POST", path: "/checklist/{checklist}/checklist_item", body: "name @additionalFields assignee:assignee:int", out: "checklist"},
			"checklistItem/delete":  {method: "DELETE", path: "/checklist/{checklist}/checklist_item/{checklistItem}", success: true},
			"checklistItem/update":  {method: "PUT", path: "/checklist/{checklist}/checklist_item/{checklistItem}", body: "@updateFields name parent assignee:assignee:int resolved", out: "checklist"},
			"folder/create":         {method: "POST", path: "/space/{space}/folder", body: "name"},
			"folder/delete":         {method: "DELETE", path: "/folder/{folder}", success: true},
			"folder/get":            {path: "/folder/{folder}"},
			"folder/getAll":         {path: "/space/{space}/folder", list: true, listKey: "folders", query: "@filters archived"},
			"folder/update":         {method: "PUT", path: "/folder/{folder}", body: "@updateFields name"},
			"list/create": {method: "POST", path: "/space/{space}/list", body: "name @additionalFields content dueDate:due_date:ms dueDateTime:due_date_time priority:priority:int assignee:assignee:int status",
				build: clickUpFolderPath("/list")},
			"list/member":       {path: "/list/{id}/member", list: true, listKey: "members"},
			"list/customFields": {path: "/list/{list}/field", out: "fields"},
			"list/delete":       {method: "DELETE", path: "/list/{list}", success: true},
			"list/get":          {path: "/list/{list}"},
			"list/getAll":       {path: "/space/{space}/list", list: true, listKey: "lists", query: "@filters archived", build: clickUpFolderPath("/list")},
			"list/update":       {method: "PUT", path: "/list/{list}", body: "@updateFields name content dueDate:due_date:ms dueDateTime:due_date_time priority:priority:int assignee:assignee:int unsetStatus:unset_status"},
			"timeEntry/get": {path: "/team/{team}/time_entries/current", out: "data", build: func(ec *execCtx, i int, r *restRequest) error {
				if running, _ := ec.boolean(i, "running", false); !running {
					p, err := ec.restPath(i, "/team/{team}/time_entries/{timeEntry}")
					r.path = p
					return err
				}
				return nil
			}},
			"timeEntry/getAll": {path: "/team/{team}/time_entries", list: true, listKey: "data", query: "@filters startDate:start_date:ms endDate:end_date:ms *snake"},
			"timeEntry/delete": {method: "DELETE", path: "/team/{team}/time_entries/{timeEntry}", out: "data"},
			"timeEntry/start":  {method: "POST", path: "/team/{team}/time_entries/start", body: "task:tid @additionalFields description billable", out: "data"},
			"timeEntry/stop":   {method: "POST", path: "/team/{team}/time_entries/stop", out: "data"},
			"timeEntry/create": {method: "POST", path: "/team/{team}/time_entries", body: "start:start:ms task:tid @additionalFields description billable assignee:assignee:int", out: "data",
				build: func(ec *execCtx, i int, r *restRequest) error {
					d, _ := ec.num(i, "duration", 0)
					r.body["duration"] = int64(d * 60000)
					return nil
				}},
			"spaceTag/create": {method: "POST", path: "/space/{space}/tag", body: "name:tag.name foregroundColor:tag.tag_fg backgroundColor:tag.tag_bg", success: true},
			"spaceTag/delete": {method: "DELETE", path: "/space/{space}/tag/{name}", success: true},
			"spaceTag/getAll": {path: "/space/{space}/tag", list: true, listKey: "tags"},
			"spaceTag/update": {method: "PUT", path: "/space/{space}/tag/{name}", body: "newName:tag.name foregroundColor:tag.tag_fg backgroundColor:tag.tag_bg", success: true},
			"goal/create":     {method: "POST", path: "/team/{team}/goal", body: "name @additionalFields dueDate:due_date:ms description multipleOwners:multiple_owners color owners:owners:csvint", out: "goal"},
			"goal/delete":     {method: "DELETE", path: "/goal/{goal}", success: true},
			"goal/get":        {path: "/goal/{goal}", out: "goal"},
			"goal/getAll":     {path: "/team/{team}/goal", list: true, listKey: "goals"},
			"goal/update":     {method: "PUT", path: "/goal/{goal}", body: "@updateFields name dueDate:due_date:ms description color addOwners:add_owners:csvint removeOwners:rem_owners:csvint", out: "goal"},
			"guest/create":    {method: "POST", path: "/team/{team}/guest", body: "email @additionalFields canEditTags:can_edit_tags canSeeTimeSpend:can_see_time_spend canSeeTimeEstimated:can_see_time_estimated", out: "team"},
			"guest/delete":    {method: "DELETE", path: "/team/{team}/guest/{guest}", success: true},
			"guest/get":       {path: "/team/{team}/guest/{guest}", out: "team"},
			"guest/update":    {method: "PUT", path: "/team/{team}/guest/{guest}", body: "@updateFields username canEditTags:can_edit_tags canSeeTimeSpend:can_see_time_spend canSeeTimeEstimated:can_see_time_estimated", out: "team"},
		},
	})
}

// clickUpTaskBody applies ClickUp's special task fields.
func clickUpTaskBody(coll string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		f := fieldsOf(ec, i, coll)
		if v, ok := r.body["time_estimate"]; ok {
			n, _ := toFloat(v)
			r.body["time_estimate"] = int64(n * 6000) // as n8n does
		}
		if toBool(f["markdownContent"]) {
			delete(r.body, "content")
			r.body["markdown_content"] = f["content"]
		}
		if coll == "updateFields" {
			a := asMap(r.body["assignees"])
			for _, k := range []string{"add", "rem"} {
				if a[k] == nil {
					a[k] = []interface{}{}
				}
			}
			r.body["assignees"] = a
		}
		return nil
	}
}

// clickUpFolderPath uses /folder/{folder}… unless the list is folderless.
func clickUpFolderPath(suffix string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if fl, _ := ec.boolean(i, "folderless", false); fl {
			return nil
		}
		p, err := ec.restPath(i, "/folder/{folder}"+suffix)
		r.path = p
		return err
	}
}
