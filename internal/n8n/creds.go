package n8n

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/dgmos/flow/internal/engine"
)

// credential loads the node's credential of the given type.
func (ec *execCtx) credential(credType string) (map[string]interface{}, error) {
	refs, _ := ec.node.Config[cfgCredentials].(map[string]interface{})
	raw, ok := refs[credType]
	if !ok {
		// Accept a variant of the same service (e.g. notionOAuth2Api for
		// notionApi), never an unrelated credential.
		for k, v := range refs {
			if credService(k) == credService(credType) {
				credType, raw, ok = k, v, true
				break
			}
		}
		if !ok {
			return nil, permanentError{fmt.Sprintf("node %q has no %s credential", ec.node.Name, credType)}
		}
	}
	ref := CredentialRef{Node: ec.node.Name}
	if ec.data != nil && ec.data.wf != nil {
		ref.WorkflowID = ec.data.wf.ID
	}
	if m, ok := raw.(map[string]interface{}); ok {
		ref.ID, _ = m["id"].(string)
		ref.Name, _ = m["name"].(string)
	}
	if ec.opts == nil || ec.opts.Credentials == nil {
		return nil, permanentError{fmt.Sprintf("credential %s %q is required but no credential provider is configured", credType, ref.Name)}
	}
	c, err := ec.opts.Credentials.Credential(ec.ctx, engine.NamespaceFromContext(ec.ctx), credType, ref)
	if err != nil {
		return nil, permanentError{err.Error()}
	}
	return c, nil
}

func credString(c map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := c[k]; ok && toString(v) != "" {
			return toString(v)
		}
	}
	return ""
}

// accessToken extracts an OAuth access token in n8n's or a flat format.
func accessToken(c map[string]interface{}) string {
	if t := credString(c, "accessToken", "access_token", "token"); t != "" {
		return t
	}
	if td, ok := c["oauthTokenData"].(map[string]interface{}); ok {
		return credString(td, "access_token", "accessToken")
	}
	return ""
}

// applyCredential authenticates a request with an n8n credential.
func applyCredential(req *http.Request, credType string, c map[string]interface{}) error {
	// Host-provided raw headers/query always win.
	if h, ok := c["__headers"].(map[string]interface{}); ok {
		for k, v := range h {
			req.Header.Set(k, toString(v))
		}
	}
	if q, ok := c["__query"].(map[string]interface{}); ok {
		qs := req.URL.Query()
		for k, v := range q {
			qs.Set(k, toString(v))
		}
		req.URL.RawQuery = qs.Encode()
	}
	if _, ok := c["__headers"]; ok {
		return nil
	}
	if _, ok := c["__query"]; ok {
		return nil
	}

	bearer := func(t string) { req.Header.Set("Authorization", "Bearer "+t) }
	query := func(k, v string) {
		qs := req.URL.Query()
		qs.Set(k, v)
		req.URL.RawQuery = qs.Encode()
	}
	switch credType {
	case "httpBasicAuth":
		req.SetBasicAuth(credString(c, "user", "username"), credString(c, "password"))
	case "httpHeaderAuth":
		req.Header.Set(credString(c, "name"), credString(c, "value"))
	case "httpQueryAuth":
		query(credString(c, "name"), credString(c, "value"))
	case "httpBearerAuth":
		bearer(credString(c, "token", "accessToken"))
	case "httpCustomAuth":
		var spec struct {
			Headers map[string]interface{} `json:"headers"`
			QS      map[string]interface{} `json:"qs"`
		}
		if err := json.Unmarshal([]byte(credString(c, "json")), &spec); err != nil {
			return fmt.Errorf("httpCustomAuth: invalid JSON: %w", err)
		}
		for k, v := range spec.Headers {
			req.Header.Set(k, toString(v))
		}
		for k, v := range spec.QS {
			query(k, toString(v))
		}
	case "anthropicApi":
		req.Header.Set("x-api-key", credString(c, "apiKey"))
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case "googlePalmApi", "googleGeminiApi":
		req.Header.Set("x-goog-api-key", credString(c, "apiKey"))
	case "notionApi":
		bearer(credString(c, "apiKey", "accessToken"))
		if req.Header.Get("Notion-Version") == "" {
			req.Header.Set("Notion-Version", "2022-06-28")
		}
	case "serpApi":
		query("api_key", credString(c, "apiKey"))
	case "supabaseApi":
		key := credString(c, "serviceRole", "apiKey")
		req.Header.Set("apikey", key)
		bearer(key)
	case "pineconeApi":
		req.Header.Set("Api-Key", credString(c, "apiKey"))
	case "qdrantApi":
		req.Header.Set("api-key", credString(c, "apiKey"))
	case "elevenLabsApi":
		req.Header.Set("xi-api-key", credString(c, "apiKey", "xiApiKey"))
	case "githubApi":
		req.Header.Set("Authorization", "token "+credString(c, "accessToken"))
	case "telegramApi":
		// Telegram puts the token in the URL path; nodes handle it.
	default:
		switch {
		case strings.Contains(strings.ToLower(credType), "oauth2"):
			t := accessToken(c)
			if t == "" {
				return fmt.Errorf("%s credential has no access token", credType)
			}
			bearer(t)
		case accessToken(c) != "":
			bearer(accessToken(c))
		case credString(c, "apiKey", "apiToken", "appToken") != "":
			bearer(credString(c, "apiKey", "apiToken", "appToken"))
		case credString(c, "user", "username") != "":
			req.SetBasicAuth(credString(c, "user", "username"), credString(c, "password"))
		default:
			return fmt.Errorf("don't know how to apply credential type %q (provide __headers or __query in the credential)", credType)
		}
	}
	return nil
}

