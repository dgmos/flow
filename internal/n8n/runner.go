package n8n

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/models"
)

// ---------------------------------------------------------------- loop extraction

type loopSpec struct {
	Nodes   []models.Node `json:"nodes"`
	Edges   []models.Edge `json:"edges"`
	Entries []models.Edge `json:"entries"` // loop node port "1" -> body
	Returns []models.Edge `json:"returns"` // body -> loop node
}

const cfgLoop = "__loop"

// cfgLoopCycle marks a Loop Over Items node whose body is wired to nodes
// outside the loop: it runs as a stateful node inside a cyclic region,
// like n8n executes it.
const cfgLoopCycle = "__loopCycle"

// extractLoops moves the body of every Loop Over Items (v3) node into the
// node's config so the engine graph stays acyclic. Inner loops go first.
func extractLoops(wf *models.Workflow, rep *Report) error {
	for {
		loops := map[string]bool{}
		for _, n := range wf.Nodes {
			if n.Type == "n8n-nodes-base.splitInBatches" && toFloatDefault(n.Config[cfgVersion], 1) >= 3 {
				_, done := n.Config[cfgLoop]
				_, cyclic := n.Config[cfgLoopCycle]
				if !done && !cyclic {
					loops[n.ID] = true
				}
			}
		}
		if len(loops) == 0 {
			return nil
		}
		// Pick the loop with the smallest body (innermost).
		best, bestBody := "", map[string]bool(nil)
		ids := make([]string, 0, len(loops))
		for id := range loops {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			body := loopBody(wf, id)
			if best == "" || len(body) < len(bestBody) {
				best, bestBody = id, body
			}
		}
		if err := rewriteLoop(wf, best, bestBody, rep); err != nil {
			return err
		}
	}
}

