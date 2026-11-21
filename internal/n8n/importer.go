package n8n

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/dgmos/flow/internal/models"
)

// ImportOptions configures Import.
type ImportOptions struct {
	// ID of the resulting flow workflow. Defaults to the n8n workflow id, or
	// a slug of its name.
	ID string
	// Name overrides the workflow name.
	Name string
}

// Issue is a problem found while importing.
type Issue struct {
	Node    string `json:"node,omitempty"`
	Type    string `json:"type,omitempty"`
	Message string `json:"message"`
}

// CredentialNeed is a credential the workflow requires.
type CredentialNeed struct {
	Node string `json:"node"`
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// TriggerSpec describes how a workflow is started.
type TriggerSpec struct {
	Node string `json:"node"`
	Type string `json:"type"`
	// Kind: manual | schedule | poll | webhook | chat | form | subworkflow | external
	Kind         string   `json:"kind"`
	Cron         []string `json:"cron,omitempty"`
	Path         string   `json:"path,omitempty"`
	Method       string   `json:"method,omitempty"`
	ResponseMode string   `json:"responseMode,omitempty"`
	Default      bool     `json:"default,omitempty"`
}

// Report summarizes how well a workflow is supported.
type Report struct {
	// Runnable is true when every node and feature is supported; the
	// workflow may still need credentials (see Credentials).
	Runnable    bool             `json:"runnable"`
	Nodes       int              `json:"nodes"`
	Supported   int              `json:"supportedNodes"`
	Blocking    []Issue          `json:"blocking,omitempty"`
	Warnings    []Issue          `json:"warnings,omitempty"`
	Credentials []CredentialNeed `json:"credentials,omitempty"`
	Triggers    []TriggerSpec    `json:"triggers,omitempty"`
}

func (r *Report) block(n *rawNode, format string, a ...interface{}) {
	is := Issue{Message: fmt.Sprintf(format, a...)}
	if n != nil {
		is.Node, is.Type = n.Name, n.Type
	}
	r.Blocking = append(r.Blocking, is)
}

func (r *Report) warn(n *rawNode, format string, a ...interface{}) {
	is := Issue{Message: fmt.Sprintf(format, a...)}
	if n != nil {
		is.Node, is.Type = n.Name, n.Type
	}
	r.Warnings = append(r.Warnings, is)
}

type rawNode struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Type             string                 `json:"type"`
	TypeVersion      flexFloat              `json:"typeVersion"`
	Position         []float64              `json:"position"`
	Parameters       map[string]interface{} `json:"parameters"`
	Credentials      map[string]interface{} `json:"credentials"`
	Disabled         bool                   `json:"disabled"`
	Notes            string                 `json:"notes"`
	AlwaysOutputData bool                   `json:"alwaysOutputData"`
	ExecuteOnce      bool                   `json:"executeOnce"`
	RetryOnFail      bool                   `json:"retryOnFail"`
	MaxTries         float64                `json:"maxTries"`
	WaitBetweenTries float64                `json:"waitBetweenTries"`
	OnError          string                 `json:"onError"`
	ContinueOnFail   bool                   `json:"continueOnFail"`
	WebhookID        string                 `json:"webhookId"`
}

// flexFloat accepts numbers and numeric strings (some exports quote
// typeVersion).
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	x, _ := toFloat(v)
	*f = flexFloat(x)
	return nil
}

type rawConn struct {
	Node  string `json:"node"`
	Type  string `json:"type"`
	Index int    `json:"index"`
}

type rawWorkflow struct {
	ID          interface{}                       `json:"id"`
	Name        string                            `json:"name"`
	Nodes       []rawNode                         `json:"nodes"`
	Connections map[string]map[string][][]rawConn `json:"connections"`
	Settings    map[string]interface{}            `json:"settings"`
	Workflow    json.RawMessage                   `json:"workflow"`
}

// parseRaw accepts a workflow export or the template API wrappers
// ({"workflow": {"workflow": {...}}}): it descends to the object that holds
// the workflow's "connections".
func parseRaw(data []byte) (*rawWorkflow, error) {
	cur := json.RawMessage(data)
	for depth := 0; depth < 4; depth++ {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(cur, &probe); err != nil {
			return nil, fmt.Errorf("invalid n8n workflow JSON: %w", err)
		}
		if _, ok := probe["connections"]; ok {
			break
		}
		inner, ok := probe["workflow"]
		if !ok {
			break
		}
		cur = inner
	}
	var w rawWorkflow
	if err := json.Unmarshal(cur, &w); err != nil {
		return nil, fmt.Errorf("invalid n8n workflow JSON: %w", err)
	}
	if len(w.Nodes) == 0 {
		return nil, errors.New("n8n workflow has no nodes")
	}
	return &w, nil
}