// credBaseURL returns a credential's base URL override, if any.
func credBaseURL(c map[string]interface{}, def string) string {
	if u := credString(c, "url", "baseUrl", "baseURL", "host"); u != "" {
		if !strings.HasPrefix(u, "http") {
			u = "https://" + u
		}
		return strings.TrimRight(u, "/")
	}
	return def
}

// credService strips auth-kind suffixes: "notionOAuth2Api" -> "notion".
func credService(t string) string {
	t = strings.ToLower(t)
	for _, suf := range []string{"oauth2api", "oauth2", "api", "token"} {
		t = strings.TrimSuffix(t, suf)
	}
	return t
}

// knownCredentialDomains lists where well-known credentials may be sent by
// generic nodes (HTTP Request with a predefined credential type). A
// credential's own url/baseUrl and allowedDomains are honored too.
var knownCredentialDomains = map[string][]string{
	"openai": {"api.openai.com"}, "anthropic": {"api.anthropic.com"}, "googlepalm": {"generativelanguage.googleapis.com"},
	"notion": {"api.notion.com"}, "slack": {"slack.com"}, "telegram": {"api.telegram.org"}, "airtable": {"api.airtable.com"},
	"github": {"api.github.com"}, "serp": {"serpapi.com"}, "openrouter": {"openrouter.ai"}, "hubspot": {"api.hubapi.com"},
	"googlesheets": {"googleapis.com"}, "gmail": {"googleapis.com"}, "googledrive": {"googleapis.com"}, "googlecalendar": {"googleapis.com"},
	"googledocs": {"googleapis.com"}, "google": {"googleapis.com"}, "googleapi": {"googleapis.com"}, "youtube": {"googleapis.com"},
	"twitter": {"api.twitter.com", "api.x.com"}, "linkedin": {"api.linkedin.com"}, "microsoftoutlook": {"graph.microsoft.com"},
	"microsoft": {"graph.microsoft.com"}, "discordbot": {"discord.com"}, "whatsapp": {"graph.facebook.com"}, "facebookgraph": {"graph.facebook.com"},
	"elevenlabs": {"api.elevenlabs.io"}, "mistralcloud": {"api.mistral.ai"}, "groq": {"api.groq.com"}, "deepseek": {"api.deepseek.com"},
	"perplexity": {"api.perplexity.ai"}, "cohere": {"api.cohere.ai", "api.cohere.com"}, "huggingface": {"huggingface.co"},
	"reddit": {"reddit.com"}, "twilio": {"twilio.com"}, "dropbox": {"dropboxapi.com"}, "openweathermap": {"api.openweathermap.org"},
	"urlscanio": {"urlscan.io"}, "apitemplateio": {"api.apitemplate.io"}, "uproc": {"api.uproc.com"},
	"googletasks": {"googleapis.com"}, "googleanalytics": {"googleapis.com"}, "microsoftteams": {"graph.microsoft.com"},
	"microsoftonedrive": {"graph.microsoft.com"}, "microsoftexcel": {"graph.microsoft.com"},
	"dhl": {"dhl.com"}, "apify": {"api.apify.com", "apify.actor"},
	"stripe": {"api.stripe.com"}, "shopify": {"myshopify.com"}, "pinecone": {"pinecone.io"}, "supabase": {"supabase.co"},
}

// credentialHostAllowed decides whether credential c of type credType may
// be sent to host. Unknown generic credentials (header/query/basic auth)
// are allowed unless they declare allowedDomains.
func credentialHostAllowed(credType string, c map[string]interface{}, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	match := func(domain string) bool {
		domain = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(domain, "*.")))
		return domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
	}
	var allowed []string
	switch v := c["allowedDomains"].(type) {
	case string:
		allowed = splitList(v)
	case []interface{}:
		for _, x := range v {
			allowed = append(allowed, toString(x))
		}
	}
	if len(allowed) > 0 {
		for _, d := range allowed {
			if d == "*" || match(d) {
				return true
			}
		}
		return false
	}
	domains, known := knownCredentialDomains[credService(credType)]
	if !known {
		return true
	}
	if u := credString(c, "url", "baseUrl", "baseURL", "host"); u != "" {
		if pu, err := url.Parse(credBaseURL(c, "")); err == nil && match(pu.Hostname()) {
			return true
		}
		_ = u
	}
	for _, d := range domains {
		if match(d) {
			return true
		}
	}
	return false
}
