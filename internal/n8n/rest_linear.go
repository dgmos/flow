package n8n

import "strings"

// Linear (n8n-nodes-base.linear), GraphQL API. Every operation is a query
// or mutation whose variables come from a field list.

const linearIssueFields = `id identifier title priority archivedAt
	assignee { id displayName email }
	state { id name }
	createdAt
	creator { id displayName email }
	team { id name }
	description dueDate
	cycle { id name }`

func init() {
	registerREST("linear", &restService{
		defResource: "issue",
		defOps:      map[string]string{"issue": "create", "comment": "addComment"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, t, err := ec.restCred(i, "linearApi", map[string]string{"apiToken": "linearApi", "oAuth2": "linearOAuth2Api"})
			if err != nil {
				return nil, err
			}
			tok := credString(c, "apiKey")
			if t == "linearOAuth2Api" {
				tok = "Bearer " + accessToken(c)
			}
			return &restClient{base: ec.apiBase("linear", "https://api.linear.app") + "/graphql", headers: map[string]string{"Authorization": tok}}, nil
		},
		ops: map[string]*restOp{
			"issue/create": linearOp(`mutation IssueCreate($title: String!, $teamId: String!, $description: String, $assigneeId: String, $priorityId: Int, $stateId: String) {
	issueCreate(input: {title: $title, description: $description, teamId: $teamId, assigneeId: $assigneeId, priority: $priorityId, stateId: $stateId}) {
		success issue { `+linearIssueFields+` } } }`,
				"teamId title @additionalFields *", "data.issueCreate.issue"),
			"issue/delete": linearOp(`mutation IssueDelete($issueId: String!) { issueDelete(id: $issueId) { success } }`, "issueId", "data.issueDelete"),
			"issue/get":    linearOp(`query Issue($issueId: String!) { issue(id: $issueId) { `+linearIssueFields+` } }`, "issueId", "data.issue"),
			"issue/update": linearOp(`mutation IssueUpdate($issueId: String!, $title: String, $teamId: String, $description: String, $assigneeId: String, $priorityId: Int, $stateId: String) {
	issueUpdate(id: $issueId, input: {title: $title, description: $description, teamId: $teamId, assigneeId: $assigneeId, priority: $priorityId, stateId: $stateId}) {
		success issue { `+linearIssueFields+` } } }`,
				"issueId @updateFields *", "data.issueUpdate.issue"),
			"issue/addLink": linearOp(`mutation AttachmentLinkURL($url: String!, $issueId: String!) { attachmentLinkURL(url: $url, issueId: $issueId) { success } }`,
				"issueId link:url", "data.attachmentLinkURL"),
			"issue/getAll": {run: linearIssues},
			"comment/addComment": linearOp(`mutation CommentCreate($issueId: String!, $body: String!, $parentId: String) {
	commentCreate(input: {issueId: $issueId, body: $body, parentId: $parentId}) { success comment { id } } }`,
				"issueId comment:body @additionalFields parentId", "data.commentCreate"),
		},
	})
}

// linearOp runs one GraphQL document with variables from a field list.
func linearOp(query, vars, out string) *restOp {
	return &restOp{run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		v := map[string]interface{}{}
		if err := ec.restFields(i, vars, v); err != nil {
			return nil, err
		}
		if p, ok := v["parentId"].(string); ok && strings.TrimSpace(p) == "" {
			delete(v, "parentId")
		}
		res, err := linearGQL(ec, c, query, v)
		if err != nil {
			return nil, err
		}
		return restItems(jpath(res, out)), nil
	}}
}

// linearIssues pages through issues 50 at a time (fewer when a limit is
// smaller), following pageInfo.endCursor.
func linearIssues(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	all, _ := ec.boolean(i, "returnAll", false)
	limit := 0
	if !all {
		n, _ := ec.num(0, "limit", 50)
		limit = int(n)
	}
	first := 50
	if limit > 0 && limit < first {
		first = limit
	}
	q := `query Issues($first: Int, $after: String) { issues(first: $first, after: $after) { nodes { ` + linearIssueFields + ` } pageInfo { hasNextPage endCursor } } }`
	vars := map[string]interface{}{"first": first, "after": nil}
	var out []interface{}
	for n := 0; n < 1000; n++ {
		res, err := linearGQL(ec, c, q, vars)
		if err != nil {
			return nil, err
		}
		out = append(out, asList(jpath(res, "data.issues.nodes"))...)
		if limit > 0 && len(out) >= limit {
			out = out[:limit]
			break
		}
		if !toBool(jpath(res, "data.issues.pageInfo.hasNextPage")) {
			break
		}
		vars["after"] = jpath(res, "data.issues.pageInfo.endCursor")
	}
	return restItems(out), nil
}

// linearGQL posts a GraphQL request and fails on GraphQL errors.
func linearGQL(ec *execCtx, c *restClient, query string, vars map[string]interface{}) (interface{}, error) {
	res, err := c.do(ec, &restRequest{method: "POST", body: map[string]interface{}{"query": query, "variables": vars}})
	if err != nil {
		return nil, err
	}
	if errs := asList(jpath(res, "errors")); len(errs) > 0 {
		msg := toString(asMap(errs[0])["message"])
		if msg == "" {
			msg = "Unknown API Error"
		}
		return nil, permanentError{ec.node.Name + ": " + msg}
	}
	return res, nil
}