var triggerKinds = map[string]string{
	"n8n-nodes-base.manualTrigger":               "manual",
	"n8n-nodes-base.start":                       "manual",
	"n8n-nodes-base.scheduleTrigger":             "schedule",
	"n8n-nodes-base.cron":                        "schedule",
	"n8n-nodes-base.interval":                    "schedule",
	"n8n-nodes-base.webhook":                     "webhook",
	"n8n-nodes-base.formTrigger":                 "form",
	"@n8n/n8n-nodes-langchain.chatTrigger":       "chat",
	"@n8n/n8n-nodes-langchain.manualChatTrigger": "chat",
	"n8n-nodes-base.executeWorkflowTrigger":      "subworkflow",
	"n8n-nodes-base.errorTrigger":                "external",
	"n8n-nodes-base.telegramTrigger":             "webhook",
	"n8n-nodes-base.whatsAppTrigger":             "webhook",
	"n8n-nodes-base.gmailTrigger":                "poll",
	"n8n-nodes-base.googleSheetsTrigger":         "poll",
	"n8n-nodes-base.googleDriveTrigger":          "poll",
	"n8n-nodes-base.emailReadImap":               "poll",
	"n8n-nodes-base.microsoftOutlookTrigger":     "poll",
	"n8n-nodes-base.rssFeedReadTrigger":          "poll",
	"n8n-nodes-base.localFileTrigger":            "poll",
	"@n8n/n8n-nodes-langchain.mcpTrigger":        "mcp",
	"n8n-nodes-base.twilioTrigger":               "webhook",
	"n8n-nodes-base.notionTrigger":               "poll",
	"n8n-nodes-base.googleCalendarTrigger":       "poll",
	"n8n-nodes-base.airtableTrigger":             "poll",
	"n8n-nodes-base.evaluationTrigger":           "evaluation",
}

var defaultTriggerRank = map[string]int{"manual": 0, "chat": 1, "webhook": 2, "form": 3, "schedule": 4, "poll": 5, "external": 6, "subworkflow": 7, "mcp": 8, "evaluation": 9}

func isTriggerType(t string) bool {
	if _, ok := triggerKinds[t]; ok {
		return true
	}
	return strings.HasSuffix(strings.ToLower(t), "trigger")
}

// Import converts an n8n workflow into a flow workflow and reports what is
// and is not supported. The workflow is returned even when it is not fully
// runnable so callers can inspect it.
func Import(data []byte, opts ImportOptions) (*models.Workflow, *Report, error) {
	raw, err := parseRaw(data)
	if err != nil {
		return nil, nil, err
	}
	rep := &Report{}
	byName := map[string]*rawNode{}
	var nodes []*rawNode
	for i := range raw.Nodes {
		n := &raw.Nodes[i]
		if n.Type == "n8n-nodes-base.stickyNote" {
			continue
		}
		if n.Name == "" {
			n.Name = n.ID
		}
		if n.ID == "" {
			n.ID = n.Name
		}
		if _, dup := byName[n.Name]; dup {
			return nil, nil, fmt.Errorf("duplicate node name %q", n.Name)
		}
		byName[n.Name] = n
		nodes = append(nodes, n)
	}
	rep.Nodes = len(nodes)
	repairNodeReferences(nodes)

	// Split connections into main edges and AI attachments.
	var edges []models.Edge
	aiLinks := map[string]map[string][]string{} // root -> connType -> sub nodes
	isSub := map[string]bool{}
	srcNames := make([]string, 0, len(raw.Connections))
	for s := range raw.Connections {
		srcNames = append(srcNames, s)
	}
	sort.Strings(srcNames)
	for _, srcName := range srcNames {
		src, ok := byName[srcName]
		if !ok {
			continue
		}
		byType := raw.Connections[srcName]
		for connType, outputs := range byType {
			for outIdx, targets := range outputs {
				for _, t := range targets {
					dst, ok := byName[t.Node]
					if !ok {
						continue
					}
					if connType != "main" {
						if _, root := extraNodeTypes(&Options{})[src.Type]; (root || nodeTypes()[src.Type] != nil) && !aiSubNodeSupported(src.Type) && !vectorStoreTypes[short(src.Type)] {
							// e.g. Agent -> Chat Model: the connection is drawn the wrong way.
							rep.block(src, "%s connection to %q starts at %s, which is not a sub-node", connType, dst.Name, src.Type)
							continue
						}
						if aiLinks[dst.Name] == nil {
							aiLinks[dst.Name] = map[string][]string{}
						}
						aiLinks[dst.Name][connType] = append(aiLinks[dst.Name][connType], src.Name)
						isSub[src.Name] = true
						continue
					}
					edges = append(edges, models.Edge{
						ID:          fmt.Sprintf("%s:%d->%s:%d", src.ID, outIdx, dst.ID, t.Index),
						SourceID:    src.ID,
						TargetID:    dst.ID,
						Condition:   strconv.Itoa(outIdx),
						TargetInput: t.Index,
					})
				}
			}
		}
	}

	// Build flow nodes (AI sub-nodes are folded into their roots).
	var flowNodes []models.Node
	triggerRank, defaultTrigger := 99, ""
	hasMain := map[string]bool{}
	for _, e := range edges {
		hasMain[e.SourceID], hasMain[e.TargetID] = true, true
	}
	for _, n := range nodes {
		if isSub[n.Name] {
			continue
		}
		// AI sub-nodes (models, tools, memory, parsers) that are not attached
		// to a root node never run in n8n: drop them.
		if _, isExec := nodeTypes()[n.Type]; !isExec && aiSubNodeSupported(n.Type) && !vectorStoreTypes[short(n.Type)] {
			if _, isExtra := extraNodeTypes(&Options{})[n.Type]; !isExtra {
				if !hasMain[n.ID] {
					rep.warn(n, "AI sub-node is not connected to any agent or chain and is ignored")
					continue
				}
				rep.block(n, "AI sub-node %s cannot be used as a regular step", n.Type)
			}
		}
		fn := toFlowNode(n, byName, aiLinks, rep, map[string]bool{})
		flowNodes = append(flowNodes, fn)
		if isTriggerType(n.Type) && !n.Disabled {
			ts := triggerSpec(n, rep)
			rep.Triggers = append(rep.Triggers, ts)
			if r, ok := defaultTriggerRank[ts.Kind]; ok && r < triggerRank {
				triggerRank, defaultTrigger = r, n.ID
			}
		}
	}
	for i := range rep.Triggers {
		if byName[rep.Triggers[i].Node].ID == defaultTrigger {
			rep.Triggers[i].Default = true
		}
	}

	wf := &models.Workflow{
		ID:      opts.ID,
		Name:    raw.Name,
		Version: 1,
		Status:  models.WorkflowActive,
		Nodes:   flowNodes,
		Edges:   edges,
		Metadata: map[string]interface{}{
			"n8n": map[string]interface{}{"defaultTrigger": defaultTrigger, "id": toString(raw.ID)},
		},
	}
	if opts.Name != "" {
		wf.Name = opts.Name
	}
	if wf.ID == "" {
		wf.ID = toString(raw.ID)
	}
	if wf.ID == "" {
		wf.ID = slug(wf.Name)
	}
	if raw.Settings != nil {
		wf.Metadata["settings"] = raw.Settings
	}

	// Mark entry nodes (no incoming main edge) so only the selected trigger
	// runs, like n8n.
	indeg := map[string]int{}
	for _, e := range wf.Edges {
		indeg[e.TargetID]++
	}
	for i := range wf.Nodes {
		if indeg[wf.Nodes[i].ID] == 0 {
			wf.Nodes[i].Config[cfgEntry] = true
		}
	}

	if err := extractLoops(wf, rep); err != nil {
		return nil, nil, err
	}
	wf.Nodes, wf.Edges = collapseCycles(wf.Nodes, wf.Edges, rep)
	breakCycles(wf, rep)

	rep.Supported = rep.Nodes - countUnsupported(rep)
	rep.Runnable = len(rep.Blocking) == 0
	return wf, rep, nil
}

