package n8n

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/models"
)

// Webhook registration: like n8n on workflow activation, create the
// webhook in the service (and keep its id and signing secret) so events
// reach /webhook/{webhookId}.

// hookRegistration is the context of one registration.
type hookRegistration struct {
	ec  *execCtx
	url string // public URL of the trigger's endpoint
}

type hookRegistrar struct {
	create func(r *hookRegistration) (map[string]interface{}, error)
	delete func(r *hookRegistration, state map[string]interface{}) error
}

func (r *hookRegistration) str(name, def string) string {
	s, err := r.ec.str(0, name, def)
	if err != nil {
		return def
	}
	return s
}

func (r *hookRegistration) rl(name string) string {
	v, _ := r.ec.param(0, name, "")
	return rlValue(v)
}

func (r *hookRegistration) list(name string) []string {
	v, _ := r.ec.param(0, name, nil)
	return listOrCSV(v)
}

func (r *hookRegistration) call(method, u string, headers map[string]string, body interface{}, ct string) (map[string]interface{}, error) {
	out, _, err := r.ec.apiCall(method, u, headers, body, ct)
	return out, err
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func bearer(t string) map[string]string { return map[string]string{"Authorization": "Bearer " + t} }

// tokenOf returns the token of the first credential type that resolves.
func (r *hookRegistration) tokenOf(types ...string) (string, map[string]interface{}, error) {
	var lastErr error
	for _, t := range types {
		c, err := r.ec.credential(t)
		if err != nil {
			lastErr = err
			continue
		}
		return firstNonEmpty(accessToken(c), credString(c, "accessToken", "apiKey", "apiToken", "token")), c, nil
	}
	return "", nil, lastErr
}

var hookRegistrars = map[string]hookRegistrar{
	hookBase + "githubTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			if strings.Contains(r.url, "//localhost") {
				return nil, permanentError{"GitHub does not accept localhost webhook URLs"}
			}
			tok, c, err := r.tokenOf("githubApi", "githubOAuth2Api")
			if err != nil {
				return nil, err
			}
			base := strings.TrimRight(firstNonEmpty(credString(c, "server"), r.ec.apiBase("github", "https://api.github.com")), "/")
			secret := randomHex(32)
			events := r.list("events")
			insecure := "0"
			if toBool(asMap(r.param("options"))["insecureSSL"]) {
				insecure = "1"
			}
			out, err := r.call("POST", base+"/repos/"+url.PathEscape(r.rl("owner"))+"/"+url.PathEscape(r.rl("repository"))+"/hooks", map[string]string{"Authorization": "token " + tok},
				map[string]interface{}{"name": "web", "active": true, "events": events, "config": map[string]interface{}{"url": r.url, "content_type": "json", "insecure_ssl": insecure, "secret": secret}}, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": out["id"], "webhookSecret": secret, "base": base, "repo": r.rl("owner") + "/" + r.rl("repository")}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			tok, _, err := r.tokenOf("githubApi", "githubOAuth2Api")
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", toString(s["base"])+"/repos/"+toString(s["repo"])+"/hooks/"+toString(s["webhookId"]), map[string]string{"Authorization": "token " + tok}, nil, "")
			return err
		},
	},
	hookBase + "stripeTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			c, err := r.ec.credential("stripeApi")
			if err != nil {
				return nil, err
			}
			form := url.Values{"url": {r.url}, "description": {"Created by flow for an imported n8n workflow"}}
			events := r.list("events")
			if len(events) == 0 {
				events = []string{"*"}
			}
			for _, e := range events {
				form.Add("enabled_events[]", e)
			}
			if v := r.str("apiVersion", ""); v != "" {
				form.Set("api_version", v)
			}
			out, err := r.call("POST", r.ec.apiBase("stripe", "https://api.stripe.com")+"/v1/webhook_endpoints", bearer(credString(c, "secretKey")), []byte(form.Encode()), "application/x-www-form-urlencoded")
			if err != nil {
				return nil, err
			}
			if out["id"] == nil || out["secret"] == nil {
				return nil, permanentError{"Stripe did not return a webhook id and secret"}
			}
			return map[string]interface{}{"webhookId": out["id"], "webhookSecret": out["secret"]}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			c, err := r.ec.credential("stripeApi")
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", r.ec.apiBase("stripe", "https://api.stripe.com")+"/v1/webhook_endpoints/"+url.PathEscape(toString(s["webhookId"])), bearer(credString(c, "secretKey")), nil, "")
			return err
		},
	},
	hookBase + "typeformTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			tok, _, err := r.tokenOf("typeformApi", "typeformOAuth2Api")
			if err != nil {
				return nil, err
			}
			tag, secret := "n8n-"+randomHex(5), randomHex(32)
			base := r.ec.apiBase("typeform", "https://api.typeform.com")
			form := r.rl("formId")
			if _, err := r.call("PUT", base+"/forms/"+url.PathEscape(form)+"/webhooks/"+tag, bearer(tok),
				map[string]interface{}{"url": r.url, "enabled": true, "verify_ssl": true, "secret": secret}, ""); err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": tag, "webhookSecret": secret, "form": form}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			tok, _, err := r.tokenOf("typeformApi", "typeformOAuth2Api")
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", r.ec.apiBase("typeform", "https://api.typeform.com")+"/forms/"+url.PathEscape(toString(s["form"]))+"/webhooks/"+toString(s["webhookId"]), bearer(tok), nil, "")
			return err
		},
	},
	hookBase + "calendlyTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			tok, _, err := r.tokenOf("calendlyApi", "calendlyOAuth2Api")
			if err != nil {
				return nil, err
			}
			base := r.ec.apiBase("calendly", "https://api.calendly.com")
			me, err := r.call("GET", base+"/users/me", bearer(tok), nil, "")
			if err != nil {
				return nil, err
			}
			res := asMap(me["resource"])
			secret := randomHex(32)
			scope := r.str("scope", "user")
			body := map[string]interface{}{"url": r.url, "events": r.list("events"), "organization": res["current_organization"], "scope": scope, "signing_key": secret}
			if scope == "user" {
				body["user"] = res["uri"]
			}
			out, err := r.call("POST", base+"/webhook_subscriptions", bearer(tok), body, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookURI": asMap(out["resource"])["uri"], "webhookSecret": secret}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			tok, _, err := r.tokenOf("calendlyApi", "calendlyOAuth2Api")
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", toString(s["webhookURI"]), bearer(tok), nil, "")
			return err
		},
	},
	hookBase + "gitlabTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			tok, c, err := r.tokenOf("gitlabApi", "gitlabOAuth2Api")
			if err != nil {
				return nil, err
			}
			base := strings.TrimRight(firstNonEmpty(credString(c, "server"), "https://gitlab.com"), "/") + "/api/v4"
			project := url.PathEscape(r.rl("owner") + "/" + r.rl("repository"))
			secret := randomHex(32)
			body := map[string]interface{}{"url": r.url, "enable_ssl_verification": false, "token": secret, "push_events": false}
			for _, e := range r.list("events") {
				if e == "*" {
					for _, x := range []string{"note", "confidential_issues", "confidential_note", "deployment", "issues", "job", "merge_requests", "pipeline", "push", "releases", "tag_push", "wiki_page"} {
						body[x+"_events"] = true
					}
					continue
				}
				body[e+"_events"] = true
			}
			out, err := r.call("POST", base+"/projects/"+project+"/hooks", bearer(tok), body, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": out["id"], "webhookSecret": secret, "base": base, "project": project}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			tok, _, err := r.tokenOf("gitlabApi", "gitlabOAuth2Api")
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", toString(s["base"])+"/projects/"+toString(s["project"])+"/hooks/"+toString(s["webhookId"]), bearer(tok), nil, "")
			return err
		},
	},
	hookBase + "clickUpTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			tok, _, err := r.tokenOf("clickUpApi", "clickUpOAuth2Api")
			if err != nil {
				return nil, err
			}
			var events interface{} = r.list("events")
			for _, e := range r.list("events") {
				if e == "*" {
					events = "*"
				}
			}
			body := map[string]interface{}{"endpoint": r.url, "events": events}
			f := asMap(r.param("filters"))
			for k, field := range map[string]string{"listId": "list_id", "taskId": "task_id", "spaceId": "space_id", "folderId": "folder_id"} {
				if v := strings.TrimPrefix(toString(f[k]), "#"); v != "" {
					body[field] = v
				}
			}
			out, err := r.call("POST", r.ec.apiBase("clickup", "https://api.clickup.com/api/v2")+"/team/"+url.PathEscape(r.str("team", ""))+"/webhook", map[string]string{"Authorization": tok}, body, "")
			if err != nil {
				return nil, err
			}
			wh := asMap(out["webhook"])
			return map[string]interface{}{"webhookId": firstNonEmpty(toString(out["id"]), toString(wh["id"])), "secret": wh["secret"]}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			tok, _, err := r.tokenOf("clickUpApi", "clickUpOAuth2Api")
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", r.ec.apiBase("clickup", "https://api.clickup.com/api/v2")+"/webhook/"+url.PathEscape(toString(s["webhookId"])), map[string]string{"Authorization": tok}, nil, "")
			return err
		},
	},
	hookBase + "wooCommerceTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			c, err := r.ec.credential("wooCommerceApi")
			if err != nil {
				return nil, err
			}
			key, sec := credString(c, "consumerKey"), credString(c, "consumerSecret")
			sum := md5.Sum([]byte(key + "," + sec))
			secret := hex.EncodeToString(sum[:])
			out, err := r.call("POST", strings.TrimRight(credString(c, "url"), "/")+"/wp-json/wc/v3/webhooks", basicAuthHeader(key, sec),
				map[string]interface{}{"delivery_url": r.url, "topic": r.str("event", ""), "secret": secret}, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": out["id"], "secret": secret}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			c, err := r.ec.credential("wooCommerceApi")
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", strings.TrimRight(credString(c, "url"), "/")+"/wp-json/wc/v3/webhooks/"+toString(s["webhookId"])+"?force=true",
				basicAuthHeader(credString(c, "consumerKey"), credString(c, "consumerSecret")), nil, "")
			return err
		},
	},
	hookBase + "shopifyTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			base, h, err := shopifyAdmin(r)
			if err != nil {
				return nil, err
			}
			out, err := r.call("POST", base+"/webhooks.json", h, map[string]interface{}{"webhook": map[string]interface{}{"topic": r.str("topic", ""), "address": r.url, "format": "json"}}, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": asMap(out["webhook"])["id"]}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			base, h, err := shopifyAdmin(r)
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", base+"/webhooks/"+toString(s["webhookId"])+".json", h, nil, "")
			return err
		},
	},
	hookBase + "pipedriveTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			u, h, err := pipedriveAPI(r)
			if err != nil {
				return nil, err
			}
			object := r.str("entity", r.str("object", "*"))
			body := map[string]interface{}{"event_action": r.str("action", "*"), "event_object": object, "subscription_url": r.url}
			if r.str("incomingAuthentication", "none") == "basicAuth" {
				c, err := r.ec.credential("httpBasicAuth")
				if err != nil {
					return nil, err
				}
				body["http_auth_user"], body["http_auth_password"] = credString(c, "user"), credString(c, "password")
			}
			out, err := r.call("POST", u("/webhooks"), h, body, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": asMap(out["data"])["id"]}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			u, h, err := pipedriveAPI(r)
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", u("/webhooks/"+toString(s["webhookId"])), h, nil, "")
			return err
		},
	},
	hookBase + "linearTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			tok, _, err := r.tokenOf("linearApi", "linearOAuth2Api")
			if err != nil {
				return nil, err
			}
			var types []string
			for _, t := range r.list("resources") {
				if t != "" {
					types = append(types, strings.ToUpper(t[:1])+t[1:])
				}
			}
			out, err := r.call("POST", r.ec.apiBase("linear", "https://api.linear.app")+"/graphql", map[string]string{"Authorization": tok},
				map[string]interface{}{"query": `mutation webhookCreate($url: String!, $teamId: String!, $resources: [String!]!) { webhookCreate(input: {url: $url, teamId: $teamId, resourceTypes: $resources}) { success webhook { id enabled } } }`,
					"variables": map[string]interface{}{"url": r.url, "teamId": r.str("teamId", ""), "resources": types}}, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": asMap(asMap(asMap(out["data"])["webhookCreate"])["webhook"])["id"]}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			tok, _, err := r.tokenOf("linearApi", "linearOAuth2Api")
			if err != nil {
				return err
			}
			_, err = r.call("POST", r.ec.apiBase("linear", "https://api.linear.app")+"/graphql", map[string]string{"Authorization": tok},
				map[string]interface{}{"query": `mutation webhookDelete($id: String!) { webhookDelete(id: $id) { success } }`, "variables": map[string]interface{}{"id": s["webhookId"]}}, "")
			return err
		},
	},
	hookBase + "gumroadTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			tok, _, err := r.tokenOf("gumroadApi", "gumroadOAuth2Api")
			if err != nil {
				return nil, err
			}
			form := url.Values{"post_url": {r.url}, "resource_name": {r.str("resource", "")}, "access_token": {tok}}
			out, err := r.call("PUT", r.ec.apiBase("gumroad", "https://api.gumroad.com")+"/v2/resource_subscriptions", nil, []byte(form.Encode()), "application/x-www-form-urlencoded")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": asMap(out["resource_subscription"])["id"]}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			tok, _, err := r.tokenOf("gumroadApi", "gumroadOAuth2Api")
			if err != nil {
				return err
			}
			_, err = r.call("DELETE", r.ec.apiBase("gumroad", "https://api.gumroad.com")+"/v2/resource_subscriptions/"+url.PathEscape(toString(s["webhookId"]))+"?"+url.Values{"access_token": {tok}}.Encode(), nil, nil, "")
			return err
		},
	},
	hookBase + "calTrigger": {
		create: func(r *hookRegistration) (map[string]interface{}, error) {
			c, err := r.ec.credential("calApi")
			if err != nil {
				return nil, err
			}
			base := strings.TrimRight(firstNonEmpty(credString(c, "host"), "https://api.cal.com"), "/")
			secret := randomHex(32)
			opts := asMap(r.param("options"))
			if r.ec.version >= 3 {
				path := "/v2/webhooks"
				if id := toString(opts["eventTypeId"]); id != "" {
					path = "/v2/event-types/" + url.PathEscape(id) + "/webhooks"
				}
				body := map[string]interface{}{"subscriberUrl": r.url, "triggers": r.list("events"), "active": true, "secret": secret}
				if t := opts["payloadTemplate"]; t != nil {
					body["payloadTemplate"] = t
				}
				out, err := r.call("POST", base+path, bearer(credString(c, "apiKey")), body, "")
				if err != nil {
					return nil, err
				}
				return map[string]interface{}{"webhookId": asMap(out["data"])["id"], "webhookSecret": secret, "path": path, "base": base}, nil
			}
			body := map[string]interface{}{"subscriberUrl": r.url, "eventTriggers": r.list("events"), "active": true, "secret": secret}
			for k, v := range opts {
				body[k] = v
			}
			out, err := r.call("POST", base+"/v1/hooks?"+url.Values{"apiKey": {credString(c, "apiKey")}}.Encode(), nil, body, "")
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{"webhookId": asMap(out["webhook"])["id"], "webhookSecret": secret, "path": "/v1/hooks", "base": base}, nil
		},
		delete: func(r *hookRegistration, s map[string]interface{}) error {
			c, err := r.ec.credential("calApi")
			if err != nil {
				return err
			}
			u := toString(s["base"]) + toString(s["path"]) + "/" + toString(s["webhookId"])
			if toString(s["path"]) == "/v1/hooks" {
				_, err = r.call("DELETE", u+"?"+url.Values{"apiKey": {credString(c, "apiKey")}}.Encode(), nil, nil, "")
			} else {
				_, err = r.call("DELETE", u, bearer(credString(c, "apiKey")), nil, "")
			}
			return err
		},
	},
}

