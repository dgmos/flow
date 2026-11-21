package n8n

import (
	"encoding/json"
	"net/url"
	"strings"
)

// VerifiEmail (community node n8n-nodes-verifiemail.verifiEmail).

func init() {
	registerREST("n8n-nodes-verifiemail.verifiEmail", &restService{
		defOps: map[string]string{"": "validateEmail"},
		client: func(ec *execCtx, i int) (*restClient, error) {
			c, _, err := ec.restCred(i, "verifiEmailApi", nil)
			if err != nil {
				return nil, err
			}
			return &restClient{base: ec.apiBase("verifiEmail", "https://api.verifi.email"),
				query: url.Values{"token": {credString(c, "apiKey")}}}, nil
		},
		ops: map[string]*restOp{
			"/validateEmail": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				email := a.str("email", "contact@verifi.email")
				if a.err != nil {
					return nil, a.err
				}
				if email == "" {
					return nil, permanentError{"Email address is required"}
				}
				v, err := cmtyCall(ec, c, "GET", "/check", url.Values{"email": {email}}, nil)
				if err != nil {
					return nil, err
				}
				return restItems(cmtyMerge(map[string]interface{}{"email": email}, v)), nil
			}},
			"/bulkValidateEmails": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				var emails []interface{}
				switch a.str("emailInputMethod", "jsonArray") {
				case "jsonArray":
					raw := a.get("emailsJson", `["user1@example.org", "user2@example.org", "invalid-email"]`)
					if s, ok := raw.(string); ok {
						if json.Unmarshal([]byte(s), &emails) != nil {
							return nil, permanentError{"Invalid JSON format for emails array"}
						}
					} else {
						emails = asList(raw)
					}
				case "commaSeparated":
					for _, e := range strings.Split(a.str("emailsCommaSeparated", "user1@example.org, user2@example.org, invalid-email"), ",") {
						if e = strings.Trim(strings.TrimSpace(e), `"'`); e != "" {
							emails = append(emails, e)
						}
					}
				case "individual":
					for _, x := range asList(asMap(a.get("emails", nil))["emailAddress"]) {
						if e := toString(asMap(x)["email"]); e != "" {
							emails = append(emails, e)
						}
					}
				}
				if a.err != nil {
					return nil, a.err
				}
				if len(emails) == 0 {
					return nil, permanentError{"At least one email address is required"}
				}
				v, err := cmtyCall(ec, c, "POST", "/v1/bulk/check", nil, map[string]interface{}{"emails": emails})
				if err != nil {
					return nil, err
				}
				return restItems(cmtyMerge(map[string]interface{}{"emails": emails}, v)), nil
			}},
			"/checkDomainHealth": {run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
				a := &cmtyArgs{ec: ec, i: i}
				domain, selector := a.str("domain", "verifi.email"), a.str("selector", "google")
				if a.err != nil {
					return nil, a.err
				}
				if domain == "" {
					return nil, permanentError{"Domain is required"}
				}
				q := url.Values{"domain": {domain}}
				var sel interface{}
				if selector != "" {
					q.Set("selector", selector)
					sel = selector
				}
				v, err := cmtyCall(ec, c, "GET", "/v1/domain/check", q, nil)
				if err != nil {
					return nil, err
				}
				return restItems(cmtyMerge(map[string]interface{}{"domain": domain, "selector": sel}, v)), nil
			}},
		},
	})
}
