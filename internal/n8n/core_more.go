package n8n

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/dgmos/flow/internal/node"
)

// ---------------------------------------------------------------- Rename Keys

func renameKeysNode(ec *execCtx) ([][]Item, error) {
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		out := Item{JSON: cloneJSON(it.JSON), Binary: it.Binary}
		keys, _ := ec.param(i, "keys.key", nil)
		for _, k := range asList(keys) {
			m := asMap(k)
			cur, nw := toString(m["currentKey"]), toString(m["newKey"])
			if cur == "" || nw == "" || cur == nw {
				continue
			}
			v, ok := getPath(it.JSON, cur, true)
			if !ok {
				continue
			}
			setPath(out.JSON, nw, v, true)
			deletePath(out.JSON, cur, true)
		}
		reps, _ := ec.param(i, "additionalOptions.regexReplacement.replacements", nil)
		for _, r := range asList(reps) {
			m := asMap(r)
			opts := asMap(m["options"])
			pat := toString(m["searchRegex"])
			if toBool(opts["caseInsensitive"]) {
				pat = "(?i)" + pat
			}
			re, err := regexp.Compile(pat)
			if err != nil {
				return nil, permanentError{fmt.Sprintf("Rename Keys: invalid regular expression %q", m["searchRegex"])}
			}
			depth := int(toFloatDefault(opts["depth"], -1))
			renameRegexKeys(out.JSON, re, jsReplacement(toString(m["replaceRegex"])), depth)
		}
		return []Item{out}, nil
	})
}

// jsReplacement converts a JavaScript replacement ($1, $&) to Go's syntax.
func jsReplacement(s string) string {
	s = strings.ReplaceAll(s, "$&", "${0}")
	return regexp.MustCompile(`\$(\d+)`).ReplaceAllString(s, "$${$1}")
}

// renameRegexKeys renames object keys matching re (first match, like
// JavaScript's String.replace) down to depth levels (-1: all).
func renameRegexKeys(v interface{}, re *regexp.Regexp, repl string, depth int) {
	switch t := v.(type) {
	case []interface{}:
		if depth != 0 {
			for _, x := range t {
				renameRegexKeys(x, re, repl, depth-1)
			}
		}
	case map[string]interface{}:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		for _, k := range keys {
			if depth != 0 {
				renameRegexKeys(t[k], re, repl, depth-1)
			}
			loc := re.FindStringSubmatchIndex(k)
			if loc == nil {
				continue
			}
			nk := k[:loc[0]] + string(re.ExpandString(nil, repl, k, loc)) + k[loc[1]:]
			if nk != k {
				t[nk] = t[k]
				delete(t, k)
			}
		}
	}
}

// ---------------------------------------------------------------- GraphQL

