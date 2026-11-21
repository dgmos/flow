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
	"fmt"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Salesforce (n8n-nodes-base.salesforce), REST API v59.0. Records are
// written with PascalCase field names derived from the node's camelCase
// parameters; lists are SOQL queries.

const salesforceAPI = "/services/data/v59.0"

// sfObject describes a standard object resource.
type sfObject struct {
	path   string // /sobjects/<path>
	soql   string // SOQL object name
	id     string // id parameter
	fields string // required top-level fields
	upsert bool
	note   bool // addNote
	camp   bool // addToCampaign
}

var sfObjects = map[string]sfObject{
	"lead":        {"lead", "Lead", "leadId", "company:Company lastname:LastName", true, true, true},
	"contact":     {"contact", "Contact", "contactId", "lastname:LastName", true, true, true},
	"account":     {"account", "Account", "accountId", "name:Name", true, true, false},
	"opportunity": {"opportunity", "Opportunity", "opportunityId", "name:Name closeDate:CloseDate stageName:StageName", true, true, false},
	"case":        {"case", "Case", "caseId", "type:Type", false, false, false},
	"task":        {"task", "Task", "taskId", "status:Status", false, false, false},
	"attachment":  {"attachment", "Attachment", "attachmentId", "", false, false, false},
}

func init() {
	ops := map[string]*restOp{}
	for res, o := range sfObjects {
		res, o := res, o
		obj := "/sobjects/" + o.path
		item := obj + "/{" + o.id + "}"
		ops[res+"/create"] = &restOp{method: "POST", path: obj, body: o.fields, build: sfRecord(res, "additionalFields"), post: sfEmpty}
		ops[res+"/update"] = &restOp{method: "PATCH", path: item, build: sfRecord(res, "updateFields"), post: sfEmpty}
		ops[res+"/get"] = &restOp{path: item, post: sfEmpty}
		ops[res+"/delete"] = &restOp{method: "DELETE", path: item, post: sfEmpty}
		ops[res+"/getSummary"] = &restOp{path: obj, post: sfEmpty}
		ops[res+"/getAll"] = &restOp{run: sfGetAll(o.soql)}
		if o.upsert {
			ops[res+"/upsert"] = &restOp{method: "PATCH", path: obj, body: o.fields, build: sfUpsert(sfRecord(res, "additionalFields")), post: sfEmpty}
		}
		if o.note {
			ops[res+"/addNote"] = &restOp{method: "POST", path: "/sobjects/note", body: o.id + ":ParentId title:Title @options body:Body owner:OwnerId:rl ownerId:OwnerId:rl isPrivate:IsPrivate", post: sfEmpty}
		}
		if o.camp {
			ops[res+"/addToCampaign"] = &restOp{method: "POST", path: "/sobjects/CampaignMember", body: o.id + ":" + strings.ToUpper(o.id[:1]) + o.id[1:] + " campaignId:CampaignId @options status:Status", post: sfEmpty}
		}
	}
	ops["attachment/create"].body = "name:Name parentId:ParentId"
	ops["attachment/create"].run = sfAttachment(ops["attachment/create"], "binaryPropertyName")
	ops["attachment/update"].run = sfAttachment(ops["attachment/update"], "updateFields.binaryPropertyName")
	ops["case/addComment"] = &restOp{method: "POST", path: "/sobjects/casecomment", body: "caseId:ParentId @options commentBody:CommentBody isPublished:IsPublished", post: sfEmpty}

	cf := "customFieldsUi:customFieldsUi" // formatted by sfCustomFields
	ops["customObject/create"] = &restOp{method: "POST", path: "/sobjects/{customObject}", body: cf + " @additionalFields recordTypeId:RecordTypeId", build: sfCustomFields, post: sfEmpty}
	ops["customObject/upsert"] = &restOp{method: "PATCH", path: "/sobjects/{customObject}", body: cf + " @additionalFields recordTypeId:RecordTypeId", build: sfUpsert(sfCustomFields), post: sfEmpty}
	ops["customObject/update"] = &restOp{method: "PATCH", path: "/sobjects/{customObject}/{recordId}", body: cf + " @updateFields recordTypeId:RecordTypeId", build: sfCustomFields, post: sfEmpty}
	ops["customObject/get"] = &restOp{path: "/sobjects/{customObject}/{recordId}", post: sfEmpty}
	ops["customObject/delete"] = &restOp{method: "DELETE", path: "/sobjects/{customObject}/{recordId}", post: sfEmpty}
	ops["customObject/getAll"] = &restOp{run: sfGetAll("")}

	ops["user/get"] = &restOp{path: "/sobjects/user/{userId}", post: sfEmpty}
	ops["user/getAll"] = &restOp{run: sfGetAll("User")}
	ops["search/query"] = &restOp{run: func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		q, _ := ec.str(i, "query", "")
		recs, err := sfQuery(ec, c, q)
		return restItems(recs), err
	}}
	ops["flow/invoke"] = &restOp{method: "POST", path: "/actions/custom/flow/{apiName}", build: sfFlowInputs, post: sfEmpty}
	ops["flow/getAll"] = &restOp{path: "/actions/custom/flow", list: true, listKey: "actions"}
	ops["document/upload"] = &restOp{run: sfDocument}

	registerREST("salesforce", &restService{
		defResource: "lead",
		defOps: map[string]string{"lead": "create", "contact": "create", "account": "create", "opportunity": "create", "case": "create", "task": "create",
			"attachment": "create", "customObject": "create", "document": "upload", "flow": "invoke", "search": "query", "user": "get"},
		client: salesforceClient,
		ops:    ops,
	})
}