const cfgEntry = "__entry"

func countUnsupported(r *Report) int {
	seen := map[string]bool{}
	for _, b := range r.Blocking {
		if b.Node != "" {
			seen[b.Node] = true
		}
	}
	return len(seen)
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = fmt.Sprintf("n8n-%d", time.Now().UnixNano())
	}
	if len(s) > 36 {
		s = s[:36]
	}
	return s
}

func toFlowNode(n *rawNode, byName map[string]*rawNode, aiLinks map[string]map[string][]string, rep *Report, visiting map[string]bool) models.Node {
	typ := n.Type
	params := n.Parameters
	if params == nil {
		params = map[string]interface{}{}
	}
	if n.Disabled {
		rep.warn(n, "node is disabled; data passes through unchanged")
		typ = "n8n-nodes-base.noOp"
	}
	settings := map[string]interface{}{}
	if n.AlwaysOutputData {
		settings["alwaysOutputData"] = true
	}
	if n.ExecuteOnce {
		settings["executeOnce"] = true
	}
	if n.OnError != "" {
		settings["onError"] = n.OnError
	}
	if n.ContinueOnFail {
		settings["continueOnFail"] = true
	}
	cfg := map[string]interface{}{
		cfgParams:  params,
		cfgVersion: float64(n.TypeVersion),
		cfgID:      n.ID,
		cfgName:    n.Name,
	}
	if len(n.Credentials) > 0 {
		cfg[cfgCredentials] = n.Credentials
		for t, ref := range n.Credentials {
			name := ""
			if m, ok := ref.(map[string]interface{}); ok {
				name, _ = m["name"].(string)
			}
			rep.Credentials = append(rep.Credentials, CredentialNeed{Node: n.Name, Type: t, Name: name})
		}
	}
	if len(settings) > 0 {
		cfg[cfgSettings] = settings
	}
	if n.WebhookID != "" {
		cfg["__webhookId"] = n.WebhookID
	}
	if links, ok := aiLinks[n.Name]; ok && !visiting[n.Name] {
		visiting[n.Name] = true
		ai := map[string]interface{}{}
		for connType, subs := range links {
			var list []interface{}
			for _, s := range subs {
				sub := byName[s]
				sn := toFlowNode(sub, byName, aiLinks, rep, visiting)
				list = append(list, map[string]interface{}{"type": sn.Type, "config": sn.Config})
			}
			ai[connType] = list
		}
		cfg[cfgAI] = ai
	}
	checkNode(n, typ, params, rep)

	fn := models.Node{
		ID:     n.ID,
		Type:   typ,
		Name:   n.Name,
		Config: cfg,
		Meta:   map[string]interface{}{"n8n": true},
	}
	if len(n.Position) == 2 {
		fn.Meta["position"] = n.Position
	}
	if n.Notes != "" {
		fn.Meta["notes"] = n.Notes
	}
	if n.RetryOnFail {
		tries := n.MaxTries
		if tries <= 0 {
			tries = 3
		}
		wait := n.WaitBetweenTries
		if wait <= 0 {
			wait = 1000
		}
		iv := time.Duration(wait) * time.Millisecond
		fn.RetryPolicy = &models.RetryPolicy{MaxAttempts: int(tries) - 1, InitialInterval: iv, BackoffCoefficient: 1, MaximumInterval: iv}
	} else {
		fn.RetryPolicy = &models.RetryPolicy{MaxAttempts: 0, BackoffCoefficient: 1}
	}
	return fn
}

