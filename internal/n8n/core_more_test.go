package n8n

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/models"
	"github.com/dgmos/flow/internal/node"
)

func TestRenameKeys(t *testing.T) {
	data := wfJSON(t, []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		nd("Seed", "set", 3.4, map[string]interface{}{"mode": "raw", "jsonOutput": `{"a":1,"user":{"first_name":"Ada","last_name":"L"},"keep":true}`}),
		nd("Rename", "renameKeys", 1, map[string]interface{}{
			"keys": map[string]interface{}{"key": []interface{}{
				map[string]interface{}{"currentKey": "a", "newKey": "b"},
				map[string]interface{}{"currentKey": "user.first_name", "newKey": "first"},
				map[string]interface{}{"currentKey": "missing", "newKey": "x"}}},
			"additionalOptions": map[string]interface{}{"regexReplacement": map[string]interface{}{"replacements": []interface{}{
				map[string]interface{}{"searchRegex": "_(name)$", "replaceRegex": "-$1", "options": map[string]interface{}{"depth": -1}}}}},
		}),
	}, "Start->Seed", "Seed->Rename")
	_, steps := newHarness(t, nil).run(t, data, "")
	got := itemsOf(t, steps["Rename"], 0)
	want := `{"b":1,"first":"Ada","keep":true,"user":{"last-name":"L"}}`
	if len(got) != 1 || mustJSON(got[0]) != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestGraphQLNode(t *testing.T) {
	f := newFakeAPI(t, func(r recorded) (int, string, interface{}) {
		if strings.Contains(r.body, "boom") {
			return 200, "application/json", map[string]interface{}{"errors": []interface{}{map[string]interface{}{"message": "bad field"}}}
		}
		return 200, "application/json", map[string]interface{}{"data": map[string]interface{}{"viewer": map[string]interface{}{"login": "me"}}}
	})
	creds := NewStaticCredentials()
	creds.Set("httpHeaderAuth", "*", map[string]interface{}{"name": "Authorization", "value": "Bearer gq"})
	h := newHarness(t, &Options{Credentials: creds, Outbound: node.OutboundPolicy{AllowedHosts: []string{"127.0.0.1"}}})
	run := func(query string) *stepResult {
		data := wfJSON(t, []map[string]interface{}{
			nd("Start", "manualTrigger", 1, nil),
			credNode(map[string]interface{}{"name": "GQL", "type": "n8n-nodes-base.graphql", "typeVersion": 1.1, "onError": "continueRegularOutput", "parameters": map[string]interface{}{
				"authentication": "headerAuth", "endpoint": f.srv.URL + "/graphql", "query": query, "variables": `{"n": 1}`,
				"headerParametersUi": map[string]interface{}{"parameter": []interface{}{map[string]interface{}{"name": "X-Extra", "value": "1"}}}}}, "httpHeaderAuth"),
		}, "Start->GQL")
		_, steps := h.run(t, data, "")
		s := steps["GQL"]
		return &stepResult{s.Error, s}
	}
	r := run("query { viewer { login } }")
	if got := itemsOf(t, r.step, 0); jpath(got[0], "data.viewer.login") != "me" {
		t.Fatalf("got %v", got)
	}
	req := f.find(t, "POST", "/graphql")
	if req.auth != "Bearer gq" || req.body != `{"operationName":null,"query":"query { viewer { login } }","variables":{"n":1}}` {
		t.Fatalf("request %+v", req)
	}
	if got := itemsOf(t, run("query { boom }").step, 0); !strings.Contains(mustJSON(got), "bad field") {
		t.Fatalf("errors not raised: %v", got)
	}
}

func TestJWTNode(t *testing.T) {
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pk8 := func(k interface{}) string {
		b, _ := x509.MarshalPKCS8PrivateKey(k)
		return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}))
	}
	pub := func(k interface{}) string {
		b, _ := x509.MarshalPKIXPublicKey(k)
		return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}))
	}
	for _, c := range []struct {
		alg  string
		cred map[string]interface{}
	}{
		{"HS256", map[string]interface{}{"keyType": "passphrase", "secret": "s3cret", "algorithm": "HS256"}},
		{"RS256", map[string]interface{}{"keyType": "pemKey", "privateKey": pk8(rk), "publicKey": pub(&rk.PublicKey), "algorithm": "RS256"}},
		{"PS384", map[string]interface{}{"keyType": "pemKey", "privateKey": pk8(rk), "publicKey": pub(&rk.PublicKey), "algorithm": "PS384"}},
		{"ES256", map[string]interface{}{"keyType": "pemKey", "privateKey": pk8(ek), "publicKey": pub(&ek.PublicKey), "algorithm": "ES256"}},
	} {
		t.Run(c.alg, func(t *testing.T) {
			creds := NewStaticCredentials()
			creds.Set("jwtAuth", "*", c.cred)
			h := newHarness(t, &Options{Credentials: creds})
			data := wfJSON(t, []map[string]interface{}{
				nd("Start", "manualTrigger", 1, nil),
				credNode(nd("Sign", "jwt", 1, map[string]interface{}{"useJson": true, "claimsJson": `{"sub":"u1","exp":9999999999}`}), "jwtAuth"),
				credNode(nd("Verify", "jwt", 1, map[string]interface{}{"operation": "verify", "token": "={{ $json.token }}"}), "jwtAuth"),
				credNode(nd("Decode", "jwt", 1, map[string]interface{}{"operation": "decode", "token": "={{ $('Sign').item.json.token }}", "options": map[string]interface{}{"complete": true}}), "jwtAuth"),
			}, "Start->Sign", "Sign->Verify", "Verify->Decode")
			_, steps := h.run(t, data, "")
			if e := steps["Verify"].Error; e != "" {
				t.Fatalf("verify: %s (sign: %s)", e, steps["Sign"].Error)
			}
			v := itemsOf(t, steps["Verify"], 0)
			if jpath(v[0], "payload.sub") != "u1" {
				t.Fatalf("verify: %v", v)
			}
			d := itemsOf(t, steps["Decode"], 0)
			if jpath(d[0], "header.alg") != c.alg {
				t.Fatalf("decode: %v", d)
			}
		})
	}
}

type stepResult struct {
	err  string
	step *models.ExecutionStep
}
