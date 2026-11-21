package n8n

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"net/url"
	"strings"

	"github.com/dgmos/flow/internal/engine"
)

// resumeSignature authorizes resuming a waiting execution via its URL. When
// nodeID is set the URL only resumes that node's wait, so an old approval
// link cannot resume a later, different wait of the same execution.
func resumeSignature(secret, namespace, execID, nodeID string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(namespace + "\x00" + execID + "\x00" + nodeID))
	return hex.EncodeToString(m.Sum(nil))
}

func validResumeSignature(secret, namespace, execID, nodeID, sig string) bool {
	if secret == "" || sig == "" {
		return false
	}
	return hmac.Equal([]byte(resumeSignature(secret, namespace, execID, nodeID)), []byte(sig))
}

// resumeURL is $execution.resumeUrl: calling it resumes the execution's
// waiting step (Wait node, send-and-wait approvals, form pages).
func (o *Options) resumeURL(namespace, execID, nodeID string) string {
	return o.resumeURLWith(namespace, execID, nodeID, "")
}

// resumeURLWith also signs an approval answer ("true"/"false"), so a
// decline link can never be turned into an approval.
func (o *Options) resumeURLWith(namespace, execID, nodeID, approved string) string {
	if o == nil || o.ResumeBaseURL == "" || o.ResumeSecret == "" || execID == "" {
		return ""
	}
	u := strings.TrimRight(o.ResumeBaseURL, "/") + "/resume/" + url.PathEscape(execID) + "?"
	if nodeID != "" {
		u += "node=" + url.QueryEscape(nodeID) + "&"
	}
	if approved != "" {
		u += "approved=" + approved + "&"
	}
	return u + "sig=" + resumeSignature(o.ResumeSecret, namespace, execID, signedNode(nodeID, approved))
}

func signedNode(nodeID, approved string) string {
	if approved == "" {
		return nodeID
	}
	return nodeID + "\x00approved=" + approved
}

// resumeURL returns a URL that resumes this node's wait.
func (ec *execCtx) resumeURL() (string, error) {
	u := ec.opts.resumeURL(engine.NamespaceFromContext(ec.ctx), engine.ExecutionIDFromContext(ec.ctx), ec.node.ID)
	if u == "" {
		return "", permanentError{"waiting for a response requires Options.ResumeBaseURL and Options.ResumeSecret"}
	}
	return u, nil
}

// approval describes the links of a send-and-wait message.
type approval struct {
	approveURL, declineURL     string
	approveLabel, declineLabel string
	double                     bool
}

// sendAndWaitApproval prepares approve/decline links. Only the "approval"
// response type is supported.
func (ec *execCtx) sendAndWaitApproval(i int) (*approval, error) {
	u, err := ec.resumeURL()
	if err != nil {
		return nil, err
	}
	switch rt, _ := ec.str(i, "responseType", "approval"); rt {
	case "approval":
	case "freeText", "customForm":
		// One button that opens a form (served by the resume endpoint).
		return &approval{approveURL: u, approveLabel: mustStr(ec, i, "options.messageButtonLabel", "Respond")}, nil
	default:
		return nil, permanentError{fmt.Sprintf("send-and-wait response type %q is not supported", rt)}
	}
	ns, execID := engine.NamespaceFromContext(ec.ctx), engine.ExecutionIDFromContext(ec.ctx)
	a := &approval{approveURL: ec.opts.resumeURLWith(ns, execID, ec.node.ID, "true"), declineURL: ec.opts.resumeURLWith(ns, execID, ec.node.ID, "false")}
	a.double = mustStr(ec, i, "approvalOptions.values.approvalType", "single") == "double"
	a.approveLabel = mustStr(ec, i, "approvalOptions.values.approveLabel", "Approve")
	a.declineLabel = mustStr(ec, i, "approvalOptions.values.disapproveLabel", "Decline")
	return a, nil
}

func mustStr(ec *execCtx, i int, path, def string) string {
	s, err := ec.str(i, path, def)
	if err != nil || s == "" {
		return def
	}
	return s
}