func (r *hookRegistration) param(name string) interface{} {
	v, _ := r.ec.param(0, name, nil)
	return v
}

func basicAuthHeader(user, pass string) map[string]string {
	return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))}
}

func shopifyAdmin(r *hookRegistration) (string, map[string]string, error) {
	switch r.str("authentication", "apiKey") {
	case "accessToken":
		c, err := r.ec.credential("shopifyAccessTokenApi")
		if err != nil {
			return "", nil, err
		}
		return "https://" + credString(c, "shopSubdomain") + ".myshopify.com/admin/api/2024-07", map[string]string{"X-Shopify-Access-Token": credString(c, "accessToken")}, nil
	case "oAuth2":
		c, err := r.ec.credential("shopifyOAuth2Api")
		if err != nil {
			return "", nil, err
		}
		return "https://" + credString(c, "shopSubdomain") + ".myshopify.com/admin/api/2024-07", map[string]string{"X-Shopify-Access-Token": accessToken(c)}, nil
	}
	c, err := r.ec.credential("shopifyApi")
	if err != nil {
		return "", nil, err
	}
	return "https://" + credString(c, "shopSubdomain") + ".myshopify.com/admin/api/2024-07", basicAuthHeader(credString(c, "apiKey"), credString(c, "password")), nil
}