func graphqlNode(ec *execCtx) ([][]Item, error) {
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		method, _ := ec.str(i, "requestMethod", "POST")
		endpoint, _ := ec.str(i, "endpoint", "")
		format, _ := ec.str(i, "requestFormat", "json")
		if ec.version < 1.1 {
			format = firstNonEmpty(format, "graphql")
		}
		query, _ := ec.str(i, "query", "")
		respFormat, _ := ec.str(0, "responseFormat", "json")
		var body []byte
		ct := "application/" + format
		u := endpoint
		if method == http.MethodGet {
			sep := "?"
			if strings.Contains(u, "?") {
				sep = "&"
			}
			u += sep + "query=" + urlQueryEscape(query)
		}
		if format == "json" {
			vars, _ := ec.param(i, "variables", map[string]interface{}{})
			if s, ok := vars.(string); ok {
				var x interface{}
				if strings.TrimSpace(s) == "" {
					s = "{}"
				}
				if err := json.Unmarshal([]byte(s), &x); err != nil {
					return nil, permanentError{fmt.Sprintf("GraphQL: using variables failed: %v", err)}
				}
				vars = x
			}
			b := map[string]interface{}{"query": query, "variables": vars, "operationName": nil}
			if op, _ := ec.str(i, "operationName", ""); op != "" {
				b["operationName"] = op
			}
			body, _ = json.Marshal(b)
		} else {
			body = []byte(query)
		}
		if method == http.MethodGet {
			body = nil
		}
		req, err := http.NewRequestWithContext(ec.ctx, method, u, bytes.NewReader(body))
		if err != nil {
			return nil, permanentError{"GraphQL: invalid endpoint: " + err.Error()}
		}
		req.Header.Set("Content-Type", ct)
		hp, _ := ec.param(i, "headerParametersUi.parameter", nil)
		for _, h := range asList(hp) {
			m := asMap(h)
			req.Header.Set(toString(m["name"]), toString(m["value"]))
		}
		if err := graphqlAuth(ec, req); err != nil {
			return nil, err
		}
		resp, err := outboundClient(ec.opts.Outbound).Do(req)
		if err != nil {
			return nil, sanitizeErr(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := node.ReadLimited(resp.Body, ec.opts.Outbound.MaxResponseBytesOrDefault())
		if err != nil {
			return nil, err
		}
		if respFormat == "string" {
			prop, _ := ec.str(0, "dataPropertyName", "data")
			return []Item{{JSON: map[string]interface{}{prop: string(raw)}}}, nil
		}
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			if resp.StatusCode >= 400 {
				return nil, permanentError{fmt.Sprintf("GraphQL: request failed with status %d: %s", resp.StatusCode, truncate(string(raw), 300))}
			}
			return nil, permanentError{`GraphQL: response body is not valid JSON. Change "Response Format" to "String"`}
		}
		if m, ok := v.(map[string]interface{}); ok && m["errors"] != nil {
			msg := "Unexpected error"
			if l := asList(m["errors"]); len(l) > 0 {
				var parts []string
				for _, e := range l {
					parts = append(parts, toString(asMap(e)["message"]))
				}
				msg = strings.Join(parts, ", ")
			}
			return nil, permanentError{"GraphQL: " + msg}
		}
		return restItems(v), nil
	})
}

func graphqlAuth(ec *execCtx, req *http.Request) error {
	auth, _ := ec.str(0, "authentication", "none")
	credType := map[string]string{"basicAuth": "httpBasicAuth", "headerAuth": "httpHeaderAuth", "queryAuth": "httpQueryAuth", "customAuth": "httpCustomAuth",
		"digestAuth": "httpDigestAuth", "oAuth1": "oAuth1Api", "oAuth2": "oAuth2Api", "multipleHeadersAuth": "httpMultipleHeadersAuth"}[auth]
	if auth == "predefinedCredentialType" {
		credType, _ = ec.str(0, "nodeCredentialType", "")
	}
	if auth == "none" || auth == "" {
		return nil
	}
	if credType == "" || credType == "oAuth1Api" || credType == "httpDigestAuth" {
		return permanentError{fmt.Sprintf("GraphQL: authentication %q is not supported", auth)}
	}
	c, err := ec.credential(credType)
	if err != nil {
		return err
	}
	if !credentialHostAllowed(credType, c, req.URL.Hostname()) {
		return permanentError{fmt.Sprintf("credential %s may not be sent to %s (set allowedDomains on the credential to allow it)", credType, req.URL.Hostname())}
	}
	return applyCredential(req, credType, c)
}

func urlQueryEscape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// ---------------------------------------------------------------- JWT

