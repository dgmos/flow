package n8n

import (
	"testing"
)

func seedRows() []map[string]interface{} {
	return []map[string]interface{}{
		nd("Start", "manualTrigger", 1, nil),
		nd("Seed", "set", 3.4, map[string]interface{}{"mode": "raw", "jsonOutput": `{"rows":[{"n":1},{"n":2},{"n":3},{"n":4},{"n":5}]}`}),
		nd("Rows", "splitOut", 1, map[string]interface{}{"fieldToSplitOut": "rows"}),
		nd("Split", "splitInBatches", 1, map[string]interface{}{"batchSize": 2, "options": map[string]interface{}{}}),
		nd("Work", "code", 2, map[string]interface{}{"jsCode": "return $input.all().map(i => ({json: {n: i.json.n * 10}}));"}),
	}
}

// Split In Batches v1 with the classic "noItemsLeft" IF ending the loop.
func TestSplitInBatchesV1NoItemsLeft(t *testing.T) {
	nodes := append(seedRows(),
		nd("More?", "if", 1, map[string]interface{}{"conditions": map[string]interface{}{"string": []interface{}{
			map[string]interface{}{"value1": `={{$node["Split"].context["noItemsLeft"] + ""}}`, "value2": "true"}}}}),
		nd("Done", "set", 3.4, map[string]interface{}{"assignments": map[string]interface{}{"assignments": []interface{}{
			assign("last", "number", "={{ $json.n }}"),
			assign("runs", "number", `={{ $node["Split"].context["currentRunIndex"] }}`)}}}),
	)
	data := wfJSON(t, nodes, "Start->Seed", "Seed->Rows", "Rows->Split", "Split->Work", "Work->More?", "More?:0->Done", "More?:1->Split")
	_, rep, _ := Import(data, ImportOptions{})
	if !rep.Runnable {
		t.Fatalf("not runnable: %+v", rep.Blocking)
	}
	_, steps := newHarness(t, nil).run(t, data, "")
	got := itemsOf(t, steps["Done"], 0)
	if len(got) != 1 || got[0]["last"] != 50.0 || got[0]["runs"] != 2.0 {
		t.Fatalf("done: %v, want the last batch [50] after 3 runs", got)
	}
}

// Split In Batches v2 re-entered directly by the body; the loop ends when
// the node emits nothing. Every batch also flows out of the loop.
func TestSplitInBatchesV2EndsWhenEmpty(t *testing.T) {
	nodes := seedRows()
	nodes[3]["typeVersion"] = 2
	nodes = append(nodes, nd("Out", "noOp", 1, nil))
	data := wfJSON(t, nodes, "Start->Seed", "Seed->Rows", "Rows->Split", "Split->Work", "Work->Split", "Work->Out")
	_, steps := newHarness(t, nil).run(t, data, "")
	got := itemsOf(t, steps["Out"], 0)
	if len(got) != 5 || got[0]["n"] != 10.0 || got[4]["n"] != 50.0 {
		t.Fatalf("out: %v, want all 5 items in order", got)
	}
}

// Loop Over Items whose body node is also fed from outside the loop (here
// it runs before the loop too): the loop runs node by node like in n8n.
func TestLoopOverItemsCrossingEdges(t *testing.T) {
	nodes := seedRows()
	nodes[3]["typeVersion"] = 3
	nodes = append(nodes, nd("Done", "noOp", 1, nil))
	data := wfJSON(t, nodes, "Start->Seed", "Seed->Rows", "Rows->Work", "Work->Split", "Split:1->Work", "Split:0->Done")
	_, rep, _ := Import(data, ImportOptions{})
	if !rep.Runnable || len(rep.Warnings) == 0 {
		t.Fatalf("runnable=%v blocking=%+v warnings=%+v", rep.Runnable, rep.Blocking, rep.Warnings)
	}
	_, steps := newHarness(t, nil).run(t, data, "")
	got := itemsOf(t, steps["Done"], 0)
	if len(got) != 5 || got[0]["n"] != 100.0 || got[4]["n"] != 500.0 {
		t.Fatalf("done: %v, want 100..500", got)
	}
}