func salesforceClient(ec *execCtx, i int) (*restClient, error) {
	auth, _ := ec.str(i, "authentication", "oAuth2")
	var tok, inst string
	if auth == "jwt" {
		c, err := ec.credential("salesforceJwtApi")
		if err != nil {
			return nil, err
		}
		if tok, inst, err = ec.salesforceJWT(c); err != nil {
			return nil, err
		}
	} else {
		c, err := ec.credential("salesforceOAuth2Api")
		if err != nil {
			return nil, err
		}
		tok = accessToken(c)
		inst = firstNonEmpty(credString(asMap(c["oauthTokenData"]), "instance_url"), credString(c, "instance_url", "instanceUrl"))
	}
	base := ec.apiBase("salesforce", strings.TrimRight(inst, "/"))
	if base == "" {
		return nil, permanentError{ec.node.Name + ": the Salesforce credential has no instance URL"}
	}
	return &restClient{base: base + salesforceAPI, headers: map[string]string{"Authorization": "Bearer " + tok}}, nil
}

type sfToken struct {
	tok, inst string
	exp       time.Time
}

var sfJWTTokens sync.Map

// salesforceJWT returns an access token and instance URL with the OAuth 2.0
// JWT bearer flow (or the credential's cached token).
func (ec *execCtx) salesforceJWT(c map[string]interface{}) (string, string, error) {
	if t, inst := credString(c, "accessToken"), credString(c, "instanceUrl"); t != "" && inst != "" {
		return t, inst, nil
	}
	authURL := strings.TrimRight(credString(c, "myDomainUrl"), "/")
	if authURL == "" {
		authURL = "https://login.salesforce.com"
		if credString(c, "environment") == "sandbox" {
			authURL = "https://test.salesforce.com"
		}
	}
	key := credString(c, "clientId") + "|" + credString(c, "username") + "|" + authURL
	if v, ok := sfJWTTokens.Load(key); ok {
		if t := v.(sfToken); time.Now().Before(t.exp) {
			return t.tok, t.inst, nil
		}
	}
	block, _ := pem.Decode([]byte(strings.ReplaceAll(credString(c, "privateKey"), `\n`, "\n")))
	if block == nil {
		return "", "", permanentError{"salesforceJwtApi: private key is not PEM encoded"}
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		k, err2 := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err2 != nil {
			return "", "", permanentError{"salesforceJwtApi: cannot parse private key: " + err.Error()}
		}
		parsed = k
	}
	rk, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return "", "", permanentError{"salesforceJwtApi: private key is not RSA"}
	}
	enc := func(v interface{}) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	now := time.Now()
	signing := enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." +
		enc(map[string]interface{}{"iss": credString(c, "clientId"), "sub": credString(c, "username"), "aud": authURL, "exp": now.Add(3 * time.Minute).Unix()})
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rk, crypto.SHA256, sum[:])
	if err != nil {
		return "", "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signing + "." + base64.RawURLEncoding.EncodeToString(sig)}}
	out, _, err := ec.apiCall("POST", ec.apiBase("salesforceAuth", authURL)+"/services/oauth2/token", nil, []byte(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", "", err
	}
	tok, inst := toString(out["access_token"]), toString(out["instance_url"])
	if tok == "" || inst == "" {
		return "", "", permanentError{"Salesforce JWT authentication did not return an access token and instance URL"}
	}
	sfJWTTokens.Store(key, sfToken{tok, inst, now.Add(30 * time.Minute)})
	return tok, inst, nil
}

// sfEmpty outputs n8n's {errors: [], success: true} for empty responses
// (updates, deletes).
func sfEmpty(_ *execCtx, _ int, v interface{}) (interface{}, error) {
	if v == nil || v == "" {
		return map[string]interface{}{"errors": []interface{}{}, "success": true}, nil
	}
	return v, nil
}

// sfRecord copies a collection (additionalFields/updateFields) into the
// record: camelCase names become PascalCase, with n8n's exceptions.
func sfRecord(res, coll string) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		for k, v := range fieldsOf(ec, i, coll) {
			if v == nil {
				continue
			}
			switch k {
			case "customFieldsUi":
				for _, x := range asList(asMap(v)["customFieldsValues"]) {
					m := asMap(x)
					if id := rlValue(m["fieldId"]); id != "" {
						r.body[id] = m["value"]
					}
				}
			case "owner", "ownerId":
				if s := rlValue(v); s != "" {
					r.body["OwnerId"] = s
				}
			case "accountId", "acconuntId":
				if s := rlValue(v); s != "" {
					r.body["AccountId"] = s
				}
			case "firstname":
				r.body["FirstName"] = v
			case "lastname":
				r.body["LastName"] = v
			case "type":
				if res == "task" {
					r.body["TaskSubtype"] = v
				} else {
					r.body["Type"] = v
				}
			case "binaryPropertyName":
			default:
				r.body[strings.ToUpper(k[:1])+k[1:]] = v
			}
		}
		return nil
	}
}

