package n8n

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dgmos/flow/internal/node"
)

// apiBase returns the base URL of an external service, honoring
// Options.APIBaseURLs overrides (proxies, testing).
func (ec *execCtx) apiBase(service, def string) string {
	if ec.opts != nil {
		if u, ok := ec.opts.APIBaseURLs[service]; ok && u != "" {
			return strings.TrimRight(u, "/")
		}
	}
	return def
}

type rawResp struct {
	body        []byte
	contentType string
	status      int
	header      http.Header
}

// doRequest sends a request through the outbound policy.
func (ec *execCtx) doRequest(method, u string, headers map[string]string, body []byte, contentType string) (rawResp, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ec.ctx, method, u, rd)
	if err != nil {
		return rawResp{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := outboundClient(ec.opts.Outbound).Do(req)
	if err != nil {
		return rawResp{}, sanitizeErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := node.ReadLimited(resp.Body, ec.opts.Outbound.MaxResponseBytesOrDefault())
	if err != nil {
		return rawResp{}, err
	}
	r := rawResp{body: raw, contentType: resp.Header.Get("Content-Type"), status: resp.StatusCode, header: resp.Header}
	if resp.StatusCode >= 400 {
		msg := fmt.Sprintf("%s %s failed with status %d: %s", method, redactURL(u), resp.StatusCode, truncate(string(raw), 400))
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			return r, errors.New(msg)
		}
		return r, permanentError{msg}
	}
	return r, nil
}

// apiCallRaw performs a request without a body and returns the raw response.
func (ec *execCtx) apiCallRaw(method, u string, headers map[string]string) (map[string]interface{}, rawResp, error) {
	r, err := ec.doRequest(method, u, headers, nil, "")
	return nil, r, err
}

// apiCall performs an authenticated JSON API request and decodes the
// response. body may be nil, a []byte (sent as-is with contentType) or any
// JSON-encodable value.
func (ec *execCtx) apiCall(method, u string, headers map[string]string, body interface{}, contentType string) (map[string]interface{}, []byte, error) {
	var raw []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		raw = b
	default:
		var err error
		if raw, err = json.Marshal(b); err != nil {
			return nil, nil, err
		}
		if contentType == "" {
			contentType = "application/json"
		}
	}
	r, err := ec.doRequest(method, u, headers, raw, contentType)
	if err != nil {
		return nil, r.body, err
	}
	var out map[string]interface{}
	if len(bytes.TrimSpace(r.body)) > 0 && strings.Contains(r.contentType, "json") {
		_ = json.Unmarshal(r.body, &out)
	}
	if out == nil {
		out = map[string]interface{}{}
	}
	return out, r.body, nil
}

// redactURL hides tokens embedded in URL paths (e.g. Telegram bot tokens)
// and drops query strings (which may carry API keys).
func redactURL(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		u = u[:i] + "?…"
	}
	return regexpBotToken.ReplaceAllString(u, "/bot***/")
}

var urlInText = regexp.MustCompile(`https?://[^\s"']+`)

// redactSecrets removes URLs' secrets from free text (e.g. *url.Error).
func redactSecrets(s string) string {
	return urlInText.ReplaceAllStringFunc(s, redactURL)
}

// sanitizeErr redacts URLs in transport errors so tokens never reach step
// errors or HTTP responses.
func sanitizeErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(permanentError); ok {
		return permanentError{redactSecrets(err.Error())}
	}
	return errors.New(redactSecrets(err.Error()))
}

// ---------------------------------------------------------------- Google auth

var googleScopes = map[string]string{
	"sheets":    "https://www.googleapis.com/auth/spreadsheets https://www.googleapis.com/auth/drive.file",
	"gmail":     "https://mail.google.com/",
	"drive":     "https://www.googleapis.com/auth/drive",
	"calendar":  "https://www.googleapis.com/auth/calendar",
	"youtube":   "https://www.googleapis.com/auth/youtube",
	"tasks":     "https://www.googleapis.com/auth/tasks",
	"analytics": "https://www.googleapis.com/auth/analytics.readonly",
	"docs":      "https://www.googleapis.com/auth/documents https://www.googleapis.com/auth/drive",
}

type cachedToken struct {
	token string
	exp   time.Time
}

