package n8n

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Mailchimp (n8n-nodes-base.mailchimp), Marketing API 3.0. The data center
// is the API key's suffix (us1 in key-us1) or, for OAuth2, the
// api_endpoint of the login metadata.

func init() {
	registerREST("mailchimp", &restService{
		defResource: "member",
		defOps:      map[string]string{"member": "create", "memberTag": "create", "listGroup": "getAll", "campaign": "getAll"},
		client:      mailchimpClient,
		pager:       mailchimpPager,
		ops: map[string]*restOp{
			"member/create": {method: "POST", path: "/lists/{list}/members", build: mailchimpMember("options", true)},
			"member/update": {method: "PUT", path: "/lists/{list}/members/{email}", build: mailchimpMember("updateFields", false)},
			"member/get":    {path: "/lists/{list}/members/{email}", query: "@options fields excludeFields:exclude_fields"},
			"member/delete": {method: "POST", path: "/lists/{list}/members/{email}/actions/delete-permanent", success: true},
			"member/getAll": {path: "/lists/{list}/members", list: true, listKey: "members",
				query: "@options beforeLastChanged:before_last_changed beforeTimestampOpt:before_timestamp_opt emailType:email_type status sinceLastChanged:since_last_changed",
				build: mailchimpCount(500)},
			"memberTag/create": {method: "POST", path: "/lists/{list}/members/{email}/tags", success: true, build: mailchimpTags("active")},
			"memberTag/delete": {method: "POST", path: "/lists/{list}/members/{email}/tags", success: true, build: mailchimpTags("inactive")},
			"listGroup/getAll": {path: "/lists/{list}/interest-categories/{groupCategory}/interests", list: true, listKey: "interests", build: mailchimpCount(500)},
			"campaign/getAll": {path: "/campaigns", list: true, listKey: "campaigns",
				query: "@options status beforeCreateTime:before_create_time beforeSendTime:before_send_time listId:list_id sinceCreateTime:since_create_time " +
					"sinceSendTime:since_send_time sortDirection:sort_dir sortField:sort_field",
				build: func(ec *execCtx, i int, r *restRequest) error {
					o := fieldsOf(ec, i, "options")
					fields := "campaigns.id,campaigns.status,campaigns.tracking,campaigns.settings.from_name,campaigns.settings.title,campaigns.settings.reply_to"
					if f := asListOrOne(o["fields"]); !restEmpty(o["fields"]) {
						var parts []string
						for _, x := range f {
							if toString(x) == "*" {
								parts = []string{mailchimpCampaignFields}
								break
							}
							parts = append(parts, toString(x))
						}
						fields = strings.Join(parts, ",")
					}
					r.query.Set("fields", fields)
					return mailchimpCount(10)(ec, i, r)
				}},
			"campaign/get":       {path: "/campaigns/{campaignId}"},
			"campaign/delete":    {method: "DELETE", path: "/campaigns/{campaignId}", success: true},
			"campaign/send":      {method: "POST", path: "/campaigns/{campaignId}/actions/send", success: true},
			"campaign/replicate": {method: "POST", path: "/campaigns/{campaignId}/actions/replicate"},
			"campaign/resend":    {method: "POST", path: "/campaigns/{campaignId}/actions/create-resend"},
		},
	})
}