func loopBody(wf *models.Workflow, loopID string) map[string]bool {
	body := map[string]bool{}
	var queue []string
	for _, e := range wf.Edges {
		if e.SourceID == loopID && e.Condition == "1" && e.TargetID != loopID {
			queue = append(queue, e.TargetID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if body[id] || id == loopID {
			continue
		}
		body[id] = true
		for _, e := range wf.Edges {
			if e.SourceID == id {
				queue = append(queue, e.TargetID)
			}
		}
	}
	return body
}

func rewriteLoop(wf *models.Workflow, loopID string, body map[string]bool, rep *Report) error {
	var loopNode *models.Node
	names := map[string]string{}
	for i := range wf.Nodes {
		names[wf.Nodes[i].ID] = wf.Nodes[i].Name
		if wf.Nodes[i].ID == loopID {
			loopNode = &wf.Nodes[i]
		}
	}
	for _, e := range wf.Edges {
		if body[e.TargetID] && !body[e.SourceID] && (e.SourceID != loopID || e.Condition != "1") {
			// An edge from outside into the body: run the loop as a
			// stateful node in a cyclic region instead.
			loopNode.Config[cfgLoopCycle] = true
			rep.warn(&rawNode{Name: names[loopID]}, "Loop Over Items %q: edge from %q into the loop body; the loop runs node by node like n8n", names[loopID], names[e.SourceID])
			return nil
		}
	}
	spec := loopSpec{}
	var kept []models.Edge
	for _, e := range wf.Edges {
		inBodySrc, inBodyDst := body[e.SourceID], body[e.TargetID]
		switch {
		case e.SourceID == loopID && e.Condition == "1" && e.TargetID == loopID:
			// loop port wired straight back: no body
		case e.SourceID == loopID && e.Condition == "1":
			spec.Entries = append(spec.Entries, e)
		case inBodySrc && e.TargetID == loopID:
			spec.Returns = append(spec.Returns, e)
		case inBodySrc && inBodyDst:
			spec.Edges = append(spec.Edges, e)
		case inBodySrc || inBodyDst:
			// Edge crossing the loop boundary other than through the loop node.
			rep.Blocking = append(rep.Blocking, Issue{Node: names[e.SourceID], Message: fmt.Sprintf("loop %q: edge to %q crosses the loop boundary", names[loopID], names[e.TargetID])})
		default:
			kept = append(kept, e)
		}
	}
	var keptNodes []models.Node
	for _, n := range wf.Nodes {
		if body[n.ID] {
			delete(n.Config, cfgEntry)
			spec.Nodes = append(spec.Nodes, n)
		} else {
			keptNodes = append(keptNodes, n)
		}
	}
	wf.Edges = kept
	spec.Nodes, spec.Edges = collapseCycles(spec.Nodes, spec.Edges, rep)
	b, _ := json.Marshal(spec)
	var asMap map[string]interface{}
	_ = json.Unmarshal(b, &asMap)
	loopNode.Config[cfgLoop] = asMap
	// loopNode points into wf.Nodes; copy the updated config to keptNodes.
	for i := range keptNodes {
		if keptNodes[i].ID == loopID {
			keptNodes[i].Config = loopNode.Config
		}
	}
	wf.Nodes = keptNodes
	return nil
}

// ---------------------------------------------------------------- subgraph runner

type subgraph struct {
	nodes map[string]*models.Node
	order []string
	edges []models.Edge
}

func newSubgraph(nodes []models.Node, edges []models.Edge) (*subgraph, error) {
	sg := &subgraph{nodes: map[string]*models.Node{}, edges: edges}
	indeg := map[string]int{}
	for i := range nodes {
		sg.nodes[nodes[i].ID] = &nodes[i]
		indeg[nodes[i].ID] = 0
	}
	for _, e := range edges {
		if _, ok := sg.nodes[e.TargetID]; ok {
			if _, ok := sg.nodes[e.SourceID]; ok {
				indeg[e.TargetID]++
			}
		}
	}
	var queue []string
	for i := range nodes {
		if indeg[nodes[i].ID] == 0 {
			queue = append(queue, nodes[i].ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sg.order = append(sg.order, id)
		for _, e := range edges {
			if e.SourceID == id {
				if _, ok := sg.nodes[e.TargetID]; ok {
					indeg[e.TargetID]--
					if indeg[e.TargetID] == 0 {
						queue = append(queue, e.TargetID)
					}
				}
			}
		}
	}
	if len(sg.order) != len(nodes) {
		return nil, errors.New("subgraph contains a cycle")
	}
	return sg, nil
}

type depthKey struct{}

// run executes the subgraph once. seeds feed external input to nodes.
// Results are recorded in d.overlay; the IDs of executed nodes are
// returned in execution order.
func (sg *subgraph) run(ctx context.Context, reg *engine.Registry, d *execData, seeds map[string][]engine.PortInput) ([]string, error) {
	depth, _ := ctx.Value(depthKey{}).(int)
	if depth > 25 {
		return nil, errors.New("maximum sub-workflow/loop nesting depth exceeded")
	}
	ctx = context.WithValue(ctx, depthKey{}, depth+1)
	ctx = withOverlay(ctx, d)

	outputs := map[string]parsedPorts{}
	settled := map[string]bool{}
	var executed []string
	for _, id := range sg.order {
		n := sg.nodes[id]
		inputs := append([]engine.PortInput(nil), seeds[id]...)
		pending := false
		hasPred := false
		for _, e := range sg.edges {
			if e.TargetID != id {
				continue
			}
			if _, inside := sg.nodes[e.SourceID]; !inside {
				continue
			}
			hasPred = true
			if !settled[e.SourceID] {
				pending = true
				continue
			}
			po, ok := outputs[e.SourceID]
			if !ok {
				continue // skipped
			}
			key := e.Condition
			if key == "" {
				key = "0"
			}
			if data, ok := po[key]; ok {
				inputs = append(inputs, engine.PortInput{FromNode: e.SourceID, FromPort: key, ToPort: e.TargetInput, Data: data})
			}
		}
		if pending {
			return nil, fmt.Errorf("internal: node %s not ready", n.Name)
		}
		settled[id] = true
		if len(inputs) == 0 && (hasPred || len(seeds) > 0) {
			continue // dead path
		}
		ex, err := reg.Get(n.Type)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", n.Name, err)
		}
		var input string
		if b, ok := ex.(engine.InputBuilder); ok && len(inputs) > 0 {
			if input, err = b.BuildInput(inputs); err != nil {
				return nil, err
			}
		}
		out, err := ex.Execute(ctx, n.Config, input)
		if err != nil {
			if err.Error() == engine.WaitSignal || err.Error() == engine.WaitHuman {
				return nil, fmt.Errorf("node %q waits for an external event, which is not supported inside loops or sub-workflows", n.Name)
			}
			return nil, fmt.Errorf("node %q: %w", n.Name, err)
		}
		outputs[id] = parsePorts(out)
		d.overlay[id] = &stepView{executed: true, input: parseNodeInput(input), outputs: decodeOutput(out)}
		executed = append(executed, id)
	}
	return executed, nil
}

type parsedPorts map[string]json.RawMessage

func parsePorts(out string) parsedPorts {
	var env struct {
		Ports map[string]json.RawMessage `json:"__ports"`
	}
	if json.Unmarshal([]byte(out), &env) != nil || env.Ports == nil {
		return parsedPorts{"0": json.RawMessage(out)}
	}
	return env.Ports
}

// ---------------------------------------------------------------- Loop Over Items

func splitInBatchesNode(opts *Options) nodeFunc {
	return func(ec *execCtx) ([][]Item, error) {
		if ec.version < 3 {
			return splitInBatchesV1(ec)
		}
		if toBool(ec.node.Config[cfgLoopCycle]) {
			return splitInBatchesV3Stateful(ec)
		}
		rawSpec, ok := ec.node.Config[cfgLoop]
		if !ok {
			return nil, permanentError{"Loop Over Items: loop body not prepared (import the workflow with the n8n importer)"}
		}
		var spec loopSpec
		b, _ := json.Marshal(rawSpec)
		if err := json.Unmarshal(b, &spec); err != nil {
			return nil, err
		}
		sg, err := newSubgraph(spec.Nodes, spec.Edges)
		if err != nil {
			return nil, err
		}
		// Body nodes are visible to expressions by name.
		for i := range spec.Nodes {
			n := &spec.Nodes[i]
			ec.data.byID[n.ID] = n
			ec.data.byName[n.Name] = n
		}
		eng := engine.EngineFromContext(ec.ctx)
		reg := registryFrom(eng)
		if reg == nil {
			return nil, errors.New("loop Over Items: no engine registry in context")
		}
		size := int(toFloatDefault(mustParam(ec, "batchSize", 1.0), 1))
		if size < 1 {
			size = 1
		}
		items := ec.items()
		var done []Item
		for start := 0; start < len(items); start += size {
			end := start + size
			if end > len(items) {
				end = len(items)
			}
			batch := make([]Item, 0, end-start)
			for i := start; i < end; i++ {
				it := items[i]
				it.PairedItem = paired(i)
				batch = append(batch, it)
			}
			for _, n := range spec.Nodes {
				delete(ec.data.overlay, n.ID) // results of the previous batch
			}
			ec.data.overlay[ec.node.ID] = &stepView{executed: true, input: ec.input, outputs: map[int][]Item{1: batch}}
			raw, _ := json.Marshal(batch)
			seeds := map[string][]engine.PortInput{}
			for _, e := range spec.Entries {
				seeds[e.TargetID] = append(seeds[e.TargetID], engine.PortInput{FromNode: ec.node.ID, FromPort: "1", ToPort: e.TargetInput, Data: raw})
			}
			ran, err := sg.run(ec.ctx, reg, ec.data, seeds)
			if err != nil {
				return nil, fmt.Errorf("loop batch %d: %w", start/size+1, err)
			}
			executed := map[string]bool{}
			for _, id := range ran {
				executed[id] = true
			}
			for _, r := range spec.Returns {
				if v, ok := ec.data.overlay[r.SourceID]; ok && executed[r.SourceID] {
					done = append(done, v.outputs[atoiDefault(r.Condition, 0)]...)
				}
			}
		}
		// Record the last run of each body node for later $('Node') lookups.
		runs := map[string]interface{}{}
		for _, n := range spec.Nodes {
			if v, ok := ec.data.overlay[n.ID]; ok {
				runs[n.ID] = map[string]interface{}{"name": n.Name, "outputs": v.outputs}
			}
		}
		ec.extra = map[string]interface{}{"__nodeRuns": runs}
		out := make([]Item, len(done))
		for i, it := range done {
			it.PairedItem = paired(i % maxInt(1, len(items)))
			out[i] = it
		}
		return [][]Item{out, nil}, nil
	}
}

// splitInBatchesV1 is Split In Batches v1/v2: a stateful node with one
// output that is re-entered by a cycle. The first run stores the input and
// emits the first batch; each later run emits the next batch and ignores its
// input; once no items are left it emits nothing, ending that branch. Its
// state is n8n's node context ($node["X"].context.noItemsLeft), which lives
// for the execution of the enclosing loop.
func splitInBatchesV1(ec *execCtx) ([][]Item, error) {
	size := int(toFloatDefault(mustParam(ec, "batchSize", 10.0), 10))
	if size < 1 {
		size = 1
	}
	c := ec.data.nodeCtx[ec.node.ID]
	reset, _ := ec.boolean(0, "options.reset", false)
	var queue []Item
	if c == nil || reset {
		c = map[string]interface{}{"currentRunIndex": 0.0}
		ec.data.nodeCtx[ec.node.ID] = c
		for i, it := range ec.items() {
			it.PairedItem = paired(i)
			queue = append(queue, it)
		}
		c["maxRunIndex"] = float64((len(queue) + size - 1) / size)
	} else {
		queue, _ = c["items"].([]Item)
		c["currentRunIndex"] = toFloatDefault(c["currentRunIndex"], 0) + 1
	}
	n := size
	if n > len(queue) {
		n = len(queue)
	}
	batch := append([]Item(nil), queue[:n]...)
	queue = queue[n:]
	c["items"] = queue
	c["noItemsLeft"] = len(queue) == 0
	return [][]Item{batch}, nil
}

// splitInBatchesV3Stateful is Loop Over Items run node by node: the first
// run stores the input and emits the first batch on "loop" (port 1); each
// later run collects its input as processed and emits the next batch; when
// no items are left it emits everything processed on "done" (port 0) and
// resets.
func splitInBatchesV3Stateful(ec *execCtx) ([][]Item, error) {
	size := int(toFloatDefault(mustParam(ec, "batchSize", 1.0), 1))
	if size < 1 {
		size = 1
	}
	c := ec.data.nodeCtx[ec.node.ID]
	reset, _ := ec.boolean(0, "options.reset", false)
	var queue, processed []Item
	if c == nil || reset {
		c = map[string]interface{}{"currentRunIndex": 0.0}
		ec.data.nodeCtx[ec.node.ID] = c
		for i, it := range ec.items() {
			it.PairedItem = paired(i)
			queue = append(queue, it)
		}
		c["maxRunIndex"] = float64((len(queue) + size - 1) / size)
	} else {
		queue, _ = c["items"].([]Item)
		processed, _ = c["processed"].([]Item)
		processed = append(processed, ec.items()...)
		c["currentRunIndex"] = toFloatDefault(c["currentRunIndex"], 0) + 1
	}
	if len(queue) == 0 {
		delete(ec.data.nodeCtx, ec.node.ID)
		for i := range processed {
			processed[i].PairedItem = paired(i)
		}
		return [][]Item{processed, nil}, nil
	}
	n := size
	if n > len(queue) {
		n = len(queue)
	}
	batch := append([]Item(nil), queue[:n]...)
	c["items"], c["processed"] = queue[n:], processed
	c["noItemsLeft"] = len(queue) == n
	return [][]Item{nil, batch}, nil
}

// persistedContext is the part of a node context recorded in a loop step's
// output for later steps: everything except Split In Batches' pending items.
func persistedContext(c map[string]interface{}) map[string]interface{} {
	if c == nil {
		return nil
	}
	out := make(map[string]interface{}, len(c))
	for k, v := range c {
		if k != "items" && k != "processed" {
			out[k] = v
		}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func mustParam(ec *execCtx, path string, def interface{}) interface{} {
	v, err := ec.param(0, path, def)
	if err != nil {
		return def
	}
	return v
}

func registryFrom(e *engine.Engine) *engine.Registry {
	if e == nil {
		return nil
	}
	return e.NodeRegistry()
}

// ---------------------------------------------------------------- Execute Workflow

func executeWorkflowNode(opts *Options) nodeFunc {
	return func(ec *execCtx) ([][]Item, error) {
		eng := engine.EngineFromContext(ec.ctx)
		if eng == nil {
			return nil, errors.New("execute Workflow: no engine in context")
		}
		child, err := loadChildWorkflow(ec, eng, opts)
		if err != nil {
			return nil, err
		}
		items := ec.items()
		if mapped, err := mapWorkflowInputs(ec, items); err != nil {
			return nil, err
		} else if mapped != nil {
			items = mapped
		}
		if wait, _ := ec.boolean(0, "options.waitForSubWorkflow", true); !wait {
			trigger := ""
			for _, n := range child.Nodes {
				if n.Type == "n8n-nodes-base.executeWorkflowTrigger" {
					trigger = n.Name
				}
			}
			raw, _ := json.Marshal(map[string]interface{}{TriggerKey: trigger, "items": items})
			ns := engine.NamespaceFromContext(ec.ctx)
			if _, err := eng.StartWorkflow(ec.ctx, ns, child.ID, string(raw)); err != nil {
				return nil, fmt.Errorf("execute Workflow: %w", err)
			}
			return passthrough(ec)
		}
		mode, _ := ec.str(0, "mode", "once")
		if mode == "each" {
			var out []Item
			for i, it := range items {
				res, err := runChildWorkflow(ec.ctx, eng, child, []Item{it}, opts)
				if err != nil {
					return nil, err
				}
				for _, r := range res {
					r.PairedItem = paired(i)
					out = append(out, r)
				}
			}
			return [][]Item{out}, nil
		}
		res, err := runChildWorkflow(ec.ctx, eng, child, items, opts)
		if err != nil {
			return nil, err
		}
		for i := range res {
			res[i].PairedItem = paired(i % maxInt(1, len(items)))
		}
		return [][]Item{res}, nil
	}
}

func mapWorkflowInputs(ec *execCtx, items []Item) ([]Item, error) {
	if _, ok := ec.raw("workflowInputs.value"); !ok {
		return nil, nil
	}
	out := make([]Item, len(items))
	for i := range items {
		v, err := ec.param(i, "workflowInputs.value", map[string]interface{}{})
		if err != nil {
			return nil, err
		}
		m, _ := v.(map[string]interface{})
		if len(m) == 0 {
			return nil, nil
		}
		out[i] = Item{JSON: m, PairedItem: paired(i)}
	}
	return out, nil
}

func loadChildWorkflow(ec *execCtx, eng *engine.Engine, opts *Options) (*models.Workflow, error) {
	source, _ := ec.str(0, "source", "database")
	switch source {
	case "parameter":
		js, _ := ec.str(0, "workflowJson", "")
		wf, rep, err := Import([]byte(js), ImportOptions{ID: "inline-" + ec.node.ID})
		if err != nil {
			return nil, fmt.Errorf("execute Workflow: %w", err)
		}
		if !rep.Runnable {
			return nil, fmt.Errorf("execute Workflow: inline workflow is not supported: %s", rep.Blocking[0].Message)
		}
		return wf, nil
	case "database", "":
	default:
		return nil, permanentError{fmt.Sprintf("Execute Workflow: source %q is not supported", source)}
	}
	v, err := ec.param(0, "workflowId", "")
	if err != nil {
		return nil, err
	}
	id := toString(v)
	if m, ok := v.(map[string]interface{}); ok {
		id = toString(m["value"])
	}
	if id == "" {
		return nil, permanentError{"Execute Workflow: workflow ID is empty"}
	}
	if id == ec.data.wf.ID || id == toString(n8nMeta(ec.data.wf)["id"]) {
		return ec.data.wf, nil
	}
	if opts != nil && opts.WorkflowResolver != nil {
		id = opts.WorkflowResolver(id)
	}
	wf, err := eng.WorkflowDefinition(ec.ctx, engine.NamespaceFromContext(ec.ctx), id)
	if err != nil {
		return nil, permanentError{fmt.Sprintf("Execute Workflow: workflow %q not found: %v", id, err)}
	}
	return wf, nil
}

func n8nMeta(wf *models.Workflow) map[string]interface{} {
	m, _ := wf.Metadata["n8n"].(map[string]interface{})
	if m == nil {
		return map[string]interface{}{}
	}
	return m
}

// runChildWorkflow runs a workflow in-process starting from its Execute
// Workflow Trigger (or its default trigger) and returns the items of the
// last executed node.
func runChildWorkflow(ctx context.Context, eng *engine.Engine, wf *models.Workflow, items []Item, opts *Options) ([]Item, error) {
	sg, err := newSubgraph(wf.Nodes, wf.Edges)
	if err != nil {
		return nil, err
	}
	start := ""
	for _, n := range wf.Nodes {
		if n.Type == "n8n-nodes-base.executeWorkflowTrigger" {
			start = n.ID
			break
		}
	}
	if start == "" {
		start = toString(n8nMeta(wf)["defaultTrigger"])
	}
	if start == "" {
		return nil, fmt.Errorf("sub-workflow %q has no trigger to start from", wf.Name)
	}
	d := newExecData(&models.Execution{ID: engine.ExecutionIDFromContext(ctx) + "/" + wf.ID}, wf, nil, opts)
	raw, _ := json.Marshal(items)
	seeds := map[string][]engine.PortInput{start: {{FromNode: "__parent", FromPort: "0", Data: raw}}}
	order, err := sg.runFrom(ctx, eng.NodeRegistry(), d, seeds, start)
	if err != nil {
		return nil, fmt.Errorf("sub-workflow %q: %w", wf.Name, err)
	}
	for i := len(order) - 1; i >= 0; i-- {
		if v := d.overlay[order[i]]; v != nil {
			ports := make([]int, 0, len(v.outputs))
			for p := range v.outputs {
				ports = append(ports, p)
			}
			sort.Ints(ports)
			var out []Item
			for _, p := range ports {
				out = append(out, v.outputs[p]...)
			}
			if len(out) > 0 {
				return out, nil
			}
		}
	}
	return nil, nil
}

// runFrom runs only nodes reachable from start.
func (sg *subgraph) runFrom(ctx context.Context, reg *engine.Registry, d *execData, seeds map[string][]engine.PortInput, start string) ([]string, error) {
	reach := map[string]bool{start: true}
	for _, id := range sg.order {
		if !reach[id] {
			continue
		}
		for _, e := range sg.edges {
			if e.SourceID == id {
				reach[e.TargetID] = true
			}
		}
	}
	var nodes []models.Node
	for _, id := range sg.order {
		if reach[id] {
			nodes = append(nodes, *sg.nodes[id])
		}
	}
	sub, err := newSubgraph(nodes, sg.edges)
	if err != nil {
		return nil, err
	}
	// Entry gating does not apply when seeded directly.
	return sub.run(ctx, reg, d, seeds)
}