var saTokens sync.Map

// googleAuth returns an Authorization header for a Google node. OAuth2
// credentials must carry a valid access token (refreshing is the credential
// provider's job); service accounts (googleApi) are exchanged here.
func (ec *execCtx) googleAuth(service, oauthType string) (map[string]string, error) {
	auth, _ := ec.str(0, "authentication", "oAuth2")
	if auth == "serviceAccount" {
		c, err := ec.credential("googleApi")
		if err != nil {
			return nil, err
		}
		tok, err := ec.serviceAccountToken(c, googleScopes[service])
		if err != nil {
			return nil, err
		}
		return map[string]string{"Authorization": "Bearer " + tok}, nil
	}
	c, err := ec.credential(oauthType)
	if err != nil {
		return nil, err
	}
	tok := accessToken(c)
	if tok == "" {
		return nil, permanentError{fmt.Sprintf("%s credential has no access token", oauthType)}
	}
	return map[string]string{"Authorization": "Bearer " + tok}, nil
}

func (ec *execCtx) serviceAccountToken(c map[string]interface{}, scope string) (string, error) {
	email := credString(c, "email", "client_email")
	keyPEM := strings.ReplaceAll(credString(c, "privateKey", "private_key"), `\n`, "\n")
	sub := ""
	if toBool(c["impersonate"]) {
		sub = credString(c, "delegatedEmail")
	}
	cacheKey := email + "|" + scope + "|" + sub
	if v, ok := saTokens.Load(cacheKey); ok {
		if t := v.(cachedToken); time.Until(t.exp) > time.Minute {
			return t.token, nil
		}
	}
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return "", permanentError{"googleApi: private key is not PEM encoded"}
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if k, err2 := x509.ParsePKCS1PrivateKey(block.Bytes); err2 == nil {
			parsed = k
		} else {
			return "", permanentError{"googleApi: cannot parse private key: " + err.Error()}
		}
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return "", permanentError{"googleApi: private key is not RSA"}
	}
	tokenURL := ec.apiBase("googleOAuth", "https://oauth2.googleapis.com") + "/token"
	now := time.Now()
	claims := map[string]interface{}{"iss": email, "scope": scope, "aud": "https://oauth2.googleapis.com/token", "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	if sub != "" {
		claims["sub"] = sub
	}
	enc := func(v interface{}) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signing + "." + base64.RawURLEncoding.EncodeToString(sig)}}
	out, _, err := ec.apiCall("POST", tokenURL, nil, []byte(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", err
	}
	tok := toString(out["access_token"])
	if tok == "" {
		return "", errors.New("googleApi: token exchange returned no access_token")
	}
	exp := now.Add(time.Duration(toFloatDefault(out["expires_in"], 3600)) * time.Second)
	saTokens.Store(cacheKey, cachedToken{token: tok, exp: exp})
	return tok, nil
}

// rlString reads a resource-locator parameter ({__rl, mode, value}) for
// item i, extracting IDs from URLs.
func (ec *execCtx) rlString(i int, path string) (string, error) {
	v, err := ec.param(i, path, "")
	if err != nil {
		return "", err
	}
	s := rlValue(v)
	if m, ok := v.(map[string]interface{}); ok && toString(m["mode"]) == "url" {
		s = idFromURL(s)
	} else if strings.HasPrefix(s, "https://") {
		s = idFromURL(s)
	}
	return s, nil
}

// idFromURL extracts Google resource IDs from document URLs.
func idFromURL(u string) string {
	for _, marker := range []string{"/d/", "/folders/", "id="} {
		if i := strings.Index(u, marker); i >= 0 {
			rest := u[i+len(marker):]
			end := strings.IndexAny(rest, "/?#&")
			if end >= 0 {
				rest = rest[:end]
			}
			return rest
		}
	}
	return u
}

var clients sync.Map // policy JSON -> *http.Client (connection reuse)

// outboundClient returns a shared guarded client per outbound policy.
func outboundClient(p node.OutboundPolicy) *http.Client {
	key := mustJSON(p)
	if c, ok := clients.Load(key); ok {
		return c.(*http.Client)
	}
	c, _ := clients.LoadOrStore(key, node.NewOutboundClient(p))
	return c.(*http.Client)
}
