package n8n

import "strings"

// monday.com (n8n-nodes-base.mondayCom) node version 1, GraphQL API v2.

const mondayColumnValues = `column_values {
	id text type value
	... on BoardRelationValue { display_value linked_item_ids }
	... on DependencyValue { display_value linked_item_ids }
	... on MirrorValue { display_value }
	... on SubtasksValue { display_value subitems { id name } }
	column { title archived description settings_str }
}`

const mondayBoardFields = `id name description state board_folder_id board_kind owners { id }`

const mondayItemFields = `{ id name created_at state ` + mondayColumnValues + ` }`

const mondayItemFieldsBoard = `{ id name created_at state board { id } ` + mondayColumnValues + ` }`

func init() {
	registerREST("mondayCom", &restService{
		defResource: "board",
		defOps:      map[string]string{"board": "create", "boardColumn": "create", "boardGroup": "create", "boardItem": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			if ec.version >= 2 {
				return nil, permanentError{ec.node.Name + ": monday.com node version 2 is not supported; use version 1"}
			}
			c, _, err := ec.restCred(i, "mondayComApi", map[string]string{"accessToken": "mondayComApi", "oAuth2": "mondayComOAuth2Api"})
			if err != nil {
				return nil, err
			}
			cl := bearerClient(ec.apiBase("mondayCom", "https://api.monday.com/v2"), c, "apiToken")
			cl.headers["API-Version"] = "2026-07"
			return cl, nil
		},
		ops: map[string]*restOp{
			"board/archive": mondayOp(`mutation ($id: ID!) { archive_board (board_id: $id) { id } }`, "boardId:id", "data.archive_board"),
			"board/create": mondayOp(`mutation ($name: String!, $kind: BoardKind!, $templateId: ID) { create_board (board_name: $name, board_kind: $kind, template_id: $templateId) { id } }`,
				"name kind @additionalFields templateId", "data.create_board"),
			"board/get":    mondayOp(`query ($id: [ID!]) { boards (ids: $id) { `+mondayBoardFields+` } }`, "boardId:id", "data.boards"),
			"board/getAll": {run: mondayBoards},

			"boardColumn/create": mondayOp(`mutation ($boardId: ID!, $title: String!, $columnType: ColumnType!, $defaults: JSON) { create_column (board_id: $boardId, title: $title, column_type: $columnType, defaults: $defaults) { id } }`,
				"boardId title columnType @additionalFields defaults::json", "data.create_column"),
			"boardColumn/getAll": mondayOp(`query ($boardId: [ID!]) { boards (ids: $boardId) { columns { id title type settings_str archived } } }`, "boardId", "data.boards.0.columns"),

			"boardGroup/create": mondayOp(`mutation ($boardId: ID!, $groupName: String!) { create_group (board_id: $boardId, group_name: $groupName) { id } }`, "boardId name:groupName", "data.create_group"),
			"boardGroup/delete": mondayOp(`mutation ($boardId: ID!, $groupId: String!) { delete_group (board_id: $boardId, group_id: $groupId) { id } }`, "boardId groupId", "data.delete_group"),
			"boardGroup/getAll": mondayOp(`query ($boardId: [ID!]) { boards (ids: $boardId) { id groups { id title color position archived } } }`, "boardId", "data.boards.0.groups"),

			"boardItem/addUpdate": mondayOp(`mutation ($itemId: ID!, $value: String!) { create_update (item_id: $itemId, body: $value) { id } }`, "itemId value", "data.create_update"),
			"boardItem/changeColumnValue": mondayOp(`mutation ($boardId: ID!, $itemId: ID!, $columnId: String!, $value: JSON!) { change_column_value (board_id: $boardId, item_id: $itemId, column_id: $columnId, value: $value) { id } }`,
				"boardId itemId columnId value::json", "data.change_column_value"),
			"boardItem/changeMultipleColumnValues": mondayOp(`mutation ($boardId: ID!, $itemId: ID!, $columnValues: JSON!) { change_multiple_column_values (board_id: $boardId, item_id: $itemId, column_values: $columnValues) { id } }`,
				"boardId itemId columnValues::json", "data.change_multiple_column_values"),
			"boardItem/create": mondayOp(`mutation ($boardId: ID!, $groupId: String!, $itemName: String!, $columnValues: JSON) { create_item (board_id: $boardId, group_id: $groupId, item_name: $itemName, column_values: $columnValues) { id } }`,
				"boardId groupId itemName name:itemName @additionalFields columnValues::json", "data.create_item"),
			"boardItem/delete": mondayOp(`mutation ($itemId: ID!) { delete_item (item_id: $itemId) { id } }`, "itemId", "data.delete_item"),
			"boardItem/get": mondayOp(`query ($itemId: [ID!]) { items (ids: $itemId) { id name created_at state `+mondayColumnValues+` } }`,
				"itemId::csv", "data.items"),
			"boardItem/getAll": {run: mondayItems(`query ($boardId: [ID!], $groupId: [String], $limit: Int) { boards(ids: $boardId) { groups(ids: $groupId) { id items_page(limit: $limit) { cursor items `+mondayItemFields+` } } } }`,
				"boardId groupId", "data.boards.0.groups.0.items_page", mondayItemFields)},
			"boardItem/getByColumnValue": {run: mondayItems(`query ($boardId: ID!, $columnId: String!, $columnValue: String!, $limit: Int) { items_page_by_column_values(limit: $limit, board_id: $boardId, columns: [{column_id: $columnId, column_values: [$columnValue]}]) { cursor items `+mondayItemFieldsBoard+` } }`,
				"boardId columnId columnValue", "data.items_page_by_column_values", mondayItemFieldsBoard)},
			"boardItem/move": mondayOp(`mutation ($groupId: String!, $itemId: ID!) { move_item_to_group (group_id: $groupId, item_id: $itemId) { id } }`, "groupId itemId", "data.move_item_to_group"),
		},
	})
}

