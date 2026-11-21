package n8n

import (
	"net/http"
	"strconv"
)

// Trello (n8n-nodes-base.trello), API v1. The trelloApi credential sends
// key and token as query parameters.

func init() {
	registerREST("trello", &restService{
		defResource: "card",
		defOps:      map[string]string{"attachment": "getAll", "board": "create", "boardMember": "add", "card": "create", "cardComment": "create", "checklist": "getAll", "label": "getAll", "list": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			if a, _ := ec.str(i, "authentication", "apiKey"); a == "oAuth1" {
				return nil, permanentError{ec.node.Name + ": Trello OAuth1 authentication is not supported; use the API key credential"}
			}
			c, err := ec.credential("trelloApi")
			if err != nil {
				return nil, err
			}
			q := map[string][]string{"key": {credString(c, "apiKey")}, "token": {credString(c, "apiToken")}}
			return &restClient{base: ec.apiBase("trello", "https://api.trello.com/1"), query: q}, nil
		},
		ops: map[string]*restOp{
			"board/create": {method: "POST", path: "/boards", body: "name description:desc @additionalFields *"},
			"board/delete": {method: "DELETE", path: "/boards/{id}"},
			"board/get":    {path: "/boards/{id}", query: "@additionalFields *"},
			"board/update": {method: "PUT", path: "/boards/{id}", query: "@updateFields *"},

			"boardMember/getAll": trelloList("/boards/{id}/members", "", true),
			"boardMember/add":    {method: "PUT", path: "/boards/{id}/members/{idMember}", query: "type =allowBillableGuest:false @additionalFields allowBillableGuest"},
			"boardMember/invite": {method: "PUT", path: "/boards/{id}/members", query: "email @additionalFields type", body: "@additionalFields fullName"},
			"boardMember/remove": {method: "DELETE", path: "/boards/{id}/members/{idMember}"},

			"card/create": {method: "POST", path: "/cards", body: "listId:idList:rl name description:desc @additionalFields *"},
			"card/delete": {method: "DELETE", path: "/cards/{id}"},
			"card/get":    {path: "/cards/{id}", query: "@additionalFields *"},
			"card/update": {method: "PUT", path: "/cards/{id}", query: "@updateFields *"},

			"cardComment/create": {method: "POST", path: "/cards/{cardId}/actions/comments", body: "text"},
			"cardComment/delete": {method: "DELETE", path: "/cards/{cardId}/actions/{commentId}/comments"},
			"cardComment/update": {method: "PUT", path: "/cards/{cardId}/actions/{commentId}/comments", query: "text"},

			"list/archive":  {method: "PUT", path: "/lists/{id}/closed", query: "archive:value"},
			"list/create":   {method: "POST", path: "/lists", body: "idBoard name @additionalFields *"},
			"list/get":      {path: "/lists/{id}", query: "@additionalFields *"},
			"list/getAll":   trelloList("/boards/{id}/lists", "@additionalFields *", false),
			"list/getCards": trelloList("/lists/{id}/cards", "@additionalFields *", true),
			"list/update":   {method: "PUT", path: "/lists/{id}", query: "@updateFields *"},

			"attachment/create": {method: "POST", path: "/cards/{cardId}/attachments", body: "url @additionalFields *"},
			"attachment/delete": {method: "DELETE", path: "/cards/{cardId}/attachments/{id}"},
			"attachment/get":    {path: "/cards/{cardId}/attachments/{id}", query: "@additionalFields *"},
			"attachment/getAll": {path: "/cards/{cardId}/attachments", query: "@additionalFields *"},

			"checklist/create":              {method: "POST", path: "/cards/{cardId}/checklists", body: "name @additionalFields *"},
			"checklist/delete":              {method: "DELETE", path: "/cards/{cardId}/checklists/{id}"},
			"checklist/get":                 {path: "/checklists/{id}", query: "@additionalFields *"},
			"checklist/getAll":              {path: "/cards/{cardId}/checklists", query: "@additionalFields *"},
			"checklist/getCheckItem":        {path: "/cards/{cardId}/checkItem/{checkItemId}", query: "@additionalFields *"},
			"checklist/createCheckItem":     {method: "POST", path: "/checklists/{checklistId}/checkItems", body: "name @additionalFields *"},
			"checklist/deleteCheckItem":     {method: "DELETE", path: "/cards/{cardId}/checkItem/{checkItemId}"},
			"checklist/updateCheckItem":     {method: "PUT", path: "/cards/{cardId}/checkItem/{checkItemId}", query: "@additionalFields *"},
			"checklist/completedCheckItems": {path: "/cards/{cardId}/checkItemStates", query: "@additionalFields *"},

			"label/create":      {method: "POST", path: "/labels", body: "boardId:idBoard:rl name color"},
			"label/delete":      {method: "DELETE", path: "/labels/{id}"},
			"label/get":         {path: "/labels/{id}", query: "@additionalFields *"},
			"label/getAll":      {path: "/board/{boardId}/labels", query: "@additionalFields *"},
			"label/update":      {method: "PUT", path: "/labels/{id}", query: "@updateFields *"},
			"label/addLabel":    {method: "POST", path: "/cards/{cardId}/idLabels", body: "id:value"},
			"label/removeLabel": {method: "DELETE", path: "/cards/{cardId}/idLabels/{id}"},
		},
	})
}

// trelloList is a list call: limit=N for one page, or (returnAll, paged)
// pages of 30 sorted by -id, walking back with before=<last id>.
func trelloList(path, query string, paged bool) *restOp {
	op := &restOp{path: path, query: query, list: true, sizeParam: "limit"}
	if !paged {
		op.sizeParam = ""
		op.build = func(ec *execCtx, i int, r *restRequest) error {
			if all, _ := ec.boolean(i, "returnAll", false); !all {
				n, _ := ec.num(i, "limit", 50)
				r.query.Set("limit", strconv.Itoa(int(n)))
			}
			return nil
		}
		return op
	}
	op.build = func(ec *execCtx, i int, r *restRequest) error {
		if all, _ := ec.boolean(i, "returnAll", false); all {
			r.query.Set("limit", "30")
			r.query.Set("sort", "-id")
		}
		return nil
	}
	op.pager = func(p *restPage, _ interface{}, _ http.Header) bool {
		if len(p.last) < 30 {
			return false
		}
		p.query.Set("before", toString(asMap(p.last[len(p.last)-1])["id"]))
		return true
	}
	return op
}
