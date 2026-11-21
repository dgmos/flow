package n8n

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Todoist (n8n-nodes-base.todoist). Node versions before 2.2 call REST v2
// (and Sync v9 for moves and quick add); 2.2 calls the unified API v1,
// whose lists are paginated {results, next_cursor} pages.

func init() {
	registerREST("todoist", &restService{
		defResource: "task",
		defOps:      map[string]string{"task": "create", "project": "create", "section": "create", "comment": "create", "label": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "todoistApi", map[string]string{"apiKey": "todoistApi", "oAuth2": "todoistOAuth2Api"})
			if err != nil {
				return nil, err
			}
			base := "https://api.todoist.com/rest/v2"
			if ec.version >= 2.2 {
				base = "https://api.todoist.com/api/v1"
			}
			return bearerClient(ec.apiBase("todoist", base), c, "apiKey"), nil
		},
		ops: map[string]*restOp{
			"task/create": {method: "POST", path: "/tasks", body: "content project:project_id:rl @options " + todoistTaskFields + " section:section_id parentId:parent_id",
				build: todoistTaskBody(true)},
			"task/update": {method: "POST", path: "/tasks/{taskId}", body: "@updateFields content " + todoistTaskFields, build: todoistTaskBody(false), success: true},
			"task/close":  {method: "POST", path: "/tasks/{taskId}/close", success: true},
			"task/reopen": {method: "POST", path: "/tasks/{taskId}/reopen", success: true},
			"task/delete": {method: "DELETE", path: "/tasks/{taskId}", success: true},
			"task/get":    {path: "/tasks/{taskId}"},
			"task/getAll": {run: todoistTasks},
			"task/move":   {run: todoistMove},
			"task/quickAdd": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				body := map[string]interface{}{}
				if err := ec.restFields(i, "text @options note reminder auto_reminder", body); err != nil {
					return nil, err
				}
				if body["auto_reminder"] == false {
					delete(body, "auto_reminder")
				}
				path := "/quick/add"
				if ec.version >= 2.2 {
					path = "/tasks/quick"
				}
				v, err := todoistSyncClient(ec, c).do(ec, &restRequest{method: "POST", path: path, body: body})
				if err != nil {
					return nil, err
				}
				return restItems(v), nil
			}},

			"project/create":           {method: "POST", path: "/projects", body: "name @projectOptions *"},
			"project/delete":           {method: "DELETE", path: "/projects/{projectId}", success: true},
			"project/get":              {path: "/projects/{projectId}"},
			"project/getAll":           {run: todoistAllOf("/projects", "")},
			"project/update":           {method: "POST", path: "/projects/{projectId}", body: "@projectUpdateFields *", success: true},
			"project/archive":          {method: "POST", path: "/projects/{projectId}/archive", success: true},
			"project/unarchive":        {method: "POST", path: "/projects/{projectId}/unarchive", success: true},
			"project/getCollaborators": {run: todoistAllOf("/projects/{projectId}/collaborators", "")},

			"section/create": {method: "POST", path: "/sections", body: "sectionName:name sectionProject:project_id:rl @sectionOptions *"},
			"section/delete": {method: "DELETE", path: "/sections/{sectionId}", success: true},
			"section/get":    {path: "/sections/{sectionId}"},
			"section/getAll": {run: todoistAllOf("/sections", "@sectionFilters project_id")},
			"section/update": {method: "POST", path: "/sections/{sectionId}", body: "@sectionUpdateFields *", success: true},

			"comment/create": {method: "POST", path: "/comments", body: "commentTaskId:task_id commentContent:content"},
			"comment/delete": {method: "DELETE", path: "/comments/{commentId}", success: true},
			"comment/get":    {path: "/comments/{commentId}"},
			"comment/getAll": {run: todoistAllOf("/comments", "@commentFilters task_id project_id")},
			"comment/update": {method: "POST", path: "/comments/{commentId}", body: "@commentUpdateFields *", success: true},

			"label/create": {method: "POST", path: "/labels", body: "labelName:name @labelOptions *"},
			"label/delete": {method: "DELETE", path: "/labels/{labelId}", success: true},
			"label/get":    {path: "/labels/{labelId}"},
			"label/getAll": {run: todoistAllOf("/labels", "")},
			"label/update": {method: "POST", path: "/labels/{labelId}", body: "@labelUpdateFields *", success: true},
		},
	})
}

// todoistTaskFields are the task options shared by create and update.
const todoistTaskFields = "description dueDateTime:due_datetime dueString:due_string dueLang:due_lang order dueDate:due_date assigneeId:assignee_id duration durationUnit:duration_unit deadlineDate:deadline_date"

