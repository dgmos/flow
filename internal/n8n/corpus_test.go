//go:build n8ncorpus

package n8n

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dgmos/flow/internal/engine"
	"github.com/dgmos/flow/internal/storage/memory"
)

// TestCorpus imports every n8n workflow JSON in $N8N_CORPUS_DIR and reports
// how many are runnable and the most common blocking reasons.
//
//	N8N_CORPUS_DIR=/path/to/templates go test -tags n8ncorpus -run Corpus -v ./internal/n8n
func TestCorpus(t *testing.T) {
	dir := os.Getenv("N8N_CORPUS_DIR")
	if dir == "" {
		t.Skip("N8N_CORPUS_DIR not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	total, runnable, failed, startFail, exprWarn := 0, 0, 0, 0, 0
	reasons := map[string]int{}
	var perTemplate []map[string]bool
	reg := engine.NewRegistry()
	Register(reg, &Options{})
	eng := engine.NewEngine(memory.NewMemoryStorage(), reg)
	norm := regexp.MustCompile(`"[^"]*"|\d+(\.\d+)?`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		total++
		_, rep, err := Import(data, ImportOptions{ID: "corpus"})
		if err != nil {
			failed++
			t.Logf("import error %s: %v", filepath.Base(f), err)
			reasons["import error: "+err.Error()]++
			continue
		}
		for _, w := range rep.Warnings {
			if strings.HasPrefix(w.Message, "expression {{") || strings.HasPrefix(w.Message, "invalid expression") {
				exprWarn++
				t.Logf("expression problem in %s: %s", filepath.Base(f), w.Message)
				break
			}
		}
		if rep.Runnable {
			wf, _, _ := Import(data, ImportOptions{ID: filepath.Base(f)})
			ctx := context.Background()
			if err := eng.RegisterWorkflow(ctx, "c", wf); err != nil {
				startFail++
				t.Logf("register %s: %v", filepath.Base(f), err)
				continue
			}
			if _, err := eng.StartWorkflow(ctx, "c", wf.ID, "{}"); err != nil {
				startFail++
				t.Logf("start %s: %v", filepath.Base(f), err)
				continue
			}
			runnable++
			continue
		}
		seen := map[string]bool{}
		for _, b := range rep.Blocking {
			key := norm.ReplaceAllString(b.Message, "…")
			if b.Type != "" && (len(b.Message) > 9 && b.Message[:9] == "node type") {
				key = "node type " + b.Type
			}
			if !seen[key] {
				seen[key] = true
				reasons[key]++
			}
		}
		perTemplate = append(perTemplate, seen)
		if os.Getenv("N8N_CORPUS_VERBOSE") != "" {
			keys := make([]string, 0, len(seen))
			for k := range seen {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			t.Logf("blocked %s: %v", filepath.Base(f), keys)
		}
	}
	// Greedy: which blockers, once fixed, unlock the most templates.
	steps := 25
	if n, err := strconv.Atoi(os.Getenv("N8N_CORPUS_GREEDY")); err == nil && n > 0 {
		steps = n
	}
	fixed := map[string]bool{}
	cum := runnable
	for step := 0; step < steps; step++ {
		gain := map[string]int{}
		for _, s := range perTemplate {
			var open []string
			for k := range s {
				if !fixed[k] {
					open = append(open, k)
				}
			}
			if len(open) == 1 {
				gain[open[0]]++
			}
		}
		best, bg := "", 0
		for k, g := range gain {
			if g > bg || g == bg && k < best {
				best, bg = k, g
			}
		}
		if bg == 0 {
			break
		}
		fixed[best] = true
		cum += bg
		t.Logf("greedy %2d: +%-3d → %5.1f%%  %s", step+1, bg, 100*float64(cum)/float64(max(total, 1)), best)
	}
	t.Logf("runnable but failed to register/start: %d", startFail)
	t.Logf("runnable with an invalid expression (fails at that node, as in n8n): %d", exprWarn)
	t.Logf("templates: %d  runnable: %d (%.1f%%)  import errors: %d", total, runnable, 100*float64(runnable)/float64(max(total, 1)), failed)
	// What kind of work unblocks each template: a template counts under a
	// category when all of its blockers are in that category.
	cats := map[string]int{}
	for _, s := range perTemplate {
		kinds := map[string]bool{}
		for k := range s {
			kinds[blockerKind(k)] = true
		}
		if len(kinds) == 1 {
			for k := range kinds {
				cats["only "+k]++
			}
		} else {
			cats["mixed"]++
		}
	}
	for _, k := range []string{"only service node", "only service trigger", "only AI node", "only community node", "only feature", "mixed"} {
		t.Logf("blocked, %-22s %5d", k+":", cats[k])
	}
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range reasons {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	for i, r := range list {
		if i >= 40 && os.Getenv("N8N_CORPUS_VERBOSE") == "" {
			break
		}
		t.Logf("%4d  %s", r.v, r.k)
	}
}

// blockerKind classifies a normalized blocking reason.
func blockerKind(k string) string {
	switch {
	case strings.HasPrefix(k, "trigger "):
		return "service trigger"
	case strings.HasPrefix(k, "node type n8n-nodes-base."):
		if strings.HasSuffix(k, "Trigger") {
			return "service trigger"
		}
		return "service node"
	case strings.HasPrefix(k, "node type @n8n/n8n-nodes-langchain."):
		return "AI node"
	case strings.HasPrefix(k, "node type "):
		return "community node"
	}
	return "feature"
}