const mailchimpCampaignFields = "*,campaigns.id,campaigns.web_id,campaigns.type,campaigns.create_time,campaigns.archive_url,campaigns.long_archive_url,campaigns.status," +
	"campaigns.emails_sent,campaigns.send_time,campaigns.content_type,campaigns.needs_block_refresh,campaigns.resendable,campaigns.recipients,campaigns.recipients.list_id," +
	"campaigns.recipients.list_is_active,campaigns.recipients.list_name,campaigns.recipients.segment_text,campaigns.recipients.recipient_count,campaigns.settings," +
	"campaigns.settings.subject_line,campaigns.settings.preview_text,campaigns.settings.title,campaigns.settings.from_name,campaigns.settings.reply_to," +
	"campaigns.settings.use_conversation,campaigns.settings.to_name,campaigns.settings.folder_id,campaigns.settings.authenticate,campaigns.settings.auto_footer," +
	"campaigns.settings.inline_css,campaigns.settings.auto_tweet,campaigns.settings.fb_comments,campaigns.settings.timewarp,campaigns.settings.template_id," +
	"campaigns.settings.drag_and_drop,campaigns.tracking,campaigns.tracking.opens,campaigns.tracking.html_clicks,campaigns.tracking.text_clicks," +
	"campaigns.tracking.goal_tracking,campaigns.tracking.ecomm360,campaigns.tracking.google_analytics,campaigns.tracking.clicktale,campaigns.report_summary," +
	"campaigns.report_summary.opens,campaigns.report_summary.unique_opens,campaigns.report_summary.open_rate,campaigns.report_summary.clicks," +
	"campaigns.report_summary.subscriber_clicks,campaigns.report_summary.click_rate,campaigns.report_summary.click_rate.ecommerce," +
	"campaigns.report_summary.click_rate.ecommerce.total_orders,campaigns.report_summary.click_rate.ecommerce.total_spent," +
	"campaigns.report_summary.click_rate.ecommerce.total_revenue,campaigns.report_summary.delivery_status.enabled,campaigns._links"

func mailchimpClient(ec *execCtx, i int) (*restClient, error) {
	c, t, err := ec.restCred(i, "mailchimpApi", map[string]string{"apiKey": "mailchimpApi", "oAuth2": "mailchimpOAuth2Api"})
	if err != nil {
		return nil, err
	}
	if t == "mailchimpApi" {
		key := credString(c, "apiKey")
		if !strings.Contains(key, "-") {
			return nil, permanentError{ec.node.Name + ": The API key is not valid!"}
		}
		dc := key[strings.LastIndex(key, "-")+1:]
		base := ec.apiBase("mailchimp", "https://"+dc+".api.mailchimp.com") + "/3.0"
		return &restClient{base: base, headers: map[string]string{"Authorization": "apikey " + key}}, nil
	}
	tok := accessToken(c)
	endpoint := ec.apiBase("mailchimp", "")
	if endpoint == "" {
		endpoint = credString(c, "api_endpoint")
	}
	if endpoint == "" {
		meta := firstNonEmpty(credString(c, "metadataUrl"), "https://login.mailchimp.com/oauth2/metadata")
		res, _, err := ec.apiCall("GET", meta, map[string]string{"Accept": "application/json", "Authorization": "OAuth " + tok}, nil, "")
		if err != nil {
			return nil, err
		}
		endpoint = toString(res["api_endpoint"])
		if endpoint == "" || endpoint == "<nil>" {
			return nil, permanentError{ec.node.Name + ": Mailchimp metadata has no api_endpoint"}
		}
	}
	return &restClient{base: strings.TrimRight(endpoint, "/") + "/3.0", headers: map[string]string{"Authorization": "Bearer " + tok}}, nil
}

// mailchimpCount sets count=limit, or offset/count=500 pages with returnAll.
func mailchimpCount(def int) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		restDefaultLimit(ec, def)
		if all, _ := ec.boolean(i, "returnAll", false); all {
			r.query.Set("offset", "0")
			r.query.Set("count", "500")
			return nil
		}
		n, _ := ec.num(i, "limit", float64(def))
		r.query.Set("count", strconv.Itoa(int(n)))
		return nil
	}
}

// mailchimpPager advances offset until an empty page (returnAll only).
func mailchimpPager(p *restPage, _ interface{}, _ http.Header) bool {
	if p.query.Get("offset") == "" {
		return false
	}
	p.query.Set("offset", strconv.Itoa(p.got))
	return true
}