// todoistTaskBody applies priority, due time and label conversions.
func todoistTaskBody(create bool) func(ec *execCtx, i int, r *restRequest) error {
	coll := "updateFields"
	if create {
		coll = "options"
	}
	return func(ec *execCtx, i int, r *restRequest) error {
		f := fieldsOf(ec, i, coll)
		switch p := f["priority"]; {
		case restEmpty(p) || p == float64(0):
			if create {
				r.body["priority"] = 1
			}
		default:
			n, _ := strconv.Atoi(strings.TrimSpace(toString(p)))
			r.body["priority"] = n
		}
		if d, ok := r.body["due_datetime"].(string); ok && strings.HasSuffix(d, ".000Z") {
			r.body["due_datetime"] = strings.TrimSuffix(d, ".000Z") + "Z"
		}
		labels := f["labels"]
		if create {
			labels, _ = ec.param(i, "labels", nil)
		}
		l := asList(labels)
		if len(l) == 0 {
			return nil
		}
		if ec.version < 2 {
			// Version 1 selects label ids; the API wants names.
			c, err := restServices["n8n-nodes-base.todoist"].client(ec, i)
			if err != nil {
				return err
			}
			names := make([]interface{}, 0, len(l))
			for _, id := range l {
				lab, err := c.do(ec, &restRequest{method: "GET", path: "/labels/" + url.PathEscape(toString(id))})
				if err != nil {
					return err
				}
				names = append(names, asMap(lab)["name"])
			}
			l = names
		}
		r.body["labels"] = l
		return nil
	}
}

// todoistSyncClient is the client of Sync API calls (Sync v9 before 2.2).
func todoistSyncClient(ec *execCtx, c *restClient) *restClient {
	if ec.version >= 2.2 {
		return c
	}
	return &restClient{base: ec.apiBase("todoistSync", "https://api.todoist.com/sync/v9"), headers: c.headers}
}

func todoistMove(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	id, _ := ec.param(i, "taskId", "")
	args := map[string]interface{}{"id": id}
	if ec.version < 2.1 {
		args["section_id"], _ = ec.param(i, "section", "")
	} else {
		o := fieldsOf(ec, i, "options")
		switch {
		case !restEmpty(o["parent"]):
			args["parent_id"] = o["parent"]
		case !restEmpty(o["section"]):
			args["section_id"] = o["section"]
		default:
			p, _ := ec.param(i, "project", "")
			args["project_id"] = rlValue(p)
		}
	}
	body := map[string]interface{}{"commands": []interface{}{map[string]interface{}{"type": "item_move", "uuid": uuid.NewString(), "args": args}}}
	if _, err := todoistSyncClient(ec, c).do(ec, &restRequest{method: "POST", path: "/sync", body: body}); err != nil {
		return nil, err
	}
	return []Item{{JSON: map[string]interface{}{"success": true}}}, nil
}

func todoistTasks(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	q := map[string]interface{}{}
	if err := ec.restFields(i, "@filters projectId:project_id sectionId:section_id labelId:label lang ids", q); err != nil {
		return nil, err
	}
	path := "/tasks"
	if f := fieldsOf(ec, i, "filters")["filter"]; !restEmpty(f) {
		if ec.version >= 2.2 {
			q["query"], path = f, "/tasks/filter"
		} else {
			q["filter"] = f
		}
	}
	limit := -1
	if all, _ := ec.boolean(i, "returnAll", false); !all {
		n, _ := ec.num(i, "limit", 50)
		limit = int(n)
	}
	l, err := todoistAll(ec, c, path, q, limit)
	if err != nil {
		return nil, err
	}
	return restItems(l), nil
}

// todoistAllOf lists every record of path with query fields from spec.
func todoistAllOf(path, spec string) func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		p, err := ec.restPath(i, path)
		if err != nil {
			return nil, err
		}
		q := map[string]interface{}{}
		if err := ec.restFields(i, spec, q); err != nil {
			return nil, err
		}
		l, err := todoistAll(ec, c, p, q, -1)
		if err != nil {
			return nil, err
		}
		return restItems(l), nil
	}
}

// todoistAll fetches a list: one call before 2.2 (truncated to limit),
// cursor pages of up to 200 since. limit < 0 means all.
func todoistAll(ec *execCtx, c *restClient, path string, q map[string]interface{}, limit int) ([]interface{}, error) {
	query := map[string][]string{}
	for k, v := range q {
		query[k] = queryValues(v)
	}
	if ec.version < 2.2 {
		v, err := c.do(ec, &restRequest{method: "GET", path: path, query: query})
		if err != nil {
			return nil, err
		}
		l := asList(v)
		if limit > 0 && len(l) > limit {
			l = l[:limit]
		}
		return l, nil
	}
	if limit == 0 {
		return []interface{}{}, nil
	}
	var out []interface{}
	for n := 0; n < 1000; n++ {
		if limit > 0 {
			query["limit"] = []string{strconv.Itoa(min(limit-len(out), 200))}
		}
		v, err := c.do(ec, &restRequest{method: "GET", path: path, query: query})
		if err != nil {
			return nil, err
		}
		res, ok := asMap(v)["results"].([]interface{})
		if !ok {
			break
		}
		out = append(out, res...)
		if limit > 0 && len(out) >= limit {
			return out[:limit], nil
		}
		cur := toString(asMap(v)["next_cursor"])
		if cur == "" {
			break
		}
		query["cursor"] = []string{cur}
	}
	return out, nil
}