// mondayVars resolves GraphQL variables. JSON variables (":json") are
// validated and sent as JSON strings, as n8n does.
func mondayVars(ec *execCtx, i int, spec string) (map[string]interface{}, error) {
	v := map[string]interface{}{}
	if err := ec.restFields(i, spec, v); err != nil {
		return nil, err
	}
	for _, tok := range strings.Fields(spec) {
		if p := strings.Split(tok, ":"); len(p) == 3 && p[2] == "json" {
			k := firstNonEmpty(p[1], p[0])
			if x, ok := v[k]; ok {
				v[k] = mustJSON(x)
			}
		}
	}
	if ct, ok := v["columnType"].(string); ok {
		v["columnType"] = snakeCase(ct)
	}
	return v, nil
}

func mondayOp(query, vars, out string) *restOp {
	return &restOp{run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		v, err := mondayVars(ec, i, vars)
		if err != nil {
			return nil, err
		}
		res, err := mondayGQL(ec, c, query, v)
		if err != nil {
			return nil, err
		}
		return restItems(jpath(res, out)), nil
	}}
}

// mondayBoards lists boards; returnAll walks pages of 50 until one is empty.
func mondayBoards(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	q := `query ($page: Int, $limit: Int) { boards (page: $page, limit: $limit) { ` + mondayBoardFields + ` } }`
	if all, _ := ec.boolean(i, "returnAll", false); !all {
		n, _ := ec.num(i, "limit", 50)
		res, err := mondayGQL(ec, c, q, map[string]interface{}{"page": 1, "limit": int(n)})
		if err != nil {
			return nil, err
		}
		return restItems(jpath(res, "data.boards")), nil
	}
	var out []interface{}
	for page := 1; page <= 1000; page++ {
		res, err := mondayGQL(ec, c, q, map[string]interface{}{"page": page, "limit": 50})
		if err != nil {
			return nil, err
		}
		b := asList(jpath(res, "data.boards"))
		if len(b) == 0 {
			break
		}
		out = append(out, b...)
	}
	return restItems(out), nil
}

// mondayItems runs an items_page query; returnAll follows the cursor with
// next_items_page (100 per page).
func mondayItems(query, vars, pagePath, fields string) func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		v, err := mondayVars(ec, i, vars)
		if err != nil {
			return nil, err
		}
		all, _ := ec.boolean(i, "returnAll", false)
		if !all {
			n, _ := ec.num(i, "limit", 50)
			v["limit"] = int(n)
		} else {
			v["limit"] = 100
		}
		res, err := mondayGQL(ec, c, query, v)
		if err != nil {
			return nil, err
		}
		page := asMap(jpath(res, pagePath))
		out := asList(page["items"])
		if !all {
			return restItems(out), nil
		}
		next := `query ($cursor: String!) { next_items_page (cursor: $cursor, limit: 100) { cursor items ` + fields + ` } }`
		for cursor, n := toString(page["cursor"]), 0; cursor != "" && cursor != "<nil>" && n < 1000; n++ {
			res, err := mondayGQL(ec, c, next, map[string]interface{}{"cursor": cursor})
			if err != nil {
				return nil, err
			}
			p := asMap(jpath(res, "data.next_items_page"))
			out = append(out, asList(p["items"])...)
			cursor = toString(p["cursor"])
		}
		return restItems(out), nil
	}
}

// mondayGQL posts a GraphQL request. Errors arrive with HTTP 200; they fail
// the call only when no data came back.
func mondayGQL(ec *execCtx, c *restClient, query string, vars map[string]interface{}) (interface{}, error) {
	res, err := c.do(ec, &restRequest{method: "POST", body: map[string]interface{}{"query": query, "variables": vars}})
	if err != nil {
		return nil, err
	}
	if errs := asList(jpath(res, "errors")); len(errs) > 0 && jpath(res, "data") == nil {
		var msgs []string
		for _, e := range errs {
			if m := toString(asMap(e)["message"]); m != "" {
				msgs = append(msgs, m)
			}
		}
		return nil, permanentError{ec.node.Name + ": " + strings.Join(msgs, "\n")}
	}
	return res, nil
}
