package n8n

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Mattermost (n8n-nodes-base.mattermost), API v4 on the credential's
// server.

func init() {
	registerREST("mattermost", &restService{
		defResource: "message",
		defOps:      map[string]string{"channel": "create", "message": "post", "reaction": "create"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, err := ec.credential("mattermostApi")
			if err != nil {
				return nil, err
			}
			base := strings.TrimRight(credString(c, "baseUrl"), "/") + "/api/v4"
			return bearerClient(ec.apiBase("mattermost", base), c, "accessToken"), nil
		},
		ops: map[string]*restOp{
			"channel/addUser": {method: "POST", path: "/channels/{channelId}/members", body: "userId:user_id"},
			"channel/create": {method: "POST", path: "/channels", body: "teamId:team_id displayName:display_name channel:name", build: func(ec *execCtx, i int, r *restRequest) error {
				r.body["type"] = "O"
				if t, _ := ec.str(i, "type", "public"); t != "public" {
					r.body["type"] = "P"
				}
				return nil
			}},
			"channel/delete":     {method: "DELETE", path: "/channels/{channelId}"},
			"channel/members":    {run: mattermostMembers},
			"channel/restore":    {method: "POST", path: "/channels/{channelId}/restore"},
			"channel/search":     {method: "POST", path: "/teams/{teamId}/channels/search", body: "term", post: mattermostLimit(true)},
			"channel/statistics": {path: "/channels/{channelId}/stats"},

			"message/delete":        {method: "DELETE", path: "/posts/{postId}"},
			"message/post":          {method: "POST", path: "/posts", body: "channelId:channel_id message @otherOptions *", build: mattermostAttachments},
			"message/postEphemeral": {method: "POST", path: "/posts/ephemeral", body: "userId:user_id channelId:post.channel_id message:post.message"},

			"reaction/create": {method: "POST", path: "/reactions", body: "userId:user_id postId:post_id", build: func(ec *execCtx, i int, r *restRequest) error {
				e, _ := ec.str(i, "emojiName", "")
				r.body["emoji_name"] = strings.ReplaceAll(e, ":", "")
				r.body["create_at"] = time.Now().UnixMilli()
				return nil
			}},
			"reaction/delete": {method: "DELETE", path: "/users/{userId}/posts/{postId}/reactions/", build: func(ec *execCtx, i int, r *restRequest) error {
				e, _ := ec.str(i, "emojiName", "")
				r.path += url.PathEscape(strings.ReplaceAll(e, ":", ""))
				return nil
			}},
			"reaction/getAll": {path: "/posts/{postId}/reactions", post: mattermostLimit(false)},

			"user/create":     {run: mattermostUserCreate},
			"user/deactive":   {method: "DELETE", path: "/users/{userId}"},
			"user/getAll":     {run: mattermostUsers},
			"user/getByEmail": {path: "/users/email/{email}"},
			"user/getById":    {run: mattermostUsersByID},
			"user/invite": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				p, err := ec.restPath(i, "/teams/{teamId}/invite/email")
				if err != nil {
					return nil, err
				}
				emails, _ := ec.str(i, "emails", "")
				return mattermostRaw(ec, c, "POST", p, nil, strings.Split(emails, ","))
			}},
		},
	})
}

// mattermostRaw sends a JSON body that is not an object (an id list).
func mattermostRaw(ec *execCtx, c *restClient, method, path string, q map[string][]string, body interface{}) ([]Item, error) {
	v, err := c.do(ec, &restRequest{method: method, path: path, query: q, raw: []byte(mustJSON(body)), contentType: "application/json"})
	if err != nil {
		return nil, err
	}
	return restItems(v), nil
}

// mattermostLimit truncates a list to limit unless returnAll (reading the
// parameters of item 0 when first, as n8n does).
func mattermostLimit(needsReturnAll bool) func(ec *execCtx, i int, v interface{}) (interface{}, error) {
	return func(ec *execCtx, i int, v interface{}) (interface{}, error) {
		l, ok := v.([]interface{})
		if !ok {
			if v == nil {
				return []interface{}{}, nil
			}
			return v, nil
		}
		if needsReturnAll {
			if all, _ := ec.boolean(0, "returnAll", false); all {
				return l, nil
			}
		}
		n, _ := ec.num(0, "limit", 0)
		if !needsReturnAll && n <= 0 {
			return l, nil
		}
		if int(n) < len(l) {
			l = l[:int(n)]
		}
		return l, nil
	}
}

// mattermostPages fetches path page by page (100 per page) until an empty
// page, or once with per_page=limit; returnAll defaults to true.
func mattermostPages(ec *execCtx, i int, c *restClient, path string, q map[string][]string) ([]interface{}, error) {
	all, _ := ec.boolean(i, "returnAll", true)
	if !all {
		n, _ := ec.num(i, "limit", 100)
		q["per_page"] = []string{strconv.Itoa(int(n))}
		v, err := c.do(ec, &restRequest{method: "GET", path: path, query: q})
		if err != nil {
			return nil, err
		}
		return asList(v), nil
	}
	var out []interface{}
	q["per_page"] = []string{"100"}
	for page := 0; page < 1000; page++ {
		q["page"] = []string{strconv.Itoa(page)}
		v, err := c.do(ec, &restRequest{method: "GET", path: path, query: q})
		if err != nil {
			return nil, err
		}
		l := asList(v)
		if len(l) == 0 {
			break
		}
		out = append(out, l...)
	}
	return out, nil
}