// mailchimpMember builds the member body (create: options, update: updateFields).
func mailchimpMember(coll string, create bool) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		list, _ := ec.param(i, "list", "")
		email, _ := ec.str(i, "email", "")
		r.body["listId"], r.body["email_address"] = rlValue(list), email
		if create {
			status, _ := ec.str(i, "status", "")
			r.body["status"] = status
		}
		f := fieldsOf(ec, i, coll)
		for k, dst := range map[string]string{"status": "status", "emailType": "email_type", "language": "language", "vip": "vip", "ipSignup": "ip_signup", "ipOptIn": "ip_opt"} {
			if k == "status" && create {
				continue
			}
			if v := f[k]; !restEmpty(v) && v != false {
				r.body[dst] = v
			}
		}
		for k, dst := range map[string]string{"timestampOpt": "timestamp_opt", "timestampSignup": "timestamp_signup"} {
			if v := f[k]; !restEmpty(v) {
				r.body[dst] = mailchimpTimestamp(v)
			}
		}
		if create && !restEmpty(f["tags"]) {
			var tags []interface{}
			for _, t := range strings.Split(toString(f["tags"]), ",") {
				tags = append(tags, t)
			}
			r.body["tags"] = tags
		}
		if !create && toBool(f["skipMergeValidation"]) {
			r.query.Set("skip_merge_validation", "true")
		}
		if js, _ := ec.boolean(i, "jsonParameters", false); js {
			for p, dst := range map[string]string{"locationJson": "location", "mergeFieldsJson": "merge_fields", "groupJson": "interests"} {
				s, _ := ec.str(i, p, "")
				if v, err := restConv("json", s); err == nil && !restEmpty(v) {
					r.body[dst] = v
				}
			}
			return nil
		}
		src := func(name string) interface{} {
			if create {
				v, _ := ec.param(i, name, nil)
				return v
			}
			return f[name]
		}
		if loc := asMap(asMap(src("locationFieldsUi"))["locationFieldsValues"]); loc != nil {
			l := map[string]interface{}{}
			for _, k := range []string{"latitude", "longitude"} {
				if v, ok := loc[k]; ok {
					n, _ := strconv.ParseFloat(strings.TrimSpace(toString(v)), 64)
					l[k] = n
				}
			}
			r.body["location"] = l
		}
		if mf := asMap(src("mergeFieldsUi"))["mergeFieldsValues"]; mf != nil {
			m := map[string]interface{}{}
			for _, x := range asList(mf) {
				m[toString(asMap(x)["name"])] = asMap(x)["value"]
			}
			r.body["merge_fields"] = m
		}
		if gv := asMap(src("groupsUi"))["groupsValues"]; gv != nil {
			m := map[string]interface{}{}
			for _, x := range asList(gv) {
				m[toString(asMap(x)["categoryFieldId"])] = asMap(x)["value"]
			}
			r.body["interests"] = m
		}
		return nil
	}
}

// mailchimpTimestamp mirrors n8n's moment format 'YYYY-MM-DD HH:MM:SS'
// (MM is the month and SS centiseconds there).
func mailchimpTimestamp(v interface{}) interface{} {
	t, ok := timeOf(v)
	if !ok {
		return v
	}
	return fmt.Sprintf("%s %02d:%02d:%02d", t.Format("2006-01-02"), t.Hour(), int(t.Month()), t.Nanosecond()/10000000)
}

func mailchimpTags(status string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		v, err := ec.param(i, "tags", nil)
		if err != nil {
			return err
		}
		tags := []interface{}{}
		for _, t := range asListOrOne(v) {
			if s := toString(t); s != "" {
				tags = append(tags, map[string]interface{}{"name": s, "status": status})
			}
		}
		r.body["tags"] = tags
		if toBool(fieldsOf(ec, i, "options")["isSyncing"]) {
			r.body["is_syncing"] = true
		}
		return nil
	}
}