const cfgAI = "__ai"

// unsupportedVars are runtime features we do not provide.
var unsupportedExpr = []struct {
	re  *regexp.Regexp
	msg string
}{
	{regexp.MustCompile(`\$secrets\b`), "$secrets is not supported"},
	{regexp.MustCompile(`\$evaluateExpression\b`), "$evaluateExpression is not supported"},
}

var (
	requireRe = regexp.MustCompile(`\brequire\s*\(\s*(?:'([^']*)'|"([^"]*)"|` + "`([^`]*)`" + `)?`)
	helpersRe = regexp.MustCompile(`\bhelpers\s*\.\s*([A-Za-z_$][\w$]*)`)
	// Code node modules and this.helpers methods we provide.
	codeModules = map[string]bool{"crypto": true, "node:crypto": true, "util": true, "node:util": true, "youtube-transcript": true}
	codeHelpers = map[string]bool{"httpRequest": true, "request": true, "prepareBinaryData": true, "getBinaryDataBuffer": true, "binaryToBuffer": true, "returnJsonArray": true}
)

// codeFeatureIssues reports require() modules and this.helpers methods a
// Code node uses that are not available.
func codeFeatureIssues(code, nodeType string) []string {
	var out []string
	for _, m := range requireRe.FindAllStringSubmatch(code, -1) {
		name := m[1] + m[2] + m[3]
		if name == "" {
			out = append(out, "require() with a computed module name is not available")
		} else if !codeModules[name] && (nodeType != "@n8n/n8n-nodes-langchain.code" || !langchainModules[name]) {
			out = append(out, fmt.Sprintf("require('%s') is not available (crypto and util are)", name))
		}
	}
	for _, m := range helpersRe.FindAllStringSubmatch(code, -1) {
		if !codeHelpers[m[1]] {
			out = append(out, fmt.Sprintf("this.helpers.%s is not available in Code nodes", m[1]))
		}
	}
	return out
}

