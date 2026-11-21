package n8n

import (
	"net/http"
	"strings"
)

// Asana (n8n-nodes-base.asana), API 1.0. Request bodies are wrapped in
// {"data": …} and results are unwrapped from it.

func init() {
	registerREST("asana", &restService{
		defResource: "task",
		defOps:      map[string]string{"subtask": "create", "task": "create", "taskComment": "add", "taskProject": "add", "taskTag": "add", "user": "get", "project": "get"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "asanaApi", map[string]string{"accessToken": "asanaApi", "oAuth2": "asanaOAuth2Api"})
			if err != nil {
				return nil, err
			}
			return bearerClient(ec.apiBase("asana", "https://app.asana.com/api/1.0"), c), nil
		},
		listKey: "data",
		pager:   nextURLPager("next_page.uri"),
		ops: map[string]*restOp{
			"subtask/create": {method: "POST", path: "/tasks/{taskId}/subtasks", body: "name @otherProperties *", wrap: "data", out: "data"},
			"subtask/getAll": {path: "/tasks/{taskId}/subtasks", query: "@options *", build: asanaOptFields, list: true, pager: asanaNoPages},

			"task/create": {method: "POST", path: "/tasks", body: "name workspace @otherProperties *", wrap: "data", out: "data"},
			"task/delete": {method: "DELETE", path: "/tasks/{id}", out: "data"},
			"task/get":    {path: "/tasks/{id}", out: "data"},
			"task/getAll": {path: "/tasks", query: "@filters modified_since::iso completed_since::iso *", build: asanaOptFields,
				list: true, sizeParam: "limit", maxSize: 100},
			"task/move":   {method: "POST", path: "/sections/{section}/addTask", body: "id:task", wrap: "data", success: true},
			"task/update": {method: "PUT", path: "/tasks/{id}", body: "@otherProperties *", wrap: "data", out: "data"},
			"task/search": {path: "/workspaces/{workspace}/tasks/search", query: "@searchTaskProperties *", out: "data"},

			"taskComment/add": {method: "POST", path: "/tasks/{id}/stories", body: "@additionalFields *", wrap: "data", out: "data",
				build: func(ec *execCtx, i int, r *restRequest) error {
					text, _ := ec.param(i, "text", "")
					if html, _ := ec.boolean(i, "isTextHtml", false); html {
						r.body["html_text"] = text
					} else {
						r.body["text"] = text
					}
					return nil
				}},
			"taskComment/remove": {method: "DELETE", path: "/stories/{id}", success: true},

			"taskTag/add":    {method: "POST", path: "/tasks/{id}/addTag", body: "tag", wrap: "data", success: true},
			"taskTag/remove": {method: "POST", path: "/tasks/{id}/removeTag", body: "tag", wrap: "data", success: true},

			"taskProject/add":    {method: "POST", path: "/tasks/{id}/addProject", body: "project @additionalFields *", wrap: "data", success: true},
			"taskProject/remove": {method: "POST", path: "/tasks/{id}/removeProject", body: "project", wrap: "data", success: true},

			"user/get":    {path: "/users/{userId}", out: "data"},
			"user/getAll": {path: "/workspaces/{workspace}/users", out: "data"},

			"project/create": {method: "POST", path: "/teams/{team}/projects", body: "name workspace", query: "@additionalFields color due_on notes", wrap: "data", out: "data"},
			"project/delete": {method: "DELETE", path: "/projects/{id}", success: true},
			"project/get":    {path: "/projects/{id}", out: "data"},
			"project/getAll": {path: "/projects", query: "@additionalFields archived", list: true, sizeParam: "limit", maxSize: 100,
				build: func(ec *execCtx, i int, r *restRequest) error {
					if t := toString(fieldsOf(ec, i, "additionalFields")["team"]); t != "" {
						r.query.Set("team", t)
						w, _ := ec.str(i, "workspace", "")
						r.query.Set("workspace", w)
					}
					if r.query.Get("archived") == "false" {
						r.query.Del("archived") // n8n only sends it when true
					}
					return nil
				}},
			"project/update": {method: "PUT", path: "/projects/{id}", query: "@updateFields color due_on notes", body: "@updateFields name owner team", wrap: "data", out: "data"},
		},
	})
}

// asanaNoPages makes a list call fetch a single page.
func asanaNoPages(*restPage, interface{}, http.Header) bool { return false }

// asanaTaskFields are the task fields that opt_fields "*" expands to.
var asanaTaskFields = "gid,resource_type,name,approval_status,assignee_status,completed,completed_at,completed_by,created_at,dependencies,dependents,due_at,due_on,external,html_notes,liked,likes,memberships,modified_at,notes,num_likes,resource_subtype,start_on,assignee,custom_fields,followers,parent,permalink_url,projects,tags,workspace"

// asanaOptFields joins the opt_fields list (n8n expands "*" to all task fields).
func asanaOptFields(ec *execCtx, i int, r *restRequest) error {
	f := r.query["opt_fields"]
	if len(f) == 0 {
		return nil
	}
	for _, x := range f {
		if x == "*" {
			r.query.Set("opt_fields", asanaTaskFields)
			return nil
		}
	}
	r.query.Set("opt_fields", strings.Join(f, ","))
	return nil
}