func jwtNode(ec *execCtx) ([][]Item, error) {
	c, err := ec.credential("jwtAuth")
	if err != nil {
		return nil, err
	}
	op, _ := ec.str(0, "operation", "sign")
	return eachItem(ec, func(i int, it Item) ([]Item, error) {
		opts := fieldsOf(ec, i, "options")
		alg := firstNonEmpty(toString(opts["algorithm"]), firstNonEmpty(credString(c, "algorithm"), "HS256"))
		pemKeys := credString(c, "keyType") == "pemKey"
		switch op {
		case "sign":
			var payload map[string]interface{}
			if useJSON, _ := ec.boolean(i, "useJson", false); useJSON {
				v, _ := ec.param(i, "claimsJson", "{}")
				if payload, err = jsonObjectParam(v); err != nil {
					return nil, permanentError{"JWT: claims JSON: " + err.Error()}
				}
			} else {
				payload = fieldsOf(ec, i, "claims")
			}
			if _, ok := payload["iat"]; !ok {
				payload["iat"] = time.Now().Unix()
			}
			header := map[string]interface{}{"alg": alg, "typ": "JWT"}
			if kid := toString(opts["kid"]); kid != "" {
				header["kid"] = kid
			}
			hv, _ := ec.param(i, "headerClaims", "{}")
			hc, err := jsonObjectParam(hv)
			if err != nil {
				return nil, permanentError{"JWT: header claims: " + err.Error()}
			}
			for k, v := range hc {
				header[k] = v
			}
			key := credString(c, "secret")
			if pemKeys {
				key = credString(c, "privateKey")
			}
			tok, err := jwtSign(header, payload, alg, key, pemKeys)
			if err != nil {
				return nil, permanentError{"JWT: " + err.Error()}
			}
			return []Item{{JSON: map[string]interface{}{"token": tok}}}, nil
		case "decode", "verify":
			tok, _ := ec.str(i, "token", "")
			if tok == "" {
				return nil, permanentError{"JWT: the JWT token was not provided"}
			}
			header, payload, sig, signed, err := jwtParse(tok)
			if err != nil {
				return nil, permanentError{"JWT: " + err.Error()}
			}
			if op == "verify" {
				key := credString(c, "secret")
				if pemKeys {
					key = credString(c, "publicKey")
				}
				if toString(header["alg"]) != alg {
					return nil, permanentError{"JWT: invalid algorithm"}
				}
				if err := jwtVerify(signed, sig, alg, key, pemKeys); err != nil {
					return nil, permanentError{"JWT: the JWT token can't be verified"}
				}
				tol := int64(toFloatDefault(opts["clockTolerance"], 0))
				now := time.Now().Unix()
				if exp, ok := toFloat(payload["exp"]); ok && !toBool(opts["ignoreExpiration"]) && now >= int64(exp)+tol {
					return nil, permanentError{"JWT: jwt expired"}
				}
				if nbf, ok := toFloat(payload["nbf"]); ok && !toBool(opts["ignoreNotBefore"]) && now+tol < int64(nbf) {
					return nil, permanentError{"JWT: jwt not active"}
				}
			}
			if toBool(opts["complete"]) {
				return []Item{{JSON: map[string]interface{}{"header": header, "payload": payload, "signature": base64.RawURLEncoding.EncodeToString(sig)}}}, nil
			}
			return []Item{{JSON: map[string]interface{}{"payload": payload}}}, nil
		}
		return nil, unsupportedOp(ec.node.Name, "", op)
	})
}

func jsonObjectParam(v interface{}) (map[string]interface{}, error) {
	switch t := v.(type) {
	case map[string]interface{}:
		return t, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return map[string]interface{}{}, nil
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(t), &m); err != nil {
			return nil, err
		}
		if m == nil {
			m = map[string]interface{}{}
		}
		return m, nil
	case nil:
		return map[string]interface{}{}, nil
	}
	return nil, errors.New("must be a JSON object")
}

func jwtHash(alg string) (crypto.Hash, func() hash.Hash, error) {
	switch alg[2:] {
	case "256":
		return crypto.SHA256, sha256.New, nil
	case "384":
		return crypto.SHA384, sha512.New384, nil
	case "512":
		return crypto.SHA512, sha512.New, nil
	}
	return 0, nil, fmt.Errorf("unsupported algorithm %q", alg)
}

// pemBlock accepts a PEM key with its line breaks flattened (as pasted
// into a credential field).
func pemBlock(s string) (*pem.Block, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), `\n`, "\n")
	if b, _ := pem.Decode([]byte(s)); b != nil {
		return b, nil
	}
	re := regexp.MustCompile(`-----BEGIN ([A-Z ]+)-----(.*)-----END [A-Z ]+-----`)
	m := re.FindStringSubmatch(strings.ReplaceAll(s, "\n", ""))
	if m == nil {
		return nil, errors.New("invalid PEM key")
	}
	der, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(m[2], " ", ""))
	if err != nil {
		return nil, errors.New("invalid PEM key")
	}
	return &pem.Block{Type: m[1], Bytes: der}, nil
}

func parsePrivateKey(s string) (crypto.Signer, error) {
	b, err := pemBlock(s)
	if err != nil {
		return nil, err
	}
	if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
		if sg, ok := k.(crypto.Signer); ok {
			return sg, nil
		}
	}
	if k, err := x509.ParsePKCS1PrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParseECPrivateKey(b.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("unsupported private key")
}