func pipedriveAPI(r *hookRegistration) (func(string) string, map[string]string, error) {
	base := r.ec.apiBase("pipedrive", "https://api.pipedrive.com/v1")
	if r.str("authentication", "apiToken") == "oAuth2" {
		tok, _, err := r.tokenOf("pipedriveOAuth2Api")
		if err != nil {
			return nil, nil, err
		}
		return func(p string) string { return base + p }, bearer(tok), nil
	}
	c, err := r.ec.credential("pipedriveApi")
	if err != nil {
		return nil, nil, err
	}
	tok := credString(c, "apiToken")
	return func(p string) string { return base + p + "?" + url.Values{"api_token": {tok}}.Encode() }, nil, nil
}

// WebhookRegistration reports the outcome for one service trigger.
type WebhookRegistration struct {
	Node string `json:"node"`
	Type string `json:"type"`
	URL  string `json:"url"`
	// Manual is set when the service has no API for it: the URL must be
	// entered in the service's settings (e.g. a Slack app's Event
	// Subscriptions).
	Manual bool   `json:"manual,omitempty"`
	Error  string `json:"error,omitempty"`
}

func hookContext(ctx context.Context, opts *Options, wf *models.Workflow, n *models.Node) *execCtx {
	return &execCtx{ctx: ctx, opts: opts, node: n, params: nodeParams(n), version: toFloatDefault(n.Config[cfgVersion], 1), data: &execData{wf: wf}}
}