// sfUpsert sends the record to /sobjects/<object>/<externalId>/<value>.
func sfUpsert(build func(ec *execCtx, i int, r *restRequest) error) func(ec *execCtx, i int, r *restRequest) error {
	return func(ec *execCtx, i int, r *restRequest) error {
		if err := build(ec, i, r); err != nil {
			return err
		}
		ext, _ := ec.str(0, "externalId", "")
		v, _ := ec.str(i, "externalIdValue", "")
		if ec.version < 1.2 {
			if d, err := url.PathUnescape(v); err == nil {
				v = d
			}
		}
		if ext == "" || v == "" {
			return permanentError{ec.node.Name + ": externalId and externalIdValue are required"}
		}
		r.path += "/" + url.PathEscape(ext) + "/" + url.PathEscape(v)
		delete(r.body, ext)
		return nil
	}
}

func sfCustomFields(ec *execCtx, i int, r *restRequest) error {
	ui := asMap(r.body["customFieldsUi"])
	delete(r.body, "customFieldsUi")
	for _, x := range asList(ui["customFieldsValues"]) {
		m := asMap(x)
		if id := rlValue(m["fieldId"]); id != "" {
			r.body[id] = m["value"]
		}
	}
	return nil
}

func sfFlowInputs(ec *execCtx, i int, r *restRequest) error {
	vars := map[string]interface{}{}
	if j, _ := ec.boolean(i, "jsonParameters", false); j {
		v, _ := ec.param(i, "variablesJson", "")
		x, err := restConv("json", v)
		if err != nil {
			return permanentError{ec.node.Name + ": variablesJson is not valid JSON"}
		}
		if m, ok := x.(map[string]interface{}); ok {
			vars = m
		}
	} else {
		for _, x := range asList(asMap(fieldsOf(ec, i, "variablesUi"))["variablesValues"]) {
			m := asMap(x)
			vars[toString(m["name"])] = m["value"]
		}
	}
	r.body["inputs"] = []interface{}{vars}
	return nil
}

// sfAttachment creates or updates an attachment, its body read from the
// binary property named by the prop parameter.
func sfAttachment(op *restOp, prop string) func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
		req, err := op.request(ec, i)
		if err != nil {
			return nil, err
		}
		name, _ := ec.str(i, prop, "")
		if name != "" || op.method == "POST" {
			b, ok := it.Binary[firstNonEmpty(name, "data")].(map[string]interface{})
			if !ok {
				return nil, permanentError{fmt.Sprintf("The property %s does not exist", name)}
			}
			req.body["Body"], req.body["ContentType"] = b["data"], b["mimeType"]
		}
		v, err := c.do(ec, req)
		if err != nil {
			return nil, err
		}
		v, _ = sfEmpty(ec, i, v)
		return restItems(v), nil
	}
}