func checkNode(n *rawNode, typ string, params map[string]interface{}, rep *Report) {
	if !Supported(typ) {
		if isTriggerType(typ) {
			rep.block(n, "trigger %s is not supported (start the workflow from your app or a webhook instead)", typ)
		} else {
			rep.block(n, "node type %s is not supported", typ)
		}
		return
	}
	// Feature checks for supported nodes.
	get := func(path string) interface{} {
		var cur interface{} = params
		for _, p := range strings.Split(path, ".") {
			m, ok := cur.(map[string]interface{})
			if !ok {
				return nil
			}
			cur = m[p]
		}
		return cur
	}
	if restServices[strings.TrimSuffix(typ, "Tool")] != nil {
		checkResourceOps(n, typ, get, rep)
	}
	switch short(typ) {
	case "httpRequest":
		if get("options.proxy") != nil {
			rep.warn(n, "HTTP proxy option is ignored")
		}
		if toString(get("genericAuthType")) == "httpDigestAuth" || toString(get("genericAuthType")) == "oAuth1Api" {
			rep.block(n, "authentication %s is not supported", get("genericAuthType"))
		}
	case "merge":
		if toString(get("mode")) == "combine" && toString(get("combineBy")) == "combineBySql" {
			rep.block(n, "Merge SQL mode is not supported")
		}
	case "code":
		if l := toString(get("language")); l == "python" || l == "pythonNative" {
			rep.warn(n, "Python code runs on the worker's python3 only with Options.AllowLocalProcesses; install the packages it imports")
		}
	case "sort":
		if toString(get("type")) == "code" {
			rep.block(n, "Sort code mode is not supported")
		}
	case "removeDuplicates":
		switch op := toString(get("operation")); op {
		case "", "removeDuplicateInputItems", "removeItemsSeenInPreviousExecutions", "removeItemsRepeatedWithinPreviousExecutions", "clearDeduplicationHistory":
			if op == "removeItemsSeenInPreviousExecutions" || op == "removeItemsRepeatedWithinPreviousExecutions" {
				rep.warn(n, "deduplication history uses Options.PollState (process memory by default)")
			}
		default:
			rep.block(n, "Remove Duplicates operation %q is not supported", op)
		}
	case "respondToWebhook":
		if w := toString(get("respondWith")); w == "jwt" {
			rep.block(n, "respond with %s is not supported", w)
		}
	case "wait":
		if r := toString(get("resume")); r == "form" {
			rep.warn(n, "form pages need Options.ResumeBaseURL/ResumeSecret; the /form endpoint returns the next page definition")
		} else if r == "webhook" {
			rep.warn(n, "resume via webhook maps to engine signals (SignalExecution)")
		}
	case "telegram", "gmail", "slack":
		if op := toString(get("operation")); op == "sendAndWait" {
			checkSendAndWait(n, get, rep)
		}
	case "googleSheets":
		if n.TypeVersion < 4 {
			if _, err := sheetsLegacyParams(params, float64(n.TypeVersion)); err != nil {
				rep.block(n, "%s", err.Error())
			}
		}
	case "mcpClient", "mcpClientTool":
		if strings.HasPrefix(n.Type, "n8n-nodes-mcp.") {
			if ct := toString(get("connectionType")); ct == "" || ct == "cmd" {
				rep.warn(n, "this MCP client starts a local process (STDIO); it runs only with Options.AllowLocalProcesses")
			}
		}
	case "executeCommand":
		rep.warn(n, "Execute Command runs a shell command on the worker only with Options.AllowLocalProcesses")
	case "localFileTrigger":
		rep.warn(n, "Local File Trigger watches paths inside Options.FileSystemRoot (polled every 10 seconds)")
	case "chat":
		if toBool(get("waitUserReply")) {
			rep.block(n, "Respond to Chat waiting for the user's reply is not supported")
		}
	case "evaluationTrigger":
		rep.warn(n, "evaluation runs (dataset rows, metrics) are not supported; started directly, the Evaluation Trigger passes its input on")
	case "youTube":
		r, o := toString(get("resource")), toString(get("operation"))
		if r == "" {
			r = "channel"
		}
		if o == "" {
			o = map[string]string{"playlistItem": "add"}[r]
			if o == "" {
				o = "getAll"
			}
		}
		if strings.HasPrefix(r, "=") || strings.HasPrefix(o, "=") {
			break
		}
		if (r != "video" || !youTubeVideoOps[o]) && youTubeOther.op(r, o) == nil {
			rep.block(n, "YouTube %s/%s is not supported", r, o)
		}
	case "googleDrive":
		if n.TypeVersion < 3 {
			if _, err := driveV2Params(params); err != nil {
				rep.block(n, "%s", err.Error())
			}
		}
		if toString(get("resource")) == "drive" {
			rep.block(n, "shared drive management is not supported")
		}
	case "vectorStoreInMemory", "vectorStoreQdrant", "vectorStorePinecone", "vectorStoreSupabase", "vectorStorePGVector", "vectorStoreMongoDBAtlas":
		if m := toString(get("mode")); m == "update" && short(n.Type) == "vectorStoreInMemory" {
			rep.block(n, "the in-memory vector store has no document IDs to update")
		}
		if short(n.Type) == "vectorStoreInMemory" {
			rep.warn(n, "the in-memory vector store lives in process memory (lost on restart, not shared between instances)")
		}
	case "readWriteFile", "readBinaryFile", "writeBinaryFile":
		rep.warn(n, "file access requires Options.FileSystemRoot (a sandbox directory)")
	case "mySql":
		switch op := toString(get("operation")); op {
		case "", "executeQuery", "insert", "update", "upsert", "select", "deleteTable":
		default:
			rep.block(n, "MySQL operation %q is not supported", op)
		}
	case "spreadsheetFile":
		if f := toString(get("fileFormat")); toString(get("operation")) == "toFile" && f != "" && f != "csv" && f != "xlsx" {
			rep.block(n, "writing Spreadsheet File format %q is not supported (csv and xlsx are)", f)
		}
	case "extractFromFile":
		if op := toString(get("operation")); op == "rtf" {
			rep.block(n, "extracting %s files is not supported", op)
		} else if op == "xls" {
			rep.warn(n, "legacy binary .xls files are not supported; .xlsx and .ods files are read")
		}
	case "convertToFile":
		if op := toString(get("operation")); op == "xls" || op == "ods" || op == "rtf" || op == "iCal" {
			rep.block(n, "converting to %s is not supported", op)
		}
	case "emailSend":
		if op := toString(get("operation")); op == "sendAndWait" {
			checkSendAndWait(n, get, rep)
		}
	case "dataTable":
		rep.warn(n, "data tables use Options.DataTables (process memory by default); a table that does not exist yet is created on first use")
		checkResourceOps(n, typ, get, rep)
	case "hubspot", "googleGemini", "anthropic", "perplexity", "apify", "ssh", "microsoftSql":
		checkResourceOps(n, typ, get, rep)
	case "form":
		if op := toString(get("operation")); op == "" || op == "page" {
			rep.warn(n, "form pages need Options.ResumeBaseURL/ResumeSecret; the /form endpoint returns the next page definition")
		}
	}
	checkExpressions(n, params, rep)
}