func (a *approval) html(message string) string {
	btn := func(href, label, color string) string {
		return fmt.Sprintf(`<a href="%s" style="display:inline-block;padding:10px 18px;margin:4px;border-radius:6px;background:%s;color:#fff;text-decoration:none">%s</a>`,
			html.EscapeString(href), color, html.EscapeString(label))
	}
	b := "<div>" + message + "</div><div style=\"margin-top:16px\">" + btn(a.approveURL, a.approveLabel, "#2e7d32")
	if a.double {
		b += btn(a.declineURL, a.declineLabel, "#c62828")
	}
	return b + "</div>"
}

// ---------------------------------------------------------------- Form

// formNode implements Form pages (wait for the next submission) and the
// completion screen of n8n form flows.
func formNode(ec *execCtx) ([][]Item, error) {
	op, _ := ec.str(0, "operation", "page")
	switch op {
	case "completion":
		title, _ := ec.str(0, "completionTitle", "")
		msg, _ := ec.str(0, "completionMessage", "")
		with, _ := ec.str(0, "respondWith", "text")
		comp := map[string]interface{}{"title": title, "message": msg, "respondWith": with}
		if with == "redirect" {
			comp["redirectUrl"], _ = ec.str(0, "redirectUrl", "")
		}
		if with == "showText" {
			comp["text"], _ = ec.str(0, "responseText", "")
		}
		ec.extra = map[string]interface{}{"__formCompletion": comp}
		return passthrough(ec)
	case "page", "":
		if _, err := ec.resumeURL(); err != nil {
			return nil, err
		}
		return nil, errWaitSignal
	}
	return nil, permanentError{fmt.Sprintf("Form: operation %q is not supported", op)}
}

// formPageDefinition returns the fields of a waiting form page for hosts.
func formPageDefinition(params map[string]interface{}) map[string]interface{} {
	def := map[string]interface{}{}
	if fv, ok := params["formFields"].(map[string]interface{}); ok {
		def["fields"] = fv["values"]
	}
	if js, ok := params["jsonOutput"]; ok && toString(params["defineForm"]) == "json" {
		def["fieldsJson"] = js
	}
	if o, ok := params["options"].(map[string]interface{}); ok {
		def["title"] = o["formTitle"]
		def["description"] = o["formDescription"]
		def["buttonLabel"] = o["buttonLabel"]
	}
	// Wait nodes resumed by a form keep title and description at the top.
	for k, d := range map[string]string{"formTitle": "title", "formDescription": "description"} {
		if v, ok := params[k]; ok && def[d] == nil {
			def[d] = v
		}
	}
	return def
}

// responseFormHTML renders the page behind a free-text / custom-form
// send-and-wait link.
func responseFormHTML(params map[string]interface{}, action string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1"><title>Respond</title>`)
	b.WriteString(`<form method=post action="` + html.EscapeString(action) + `" style="font-family:sans-serif;max-width:480px;margin:40px auto;display:grid;gap:12px">`)
	if toString(params["responseType"]) == "customForm" {
		fv, _ := params["formFields"].(map[string]interface{})
		for _, raw := range asList(fv["values"]) {
			f, _ := raw.(map[string]interface{})
			label := toString(f["fieldLabel"])
			typ := toString(f["fieldType"])
			input := "text"
			switch typ {
			case "number", "email", "date", "password":
				input = typ
			}
			req := ""
			if toBool(f["requiredField"]) {
				req = " required"
			}
			if typ == "textarea" {
				b.WriteString(`<label>` + html.EscapeString(label) + `<textarea name="` + html.EscapeString(label) + `"` + req + ` style="width:100%"></textarea></label>`)
			} else {
				b.WriteString(`<label>` + html.EscapeString(label) + `<input type=` + input + ` name="` + html.EscapeString(label) + `"` + req + ` style="width:100%"></label>`)
			}
		}
	} else {
		b.WriteString(`<textarea name=text required rows=6 style="width:100%"></textarea>`)
	}
	b.WriteString(`<button type=submit>Submit</button></form>`)
	return b.String()
}