// sfDocument uploads a ContentVersion as multipart form data.
func sfDocument(ec *execCtx, i int, it Item, c *restClient) ([]Item, error) {
	title, _ := ec.str(i, "title", "")
	prop, _ := ec.str(i, "binaryPropertyName", "data")
	data, _, fileName, err := binaryOf(it, prop)
	if err != nil {
		return nil, err
	}
	af := fieldsOf(ec, i, "additionalFields")
	entity := map[string]interface{}{"Title": title, "ContentLocation": "S"}
	if s := rlValue(af["ownerId"]); s != "" {
		entity["ownerId"] = s
	}
	if s := toString(af["linkToObjectId"]); s != "" {
		entity["FirstPublishLocationId"] = s
	}
	ext := toString(af["fileExtension"])
	if ext == "" {
		ext = toString(asMap(it.Binary[prop])["fileExtension"])
		if ext == "" && strings.Contains(fileName, ".") {
			ext = fileName[strings.LastIndex(fileName, ".")+1:]
		}
	}
	entity["PathOnClient"] = title + "." + ext
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="entity_content"`)
	h.Set("Content-Type", "application/json")
	pw, _ := w.CreatePart(h)
	_, _ = pw.Write([]byte(mustJSON(entity)))
	fw, _ := w.CreateFormFile("VersionData", toString(entity["PathOnClient"]))
	_, _ = fw.Write(data)
	_ = w.Close()
	v, err := c.do(ec, &restRequest{method: "POST", path: "/sobjects/ContentVersion", raw: buf.Bytes(), contentType: w.FormDataContentType()})
	if err != nil {
		return nil, err
	}
	v, _ = sfEmpty(ec, i, v)
	return restItems(v), nil
}

// sfGetAll lists records with a SOQL query built from options (fields,
// conditionsUi); sobject "" reads the customObject parameter.
func sfGetAll(sobject string) func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
	return func(ec *execCtx, i int, _ Item, c *restClient) ([]Item, error) {
		obj := sobject
		if obj == "" {
			v, _ := ec.param(i, "customObject", "")
			obj = rlValue(v)
		}
		all, _ := ec.boolean(i, "returnAll", false)
		limit, _ := ec.num(i, "limit", 50)
		q, err := sfSOQL(fieldsOf(ec, i, "options"), obj, all, int(limit), ec.version)
		if err != nil {
			return nil, permanentError{ec.node.Name + ": " + err.Error()}
		}
		recs, err := sfQuery(ec, c, q)
		return restItems(recs), err
	}
}

// sfQuery runs a SOQL query and follows nextRecordsUrl.
func sfQuery(ec *execCtx, c *restClient, q string) ([]interface{}, error) {
	req := &restRequest{method: "GET", path: "/query", query: url.Values{"q": {q}}}
	var out []interface{}
	for n := 0; n < 10000; n++ {
		v, err := c.do(ec, req)
		if err != nil {
			return nil, err
		}
		m := asMap(v)
		out = append(out, asList(m["records"])...)
		next := toString(m["nextRecordsUrl"])
		if next == "" {
			break
		}
		req = &restRequest{method: "GET", path: "/query/" + next[strings.LastIndex(next, "/")+1:], query: url.Values{"q": {q}}}
	}
	if out == nil {
		out = []interface{}{}
	}
	return out, nil
}

var sfDefaultFields = map[string]string{
	"Account":     "id,name,type,LastModifiedDate",
	"Lead":        "id,company,firstname,lastname,street,postalCode,city,email,status,LastModifiedDate",
	"Contact":     "id,firstname,lastname,email,LastModifiedDate",
	"Opportunity": "id,accountId,amount,probability,type,LastModifiedDate",
	"Case":        "id,accountId,contactId,priority,status,subject,type,LastModifiedDate",
	"Task":        "id,subject,status,priority,LastModifiedDate",
	"Attachment":  "id,name,LastModifiedDate",
	"User":        "id,name,email,LastModifiedDate",
}

var (
	sfFieldRe  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(__[a-zA-Z]+)?(\.[a-zA-Z][a-zA-Z0-9_]*(__[a-zA-Z]+)?)*$`)
	sfObjectRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:__[A-Za-z][A-Za-z0-9_]*)*$`)
	sfOps      = map[string]string{"EQUAL": "=", "=": "=", "!=": "!=", "<>": "<>", "<": "<", "<=": "<=", ">": ">", ">=": ">=",
		"LIKE": "LIKE", "NOT LIKE": "NOT LIKE", "IN": "IN", "NOT IN": "NOT IN", "INCLUDES": "INCLUDES", "EXCLUDES": "EXCLUDES"}
	sfDateLitRe  = regexp.MustCompile(`^(YESTERDAY|TODAY|TOMORROW|(LAST|THIS|NEXT)_(WEEK|MONTH|QUARTER|YEAR|FISCAL_QUARTER|FISCAL_YEAR)|(LAST|NEXT)_90_DAYS|(LAST|NEXT)_N_(DAYS|WEEKS|MONTHS|QUARTERS|YEARS|FISCAL_QUARTERS|FISCAL_YEARS):\d+|N_(DAYS|WEEKS|MONTHS|QUARTERS|YEARS|FISCAL_QUARTERS|FISCAL_YEARS)_AGO:\d+)$`)
	sfDateTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}:\d{2}(\.\d{3})?(Z|[+-]\d{2}:\d{2})?)?$`)
	sfNumberRe   = regexp.MustCompile(`^-?(0|[1-9]\d*)(\.\d+)?$`)
	sfEscaper    = strings.NewReplacer(`\`, `\\`, `'`, `\'`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "\f", `\f`, "\b", `\b`)
)