func mattermostMembers(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	p, err := ec.restPath(i, "/channels/{channelId}/members")
	if err != nil {
		return nil, err
	}
	l, err := mattermostPages(ec, i, c, p, map[string][]string{})
	if err != nil {
		return nil, err
	}
	if all, _ := ec.boolean(i, "returnAll", true); !all {
		if n, _ := ec.num(i, "limit", 0); n > 0 && int(n) < len(l) {
			l = l[:int(n)]
		}
	}
	if resolve, _ := ec.boolean(i, "resolveData", true); resolve {
		var ids []string
		for _, m := range l {
			ids = append(ids, toString(asMap(m)["user_id"]))
		}
		if len(ids) > 0 {
			return mattermostRaw(ec, c, "POST", "/users/ids", nil, ids)
		}
	}
	return restItems(l), nil
}

func mattermostUsers(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	f := fieldsOf(ec, i, "additionalFields")
	q := map[string][]string{}
	for k, name := range map[string]string{"inTeam": "in_team", "notInTeam": "not_in_team", "inChannel": "in_channel", "notInChannel": "not_in_channel"} {
		if s := toString(f[k]); f[k] != nil && s != "" {
			q[name] = []string{s}
		}
	}
	if sort := toString(f["sort"]); f["sort"] != nil && sort != "" {
		sort = snakeCase(sort)
		_, inTeam := f["inTeam"]
		_, inChannel := f["inChannel"]
		switch {
		case !inTeam && !inChannel:
			return nil, permanentError{ec.node.Name + ": When sort is defined either 'in team' or 'in channel' must be defined"}
		case inTeam && sort != "last_activity_at" && sort != "created_at" && sort != "username":
			return nil, permanentError{ec.node.Name + ": When In Team is set the only valid values for sorting are last_activity_at,created_at,username"}
		case inChannel && sort != "status" && sort != "username":
			return nil, permanentError{ec.node.Name + ": When In Channel is set the only valid values for sorting are status,username"}
		case inChannel && toString(f["inChannel"]) == "" && sort != "username":
			return nil, permanentError{ec.node.Name + ": When sort is different than username In Channel must be set"}
		case inTeam && toString(f["inTeam"]) == "" && sort != "username":
			return nil, permanentError{ec.node.Name + ": When sort is different than username In Team must be set"}
		}
		if sort == "username" {
			sort = ""
		}
		q["sort"] = []string{sort}
	}
	l, err := mattermostPages(ec, i, c, "/users", q)
	if err != nil {
		return nil, err
	}
	return restItems(l), nil
}

func mattermostUsersByID(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	ids, _ := ec.str(i, "userIds", "")
	q := map[string][]string{}
	if s := fieldsOf(ec, i, "additionalFields")["since"]; s != nil && toString(s) != "" {
		ms, err := restConv("ms", s)
		if err != nil {
			return nil, permanentError{ec.node.Name + ": since: " + err.Error()}
		}
		q["since"] = []string{toString(ms)}
	}
	return mattermostRaw(ec, c, "POST", "/users/ids", q, strings.Split(ids, ","))
}

func mattermostUserCreate(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	body := map[string]interface{}{}
	for k, v := range fieldsOf(ec, i, "additionalFields") {
		body[k] = v
	}
	if ui, ok := body["notificationUi"]; ok {
		body["notify_props"] = asMap(ui)["notificationValues"]
	}
	auth, _ := ec.str(i, "authService", "")
	body["auth_service"] = auth
	body["username"], _ = ec.str(i, "username", "")
	if auth == "email" {
		body["email"], _ = ec.str(i, "email", "")
		body["password"], _ = ec.str(i, "password", "")
	} else {
		body["auth_data"], _ = ec.str(i, "authData", "")
	}
	v, err := c.do(ec, &restRequest{method: "POST", path: "/users", body: body})
	if err != nil {
		return nil, err
	}
	return restItems(v), nil
}

// mattermostAttachments turns the attachments parameter (fixedCollection
// shapes) into Mattermost message attachments under props.
func mattermostAttachments(ec *execCtx, i int, r *restRequest) error {
	raw, _ := ec.param(i, "attachments", nil)
	var atts []interface{}
	for _, x := range asList(raw) {
		a := map[string]interface{}{}
		for k, v := range asMap(x) {
			a[k] = v
		}
		if f, ok := a["fields"]; ok {
			if item, ok := asMap(f)["item"]; ok {
				a["fields"] = item
			} else {
				delete(a, "fields")
			}
		}
		if acts, ok := a["actions"]; ok {
			if item, ok := asMap(acts)["item"]; ok {
				var out []interface{}
				for _, y := range asList(item) {
					out = append(out, mattermostAction(asMap(y)))
				}
				a["actions"] = out
			} else {
				delete(a, "actions")
			}
		}
		atts = append(atts, a)
	}
	if atts == nil {
		atts = []interface{}{}
	}
	r.body["props"] = map[string]interface{}{"attachments": atts}
	return nil
}

func mattermostAction(in map[string]interface{}) map[string]interface{} {
	a := map[string]interface{}{}
	for k, v := range in {
		a[k] = v
	}
	if a["type"] == "button" {
		delete(a, "type")
	}
	if a["data_source"] == "custom" {
		delete(a, "data_source")
	}
	if o, ok := a["options"]; ok {
		a["options"] = asMap(o)["option"]
	}
	if integ, ok := asMap(a["integration"])["item"]; ok {
		it := map[string]interface{}{}
		for k, v := range asMap(integ) {
			it[k] = v
		}
		if props, ok := asMap(it["context"])["property"].([]interface{}); ok {
			ctx := map[string]interface{}{}
			for _, p := range props {
				pm := asMap(p)
				ctx[toString(pm["name"])] = pm["value"]
			}
			it["context"] = ctx
		}
		a["integration"] = it
	}
	return a
}