func checkExpressions(n *rawNode, params map[string]interface{}, rep *Report) {
	var walk func(v interface{})
	reported := map[string]bool{}
	walk = func(v interface{}) {
		switch t := v.(type) {
		case string:
			code := ""
			if strings.HasPrefix(t, "=") {
				segs, err := splitTemplate(t[1:])
				if err != nil {
					rep.warn(n, "invalid expression (%v); this node fails when it runs, as in n8n", err)
					return
				}
				for _, s := range segs {
					if !s.expr {
						continue
					}
					code += s.text + "\n"
					if _, err := goja.Compile("expr", "(function(){ return (\n"+s.text+"\n); })()", false); err != nil {
						if _, err2 := goja.Compile("expr", "(function(){\n"+s.text+"\n})()", false); err2 != nil {
							msg := fmt.Sprintf("expression {{%s}} has a syntax error (%s); this node fails when it runs, as in n8n — the template probably expects a value to be filled in", shorten(s.text, 60), firstLine(err.Error()))
							if !reported[msg] {
								reported[msg] = true
								rep.warn(n, "%s", msg)
							}
						}
					}
				}
			} else if short(n.Type) == "code" || short(n.Type) == "function" || short(n.Type) == "functionItem" {
				code = t
			}
			for _, u := range unsupportedExpr {
				if code != "" && u.re.MatchString(code) && !reported[u.msg] {
					reported[u.msg] = true
					rep.block(n, "%s", u.msg)
				}
			}
			for _, msg := range codeFeatureIssues(code, n.Type) {
				if !reported[msg] {
					reported[msg] = true
					rep.block(n, "%s", msg)
				}
			}
		case map[string]interface{}:
			for _, x := range t {
				walk(x)
			}
		case []interface{}:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(params)
}

func checkSendAndWait(n *rawNode, get func(string) interface{}, rep *Report) {
	if rt := toString(get("responseType")); rt != "" && rt != "approval" && rt != "freeText" && rt != "customForm" {
		rep.block(n, "send-and-wait response type %q is not supported", rt)
		return
	}
	rep.warn(n, "send-and-wait needs Options.ResumeBaseURL and Options.ResumeSecret")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// triggerSpec describes how to start the workflow from a trigger node.
func triggerSpec(n *rawNode, rep *Report) TriggerSpec {
	kind, ok := triggerKinds[n.Type]
	if !ok {
		kind = "external"
		if serviceHooks[n.Type].handle != nil {
			kind = "webhook"
		}
	}
	ts := TriggerSpec{Node: n.Name, Type: n.Type, Kind: kind}
	p := n.Parameters
	switch kind {
	case "webhook":
		if serviceHooks[n.Type].handle != nil {
			ts.Path = n.WebhookID
			if ts.Path == "" {
				ts.Path = n.ID
			}
			ts.Method = "POST"
			ts.ResponseMode = "onReceived"
			if _, auto := hookRegistrars[n.Type]; auto {
				rep.warn(n, "events arrive at <handler base URL>/webhook/%s; set N8NInstallOptions.WebhookBaseURL to create the webhook in the service (it also stores the signing secret)", ts.Path)
			} else {
				rep.warn(n, "events arrive at <handler base URL>/webhook/%s; enter this URL in the service's webhook settings", ts.Path)
			}
			break
		}
		if rawBodyTriggers[n.Type] {
			ts.Path = n.WebhookID
			if ts.Path == "" {
				ts.Path = n.ID
			}
			ts.Method = "POST"
			ts.ResponseMode = "onReceived"
			rep.warn(n, "register <handler base URL>/webhook/%s as the webhook URL with the service (e.g. Telegram setWebhook)", ts.Path)
			break
		}
		ts.Path = toString(p["path"])
		if ts.Path == "" {
			ts.Path = n.WebhookID
		}
		ts.Method = strings.ToUpper(toString(p["httpMethod"]))
		if ts.Method == "" {
			ts.Method = "GET"
		}
		ts.ResponseMode = toString(p["responseMode"])
		if ts.ResponseMode == "" {
			ts.ResponseMode = "onReceived"
		}
	case "form", "chat":
		ts.Path = n.WebhookID
		if o, ok := p["options"].(map[string]interface{}); ok {
			ts.ResponseMode = toString(o["responseMode"])
		}
		if ts.ResponseMode == "" {
			ts.ResponseMode = "lastNode"
		}
	case "poll":
		ts.Cron = pollTimesCron(n.Parameters)
		if len(ts.Cron) == 0 && n.Type == "n8n-nodes-base.emailReadImap" {
			ts.Cron = []string{imapPollCron}
		}
		if _, set := n.Parameters["pollTimes"]; !set && n.Type == "n8n-nodes-base.localFileTrigger" {
			ts.Cron = []string{localFileTriggerPollCron}
		}
	case "schedule":
		crons, err := scheduleCrons(n)
		if err != nil {
			rep.warn(n, "schedule: %v", err)
		}
		ts.Cron = crons
	}
	return ts
}

// scheduleCrons converts n8n schedule rules to 6-field cron expressions.
func scheduleCrons(n *rawNode) ([]string, error) {
	p := n.Parameters
	if n.Type == "n8n-nodes-base.cron" {
		var out []string
		tt, _ := p["triggerTimes"].(map[string]interface{})
		for _, raw := range asList(tt["item"]) {
			it, _ := raw.(map[string]interface{})
			h, m := int(toFloatDefault(it["hour"], 14)), int(toFloatDefault(it["minute"], 0))
			switch toString(it["mode"]) {
			case "everyMinute":
				out = append(out, "0 * * * * *")
			case "everyHour":
				out = append(out, fmt.Sprintf("0 %d * * * *", m))
			case "everyWeek":
				out = append(out, fmt.Sprintf("0 %d %d * * %d", m, h, int(toFloatDefault(it["weekday"], 1))))
			case "everyMonth":
				out = append(out, fmt.Sprintf("0 %d %d %d * *", m, h, int(toFloatDefault(it["dayOfMonth"], 1))))
			case "everyX":
				v := int(toFloatDefault(it["value"], 2))
				if toString(it["unit"]) == "minutes" {
					out = append(out, fmt.Sprintf("0 */%d * * * *", v))
				} else {
					out = append(out, fmt.Sprintf("0 0 */%d * * *", v))
				}
			case "custom":
				out = append(out, normCron(toString(it["cronExpression"])))
			default: // everyDay
				out = append(out, fmt.Sprintf("0 %d %d * * *", m, h))
			}
		}
		if len(out) == 0 {
			out = append(out, "0 0 14 * * *")
		}
		return out, nil
	}
	if n.Type == "n8n-nodes-base.interval" {
		v := int(toFloatDefault(p["interval"], 1))
		switch toString(p["unit"]) {
		case "minutes":
			return []string{fmt.Sprintf("0 */%d * * * *", v)}, nil
		case "hours":
			return []string{fmt.Sprintf("0 0 */%d * * *", v)}, nil
		default:
			return []string{fmt.Sprintf("*/%d * * * * *", v)}, nil
		}
	}
	rule, _ := p["rule"].(map[string]interface{})
	intervals := asList(rule["interval"])
	if len(intervals) == 0 {
		intervals = []interface{}{map[string]interface{}{}}
	}
	var out []string
	var problems []string
	for _, raw := range intervals {
		iv, _ := raw.(map[string]interface{})
		field := toString(iv["field"])
		if field == "" {
			field = "days"
		}
		h := int(toFloatDefault(iv["triggerAtHour"], 0))
		m := int(toFloatDefault(iv["triggerAtMinute"], 0))
		switch field {
		case "seconds":
			out = append(out, fmt.Sprintf("*/%d * * * * *", int(toFloatDefault(iv["secondsInterval"], 30))))
		case "minutes":
			out = append(out, fmt.Sprintf("0 */%d * * * *", int(toFloatDefault(iv["minutesInterval"], 5))))
		case "hours":
			out = append(out, fmt.Sprintf("0 %d */%d * * *", m, int(toFloatDefault(iv["hoursInterval"], 1))))
		case "days":
			d := int(toFloatDefault(iv["daysInterval"], 1))
			if d == 1 {
				out = append(out, fmt.Sprintf("0 %d %d * * *", m, h))
			} else {
				out = append(out, fmt.Sprintf("0 %d %d */%d * *", m, h, d))
			}
		case "weeks":
			days := asList(iv["triggerAtDay"])
			var ds []string
			for _, d := range days {
				ds = append(ds, strconv.Itoa(int(toFloatDefault(d, 0))))
			}
			if len(ds) == 0 {
				ds = []string{"0"}
			}
			if w := toFloatDefault(iv["weeksInterval"], 1); w > 1 {
				problems = append(problems, fmt.Sprintf("every %v weeks runs weekly", w))
			}
			out = append(out, fmt.Sprintf("0 %d %d * * %s", m, h, strings.Join(ds, ",")))
		case "months":
			dom := int(toFloatDefault(iv["triggerAtDayOfMonth"], 1))
			out = append(out, fmt.Sprintf("0 %d %d %d */%d *", m, h, dom, int(toFloatDefault(iv["monthsInterval"], 1))))
		case "cronExpression":
			out = append(out, normCron(toString(iv["expression"])))
		default:
			problems = append(problems, "unknown interval "+field)
		}
	}
	if len(problems) > 0 {
		return out, errors.New(strings.Join(problems, "; "))
	}
	return out, nil
}

// normCron converts a 5-field cron to the engine's 6-field form.
func normCron(expr string) string {
	expr = strings.TrimSpace(strings.TrimPrefix(expr, "="))
	if len(strings.Fields(expr)) == 5 {
		return "0 " + expr
	}
	return expr
}

// breakCycles removes edges that would form cycles (unsupported loops).
func breakCycles(wf *models.Workflow, rep *Report) {
	adj := map[string][]int{}
	for i, e := range wf.Edges {
		adj[e.SourceID] = append(adj[e.SourceID], i)
	}
	names := map[string]string{}
	for _, n := range wf.Nodes {
		names[n.ID] = n.Name
	}
	state := map[string]int{}
	drop := map[int]bool{}
	var dfs func(u string)
	dfs = func(u string) {
		state[u] = 1
		for _, ei := range adj[u] {
			v := wf.Edges[ei].TargetID
			switch state[v] {
			case 1:
				drop[ei] = true
			case 0:
				dfs(v)
			}
		}
		state[u] = 2
	}
	for _, n := range wf.Nodes {
		if state[n.ID] == 0 {
			dfs(n.ID)
		}
	}
	if len(drop) == 0 {
		return
	}
	var kept []models.Edge
	for i, e := range wf.Edges {
		if drop[i] {
			rep.Blocking = append(rep.Blocking, Issue{Node: names[e.SourceID], Message: fmt.Sprintf("loop back to %q is not supported (only Loop Over Items loops are)", names[e.TargetID])})
			continue
		}
		kept = append(kept, e)
	}
	wf.Edges = kept
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// repairNodeReferences escapes quotes of node names inside $('…'),
// $("…") and $node[…] references (e.g. $('Create Sheet If It Doesn't
// Exist')), which n8n's editor would have escaped.
func repairNodeReferences(nodes []*rawNode) {
	type fix struct{ from, to string }
	var fixes []fix
	for _, n := range nodes {
		if !strings.ContainsAny(n.Name, `'"`) {
			continue
		}
		single := strings.ReplaceAll(strings.ReplaceAll(n.Name, `\`, `\\`), `'`, `\'`)
		double := strings.ReplaceAll(strings.ReplaceAll(n.Name, `\`, `\\`), `"`, `\"`)
		if strings.Contains(n.Name, "'") {
			fixes = append(fixes, fix{"$('" + n.Name + "')", "$('" + single + "')"}, fix{"$node['" + n.Name + "']", "$node['" + single + "']"})
		}
		if strings.Contains(n.Name, `"`) {
			fixes = append(fixes, fix{`$("` + n.Name + `")`, `$("` + double + `")`}, fix{`$node["` + n.Name + `"]`, `$node["` + double + `"]`})
		}
	}
	if len(fixes) == 0 {
		return
	}
	var walk func(v interface{}) interface{}
	walk = func(v interface{}) interface{} {
		switch t := v.(type) {
		case string:
			if strings.HasPrefix(t, "=") {
				for _, f := range fixes {
					t = strings.ReplaceAll(t, f.from, f.to)
				}
			}
			return t
		case map[string]interface{}:
			for k, x := range t {
				t[k] = walk(x)
			}
		case []interface{}:
			for i, x := range t {
				t[i] = walk(x)
			}
		}
		return v
	}
	for _, n := range nodes {
		walk(n.Parameters)
	}
}

// resourceOps lists the supported resource/operation pairs of nodes that
// implement only part of n8n's operations, with n8n's defaults.
var resourceOps = map[string]struct {
	defResource string
	defOps      map[string]string
	ops         map[string]string // resource -> operations, space-separated ("|" for Apify)
}{
	"dataTable": {"row", map[string]string{"row": "insert", "table": "list"}, map[string]string{
		"row":   "insert get getAll append deleteRows delete update upsert rowExists rowNotExists",
		"table": "list create clear update delete"}},
	"hubspot": {"contact", map[string]string{"contact": "upsert", "company": "create", "deal": "create", "engagement": "create", "ticket": "create", "contactList": "add", "owner": "getAll"}, map[string]string{
		"contact":     "upsert get getAll getRecentlyCreatedUpdated delete search",
		"company":     "create update get getAll getRecentlyCreatedUpdated getRecentlyModified getRecentlyCreated searchByDomain delete search",
		"deal":        "create update get getAll getRecentlyCreatedUpdated getRecentlyCreated getRecentlyModified delete search",
		"engagement":  "create get getAll delete",
		"ticket":      "create update get getAll delete",
		"contactList": "add remove",
		"owner":       "getAll"}},
	"googleGemini": {"text", map[string]string{"text": "message", "image": "analyze", "video": "analyze", "audio": "transcribe", "document": "analyze", "file": "upload"}, map[string]string{
		"text": "message", "image": "analyze generate edit", "video": "analyze generate download", "audio": "analyze transcribe", "document": "analyze", "file": "upload"}},
	"anthropic": {"text", map[string]string{"text": "message", "image": "analyze", "document": "analyze", "file": "upload"}, map[string]string{
		"text": "message", "image": "analyze", "document": "analyze", "file": "upload get list delete deleteFile"}},
	"perplexity":   {"chat", map[string]string{"chat": "complete"}, map[string]string{"chat": "complete"}},
	"ssh":          {"command", map[string]string{"command": "execute", "file": "upload"}, map[string]string{"command": "execute"}},
	"microsoftSql": {"", map[string]string{"": "insert"}, map[string]string{"": "executeQuery"}},
	"apify": {"Actors", map[string]string{"Actors": "Run actor", "Actor tasks": "Run task", "Actor runs": "Get user runs list", "Datasets": "Get items", "Key-Value Stores": "Get Key-Value Store Record",
		"actor": "run", "dataset": "getItems"}, map[string]string{
		"Actors":           "Run actor|Run actor and get dataset|Scrape single URL|Get last run|Get list of actors",
		"Actor tasks":      "Run task|Run task and get dataset|Get list of tasks",
		"Actor runs":       "Get run|Get runs|Get user runs list|Abort run",
		"Datasets":         "Get items",
		"Key-Value Stores": "Get Key-Value Store Record",
		"actor":            "run", "dataset": "getItems"}},
}

func checkResourceOps(n *rawNode, typ string, get func(string) interface{}, rep *Report) {
	if rs := restServices[strings.TrimSuffix(typ, "Tool")]; rs != nil {
		r, o := toString(get("resource")), toString(get("operation"))
		if r == "" {
			r = rs.defResource
		}
		if o == "" {
			o = rs.defOps[r]
		}
		if strings.HasPrefix(r, "=") || strings.HasPrefix(o, "=") || rs.supports(r, o) {
			return
		}
		rep.block(n, "%s %s/%s is not supported", n.Type[strings.LastIndex(n.Type, ".")+1:], r, o)
		return
	}
	spec, ok := resourceOps[short(typ)]
	if !ok {
		return
	}
	r, o := toString(get("resource")), toString(get("operation"))
	if r == "" {
		r = spec.defResource
	}
	if o == "" {
		o = spec.defOps[r]
	}
	if strings.HasPrefix(r, "=") || strings.HasPrefix(o, "=") {
		return // decided at run time
	}
	list, ok := spec.ops[r]
	sep := " "
	if spec.defResource == "Actors" {
		sep = "|" // Apify's operation names contain spaces
	}
	if ok {
		for _, x := range strings.Split(list, sep) {
			if x == o {
				return
			}
		}
	}
	rep.block(n, "%s %s/%s is not supported", n.Type[strings.LastIndex(n.Type, ".")+1:], r, o)
}