// sfSOQL builds the getAll query as n8n does.
func sfSOQL(opts map[string]interface{}, sobject string, all bool, limit int, version float64) (string, error) {
	if !sfObjectRe.MatchString(sobject) {
		return "", fmt.Errorf("invalid SOQL object name: %s", sobject)
	}
	var fields []string
	switch f := opts["fields"].(type) {
	case string:
		if f != "" {
			for _, x := range strings.Split(f, ",") {
				fields = append(fields, strings.TrimSpace(x))
			}
		}
	case []interface{}:
		for _, x := range f {
			fields = append(fields, toString(x))
		}
	}
	for _, f := range fields {
		if !sfFieldRe.MatchString(f) {
			return "", fmt.Errorf("invalid SOQL field name: %s", f)
		}
	}
	if len(fields) == 0 {
		fields = strings.Split(firstNonEmpty(sfDefaultFields[sobject], "id,LastModifiedDate"), ",")
	}
	var conds []string
	for _, x := range asList(asMap(opts["conditionsUi"])["conditionValues"]) {
		m := asMap(x)
		field := toString(m["field"])
		if !sfFieldRe.MatchString(field) {
			return "", fmt.Errorf("invalid SOQL field name: %s", field)
		}
		opName := toString(m["operation"])
		op, ok := sfOps[strings.ToUpper(strings.Join(strings.Fields(opName), " "))]
		if !ok {
			return "", fmt.Errorf("invalid SOQL operator: %s", opName)
		}
		val, err := sfValue(m["value"], version)
		if err != nil {
			return "", err
		}
		conds = append(conds, field+" "+op+" "+val)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	q := "SELECT " + strings.Join(fields, ",") + " FROM " + sobject + " " + where
	if !all {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	return q, nil
}

// sfValue formats a SOQL literal (versions before 1.1 leave numeric strings
// unquoted).
func sfValue(v interface{}, version float64) (string, error) {
	switch t := v.(type) {
	case nil:
		return "null", nil
	case bool, float64, int, int64, json.Number:
		return toString(t), nil
	case []interface{}:
		parts := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok {
				parts = append(parts, "'"+sfEscaper.Replace(s)+"'")
			} else {
				parts = append(parts, toString(x))
			}
		}
		return "(" + strings.Join(parts, ",") + ")", nil
	case string:
		s := strings.TrimSpace(t)
		if sfDateLitRe.MatchString(strings.ToUpper(s)) {
			return strings.ToUpper(s), nil
		}
		if sfDateTimeRe.MatchString(s) {
			return s, nil
		}
		if version < 1.1 && sfNumberRe.MatchString(s) {
			return s, nil
		}
		return "'" + sfEscaper.Replace(s) + "'", nil
	}
	return "", fmt.Errorf("unsupported value type: %T", v)
}