func parsePublicKey(s string) (crypto.PublicKey, error) {
	b, err := pemBlock(s)
	if err != nil {
		return nil, err
	}
	if k, err := x509.ParsePKIXPublicKey(b.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS1PublicKey(b.Bytes); err == nil {
		return k, nil
	}
	if c, err := x509.ParseCertificate(b.Bytes); err == nil {
		return c.PublicKey, nil
	}
	return nil, errors.New("unsupported public key")
}

func jwtSign(header, payload map[string]interface{}, alg, key string, pemKey bool) (string, error) {
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(payload)
	signed := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(pb)
	if alg == "none" {
		return signed + ".", nil
	}
	h, newHash, err := jwtHash(alg)
	if err != nil {
		return "", err
	}
	var sig []byte
	if alg[0] == 'H' {
		sig = hmacSum(newHash, key, []byte(signed))
	} else {
		if !pemKey {
			return "", fmt.Errorf("%s needs a PEM key credential", alg)
		}
		k, err := parsePrivateKey(key)
		if err != nil {
			return "", err
		}
		d := newHash()
		d.Write([]byte(signed))
		digest := d.Sum(nil)
		switch alg[0] {
		case 'R':
			rk, ok := k.(*rsa.PrivateKey)
			if !ok {
				return "", errors.New("RS algorithms need an RSA key")
			}
			sig, err = rsa.SignPKCS1v15(rand.Reader, rk, h, digest)
		case 'P':
			rk, ok := k.(*rsa.PrivateKey)
			if !ok {
				return "", errors.New("PS algorithms need an RSA key")
			}
			sig, err = rsa.SignPSS(rand.Reader, rk, h, digest, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
		case 'E':
			ek, ok := k.(*ecdsa.PrivateKey)
			if !ok {
				return "", errors.New("ES algorithms need an EC key")
			}
			var r, s *big.Int
			if r, s, err = ecdsa.Sign(rand.Reader, ek, digest); err == nil {
				size := (ek.Curve.Params().BitSize + 7) / 8
				sig = make([]byte, 2*size)
				r.FillBytes(sig[:size])
				s.FillBytes(sig[size:])
			}
		default:
			return "", fmt.Errorf("unsupported algorithm %q", alg)
		}
		if err != nil {
			return "", err
		}
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func jwtParse(tok string) (header, payload map[string]interface{}, sig []byte, signed string, err error) {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) != 3 {
		return nil, nil, nil, "", errors.New("jwt malformed")
	}
	hb, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[0], "="))
	pb, err2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	sig, err3 := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[2], "="))
	if err1 != nil || err2 != nil || err3 != nil || json.Unmarshal(hb, &header) != nil || json.Unmarshal(pb, &payload) != nil {
		return nil, nil, nil, "", errors.New("invalid token")
	}
	return header, payload, sig, parts[0] + "." + parts[1], nil
}

func jwtVerify(signed string, sig []byte, alg, key string, pemKey bool) error {
	if alg == "none" {
		return errors.New("unsigned token")
	}
	h, newHash, err := jwtHash(alg)
	if err != nil {
		return err
	}
	if alg[0] == 'H' {
		if !hmac.Equal(sig, hmacSum(newHash, key, []byte(signed))) {
			return errors.New("invalid signature")
		}
		return nil
	}
	if !pemKey {
		return errors.New("needs a PEM key")
	}
	pk, err := parsePublicKey(key)
	if err != nil {
		return err
	}
	d := newHash()
	d.Write([]byte(signed))
	digest := d.Sum(nil)
	switch k := pk.(type) {
	case *rsa.PublicKey:
		if alg[0] == 'P' {
			return rsa.VerifyPSS(k, h, digest, sig, nil)
		}
		return rsa.VerifyPKCS1v15(k, h, digest, sig)
	case *ecdsa.PublicKey:
		if len(sig)%2 != 0 {
			return errors.New("invalid signature")
		}
		r, s := new(big.Int).SetBytes(sig[:len(sig)/2]), new(big.Int).SetBytes(sig[len(sig)/2:])
		if !ecdsa.Verify(k, digest, r, s) {
			return errors.New("invalid signature")
		}
		return nil
	}
	return errors.New("unsupported key")
}