// RegisterWebhooks creates the webhooks of a workflow's service triggers
// in their services, pointing at baseURL + "/webhook/{webhookId}" (baseURL
// is where the n8n handler is publicly mounted). Registration data
// (webhook ids, signing secrets) is kept in Options.PollState.
func RegisterWebhooks(ctx context.Context, opts *Options, namespace string, wf *models.Workflow, baseURL string) []WebhookRegistration {
	if opts == nil {
		opts = &Options{}
	}
	ctx = engine.WithExecutionContext(ctx, namespace, "")
	var out []WebhookRegistration
	for i := range wf.Nodes {
		n := &wf.Nodes[i]
		if serviceHooks[n.Type].handle == nil {
			continue
		}
		res := WebhookRegistration{Node: n.Name, Type: n.Type, URL: strings.TrimRight(baseURL, "/") + "/webhook/" + serviceHookPath(n)}
		reg, has := hookRegistrars[n.Type]
		if !has {
			res.Manual = true
			out = append(out, res)
			continue
		}
		ec := hookContext(ctx, opts, wf, n)
		state, err := reg.create(&hookRegistration{ec: ec, url: res.URL})
		if err == nil {
			b, _ := json.Marshal(state)
			err = ec.pollStore().SetPollState(ctx, hookStateKey(namespace, wf.ID, n.ID), string(b))
		}
		if err != nil {
			res.Error = redactSecrets(err.Error())
		}
		out = append(out, res)
	}
	return out
}

// UnregisterWebhooks deletes the webhooks RegisterWebhooks created.
func UnregisterWebhooks(ctx context.Context, opts *Options, namespace string, wf *models.Workflow) error {
	if opts == nil {
		opts = &Options{}
	}
	ctx = engine.WithExecutionContext(ctx, namespace, "")
	var errs []string
	for i := range wf.Nodes {
		n := &wf.Nodes[i]
		reg, has := hookRegistrars[n.Type]
		if !has {
			continue
		}
		ec := hookContext(ctx, opts, wf, n)
		key := hookStateKey(namespace, wf.ID, n.ID)
		raw, seen, err := ec.pollStore().GetPollState(ctx, key)
		if err != nil || !seen || raw == "" {
			continue
		}
		var state map[string]interface{}
		_ = json.Unmarshal([]byte(raw), &state)
		if err := reg.delete(&hookRegistration{ec: ec}, state); err != nil {
			errs = append(errs, n.Name+": "+redactSecrets(err.Error()))
			continue
		}
		_ = ec.pollStore().SetPollState(ctx, key, "")
	}
	if len(errs) > 0 {
		return fmt.Errorf("unregister webhooks: %s", strings.Join(errs, "; "))
	}
	return nil
}
