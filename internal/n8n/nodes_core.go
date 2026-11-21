package n8n

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/dgmos/flow/internal/engine"
)

// passthrough returns the main input unchanged (NoOp, triggers, …).
func passthrough(ec *execCtx) ([][]Item, error) {
	items := ec.items()
	out := make([]Item, len(items))
	for i, it := range items {
		it.PairedItem = paired(i)
		out[i] = it
	}
	return [][]Item{out}, nil
}

// scheduleTrigger emits n8n's schedule item when started without input.
func scheduleTrigger(ec *execCtx) ([][]Item, error) {
	items := ec.items()
	if len(items) == 1 && len(items[0].JSON) == 0 {
		loc, err := loadZone(ec.opts.timezone(ec.data.wf))
		if err != nil {
			loc = time.UTC
		}
		now := time.Now().In(loc)
		items = []Item{{JSON: map[string]interface{}{
			"timestamp":     now.Format(time.RFC3339),
			"Readable date": now.Format("January 2nd 2006, 3:04:05 pm"),
			"Readable time": now.Format("3:04:05 pm"),
			"Day of week":   now.Weekday().String(),
			"Year":          fmt.Sprint(now.Year()),
			"Month":         now.Month().String(),
			"Day of month":  fmt.Sprintf("%02d", now.Day()),
			"Hour":          fmt.Sprintf("%02d", now.Hour()),
			"Minute":        fmt.Sprintf("%02d", now.Minute()),
			"Second":        fmt.Sprintf("%02d", now.Second()),
			"Timezone":      loc.String(),
		}}}
	}
	return [][]Item{items}, nil
}

// ---------------------------------------------------------------- IF / Filter

func evalItemCondition(ec *execCtx, i int) (bool, error) {
	if ec.version >= 2 {
		c, err := ec.param(i, "conditions", map[string]interface{}{})
		if err != nil {
			return false, err
		}
		m, _ := c.(map[string]interface{})
		loose, _ := ec.boolean(i, "looseTypeValidation", false)
		if !loose {
			loose, _ = ec.boolean(i, "options.looseTypeValidation", false)
		}
		return condV2(applyIgnoreCase(ec, i, m), loose)
	}
	c, err := ec.param(i, "conditions", map[string]interface{}{})
	if err != nil {
		return false, err
	}
	m, _ := c.(map[string]interface{})
	comb, _ := ec.str(i, "combineOperation", "all")
	return condV1(m, comb)
}

func ifNode(ec *execCtx) ([][]Item, error) {
	out := make([][]Item, 2)
	for i, it := range ec.items() {
		ok, err := evalItemCondition(ec, i)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		it.PairedItem = paired(i)
		if ok {
			out[0] = append(out[0], it)
		} else {
			out[1] = append(out[1], it)
		}
	}
	return out, nil
}

func filterNode(ec *execCtx) ([][]Item, error) {
	outs, err := ifNode(ec)
	if err != nil {
		return nil, err
	}
	if keep, _ := ec.boolean(0, "options.allowFailedDiscarded", false); keep {
		return outs, nil
	}
	return outs[:1], nil
}

// ---------------------------------------------------------------- Switch

func switchNode(ec *execCtx) ([][]Item, error) {
	items := ec.items()
	mode, _ := ec.str(0, "mode", "rules")
	if mode == "expression" {
		n, _ := ec.num(0, "numberOutputs", 4)
		out := make([][]Item, int(n))
		for i, it := range items {
			idx, err := ec.num(i, "output", 0)
			if err != nil {
				return nil, err
			}
			if int(idx) < 0 || int(idx) >= len(out) {
				return nil, fmt.Errorf("output index %d out of range", int(idx))
			}
			it.PairedItem = paired(i)
			out[int(idx)] = append(out[int(idx)], it)
		}
		return out, nil
	}
	if ec.version >= 3 {
		return switchV3(ec, items)
	}
	return switchV1(ec, items)
}

func switchV3(ec *execCtx, items []Item) ([][]Item, error) {
	rawRules, _ := ec.raw("rules.values")
	rules, _ := rawRules.([]interface{})
	fallback, _ := ec.param(0, "options.fallbackOutput", "none")
	allMatching, _ := ec.boolean(0, "options.allMatchingOutputs", false)
	out := make([][]Item, len(rules)+1)
	for i, it := range items {
		it.PairedItem = paired(i)
		matched := false
		for r := range rules {
			c, err := ec.param(i, fmt.Sprintf("rules.values.%d.conditions", r), map[string]interface{}{})
			if err != nil {
				return nil, err
			}
			m, _ := c.(map[string]interface{})
			loose, _ := ec.boolean(i, "looseTypeValidation", false)
			if !loose {
				loose, _ = ec.boolean(i, "options.looseTypeValidation", false)
			}
			ok, err := condV2(applyIgnoreCase(ec, i, m), loose)
			if err != nil {
				return nil, fmt.Errorf("rule %d: %w", r, err)
			}
			if ok {
				out[r] = append(out[r], it)
				matched = true
				if !allMatching {
					break
				}
			}
		}
		if !matched {
			switch fb := fallback.(type) {
			case string:
				if fb == "extra" {
					out[len(rules)] = append(out[len(rules)], it)
				}
			case float64:
				if int(fb) >= 0 && int(fb) < len(rules) {
					out[int(fb)] = append(out[int(fb)], it)
				}
			}
		}
	}
	return out, nil
}

func switchV1(ec *execCtx, items []Item) ([][]Item, error) {
	rawRules, _ := ec.raw("rules.rules")
	rules, _ := rawRules.([]interface{})
	dataType, _ := ec.str(0, "dataType", "number")
	out := make([][]Item, 4)
	grow := func(n int) {
		for len(out) <= n {
			out = append(out, nil)
		}
	}
	for i, it := range items {
		it.PairedItem = paired(i)
		v1, err := ec.param(i, "value1", nil)
		if err != nil {
			return nil, err
		}
		matched := false
		for r := range rules {
			rv, err := ec.param(i, fmt.Sprintf("rules.rules.%d", r), map[string]interface{}{})
			if err != nil {
				return nil, err
			}
			rule, _ := rv.(map[string]interface{})
			op, _ := rule["operation"].(string)
			if op == "" {
				op = "equal"
			}
			ok, err := evalCondV1(dataType, map[string]interface{}{"value1": v1, "value2": rule["value2"], "operation": op})
			if err != nil {
				return nil, err
			}
			if ok {
				idx := r
				if ec.version < 2 {
					idx = int(toFloatDefault(rule["output"], 0))
				}
				grow(idx)
				out[idx] = append(out[idx], it)
				matched = true
				break
			}
		}
		if !matched {
			fb, _ := ec.num(i, "fallbackOutput", -1)
			if fb >= 0 {
				grow(int(fb))
				out[int(fb)] = append(out[int(fb)], it)
			}
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- Set

func setNode(ec *execCtx) ([][]Item, error) {
	items := ec.items()
	out := make([]Item, 0, len(items))
	for i, it := range items {
		j, err := setItem(ec, i, it.JSON)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		out = append(out, Item{JSON: j, Binary: it.Binary, PairedItem: paired(i)})
	}
	return [][]Item{out}, nil
}

func setItem(ec *execCtx, i int, in map[string]interface{}) (map[string]interface{}, error) {
	dot := true
	if v, ok := ec.raw("options.dotNotation"); ok {
		dot = toBool(v)
	}
	if ec.version < 3 {
		keepOnly, _ := ec.boolean(i, "keepOnlySet", false)
		var res map[string]interface{}
		if keepOnly {
			res = map[string]interface{}{}
		} else {
			res = cloneJSON(in)
		}
		vals, err := ec.param(i, "values", map[string]interface{}{})
		if err != nil {
			return nil, err
		}
		vm, _ := vals.(map[string]interface{})
		for _, typ := range []string{"boolean", "number", "string"} {
			list, _ := vm[typ].([]interface{})
			for _, e := range list {
				f, _ := e.(map[string]interface{})
				name := toString(f["name"])
				val := f["value"]
				switch typ {
				case "number":
					val = toFloatDefault(val, 0)
				case "boolean":
					val = toBool(val)
				case "string":
					val = toString(val)
				}
				setPath(res, name, val, dot)
			}
		}
		return res, nil
	}

	mode, _ := ec.str(i, "mode", "manual")
	assigned := map[string]interface{}{}
	type kv struct {
		k string
		v interface{}
	}
	var ordered []kv
	if mode == "raw" {
		v, err := ec.param(i, "jsonOutput", "{}")
		if err != nil {
			return nil, err
		}
		switch t := v.(type) {
		case map[string]interface{}:
			assigned = t
		case string:
			if err := json.Unmarshal([]byte(t), &assigned); err != nil {
				return nil, fmt.Errorf("'JSON Output' is not valid JSON: %v", err)
			}
		default:
			return nil, errors.New("'JSON Output' must be an object")
		}
	} else if ec.version >= 3.3 {
		list, err := ec.param(i, "assignments.assignments", []interface{}{})
		if err != nil {
			return nil, err
		}
		ignoreConv, _ := ec.boolean(i, "options.ignoreConversionErrors", false)
		for _, e := range asList(list) {
			a, _ := e.(map[string]interface{})
			v, err := convertType(a["value"], toString(a["type"]))
			if err != nil && !ignoreConv {
				return nil, fmt.Errorf("field %q: %w", toString(a["name"]), err)
			}
			ordered = append(ordered, kv{toString(a["name"]), v})
		}
	} else {
		list, err := ec.param(i, "fields.values", []interface{}{})
		if err != nil {
			return nil, err
		}
		for _, e := range asList(list) {
			f, _ := e.(map[string]interface{})
			typ := toString(f["type"])
			if typ == "" {
				typ = "stringValue"
			}
			v, err := convertType(f[typ], strings.TrimSuffix(typ, "Value"))
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", toString(f["name"]), err)
			}
			ordered = append(ordered, kv{toString(f["name"]), v})
		}
	}

	var include string
	if ec.version >= 3.3 {
		if ok, _ := ec.boolean(i, "includeOtherFields", false); !ok {
			include = "none"
		} else {
			include, _ = ec.str(i, "include", "all")
		}
	} else {
		include, _ = ec.str(i, "include", "all")
	}
	var res map[string]interface{}
	switch include {
	case "none":
		res = map[string]interface{}{}
	case "selected":
		res = map[string]interface{}{}
		fields, _ := ec.str(i, "includeFields", "")
		for _, f := range splitList(fields) {
			if v, ok := getPath(in, f, dot); ok {
				setPath(res, f, v, dot)
			}
		}
	case "except":
		res = cloneJSON(in)
		fields, _ := ec.str(i, "excludeFields", "")
		for _, f := range splitList(fields) {
			deletePath(res, f, dot)
		}
	default:
		res = cloneJSON(in)
	}
	if mode == "raw" {
		for k, v := range assigned {
			res[k] = v
		}
		return res, nil
	}
	for _, a := range ordered {
		setPath(res, a.k, a.v, dot) // replaces whole values, like n8n
	}
	return res, nil
}

func convertType(v interface{}, typ string) (interface{}, error) {
	switch typ {
	case "number":
		if f, ok := toFloat(v); ok {
			return f, nil
		}
		if v == nil || v == "" {
			return nil, nil
		}
		return v, fmt.Errorf("value '%v' cannot be converted to a number", v)
	case "boolean":
		if b, ok := boolOf(v, true); ok {
			return b, nil
		}
		return v, fmt.Errorf("value '%v' cannot be converted to a boolean", v)
	case "array":
		switch t := v.(type) {
		case []interface{}:
			return t, nil
		case string:
			var arr []interface{}
			if err := json.Unmarshal([]byte(t), &arr); err != nil {
				return v, fmt.Errorf("value cannot be converted to an array")
			}
			return arr, nil
		case nil:
			return []interface{}{}, nil
		}
		return []interface{}{v}, nil
	case "object":
		switch t := v.(type) {
		case map[string]interface{}:
			return t, nil
		case string:
			var obj map[string]interface{}
			if err := json.Unmarshal([]byte(t), &obj); err != nil {
				return v, fmt.Errorf("value cannot be converted to an object")
			}
			return obj, nil
		case nil:
			return map[string]interface{}{}, nil
		}
		return v, fmt.Errorf("value cannot be converted to an object")
	case "string":
		if s, ok := v.(string); ok {
			return s, nil
		}
		if v == nil {
			return "", nil
		}
		return toString(v), nil
	default:
		return v, nil
	}
}

func asList(v interface{}) []interface{} {
	l, _ := v.([]interface{})
	return l
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------- Merge

func mergeNode(ec *execCtx) ([][]Item, error) {
	in := ec.input.Inputs
	get := func(i int) []Item {
		if i < len(in) {
			return in[i]
		}
		return nil
	}
	mode, _ := ec.str(0, "mode", "append")
	pairedFrom := func(items []Item, input int) []Item {
		out := make([]Item, len(items))
		for i, it := range items {
			it.PairedItem = map[string]interface{}{"item": i, "input": input}
			out[i] = it
		}
		return out
	}
	switch mode {
	case "append":
		var out []Item
		for i := range in {
			out = append(out, pairedFrom(get(i), i)...)
		}
		return [][]Item{out}, nil
	case "chooseBranch", "passThrough", "wait":
		which := 1
		if ec.version >= 3 {
			if o, _ := ec.str(0, "output", "specifiedInput"); o == "empty" {
				return [][]Item{{{JSON: map[string]interface{}{}}}}, nil
			}
			n, _ := ec.num(0, "useDataOfInput", 1)
			which = int(n)
		} else {
			o, _ := ec.str(0, "output", "input1")
			if o == "empty" {
				return [][]Item{{{JSON: map[string]interface{}{}}}}, nil
			}
			if o == "input2" {
				which = 2
			}
		}
		return [][]Item{pairedFrom(get(which-1), which-1)}, nil
	case "mergeByIndex", "combineByPosition":
		return [][]Item{mergeByPosition(ec, get(0), get(1))}, nil
	case "multiplex", "combineAll":
		var out []Item
		for i, a := range get(0) {
			for j, b := range get(1) {
				m := cloneJSON(a.JSON)
				for k, v := range b.JSON {
					m[k] = v
				}
				out = append(out, Item{JSON: m, PairedItem: []interface{}{
					map[string]interface{}{"item": i, "input": 0}, map[string]interface{}{"item": j, "input": 1}}})
			}
		}
		return [][]Item{out}, nil
	case "combine":
		by, _ := ec.str(0, "combineBy", "combineByFields")
		if ec.version < 3 {
			cm, _ := ec.str(0, "combinationMode", "mergeByFields")
			by = map[string]string{"mergeByFields": "combineByFields", "mergeByPosition": "combineByPosition", "multiplex": "combineAll"}[cm]
		}
		switch by {
		case "combineByPosition":
			return [][]Item{mergeByPosition(ec, get(0), get(1))}, nil
		case "combineAll":
			ec.params["mode"] = "combineAll"
			return mergeNode(ec)
		case "combineBySql":
			return nil, errors.New("merge: SQL query mode is not supported")
		}
		return mergeByFields(ec, get(0), get(1))
	case "mergeByKey", "keepKeyMatches", "removeKeyMatches":
		k1, _ := ec.str(0, "propertyName1", "")
		k2, _ := ec.str(0, "propertyName2", "")
		pairs := []fieldPair{{k1, k2}}
		join := map[string]string{"mergeByKey": "enrichInput1", "keepKeyMatches": "keepMatchesInput1", "removeKeyMatches": "keepNonMatchesInput1"}[mode]
		return [][]Item{joinItems(get(0), get(1), pairs, join, "both", true)}, nil
	}
	return nil, fmt.Errorf("merge: unsupported mode %q", mode)
}

func mergeByPosition(ec *execCtx, a, b []Item) []Item {
	unpaired, _ := ec.boolean(0, "options.includeUnpaired", false)
	join, _ := ec.str(0, "join", "")
	if join == "outer" || join == "left" {
		unpaired = true
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	if unpaired {
		if len(b) > n {
			n = len(b)
		}
		if len(a) > n {
			n = len(a)
		}
	}
	var out []Item
	for i := 0; i < n; i++ {
		m := map[string]interface{}{}
		var pi []interface{}
		if i < len(a) {
			m = cloneJSON(a[i].JSON)
			pi = append(pi, map[string]interface{}{"item": i, "input": 0})
		}
		if i < len(b) {
			for k, v := range b[i].JSON {
				m[k] = v
			}
			pi = append(pi, map[string]interface{}{"item": i, "input": 1})
		}
		out = append(out, Item{JSON: m, PairedItem: pi})
	}
	return out
}

type fieldPair struct{ a, b string }

func mergeByFields(ec *execCtx, a, b []Item) ([][]Item, error) {
	var pairs []fieldPair
	if s, ok := ec.raw("fieldsToMatchString"); ok {
		v, _ := ec.str(0, "fieldsToMatchString", toString(s))
		for _, f := range splitList(v) {
			pairs = append(pairs, fieldPair{f, f})
		}
	}
	if adv, err := ec.param(0, "mergeByFields.values", nil); err == nil {
		for _, e := range asList(adv) {
			m, _ := e.(map[string]interface{})
			pairs = append(pairs, fieldPair{toString(m["field1"]), toString(m["field2"])})
		}
	}
	if len(pairs) == 0 {
		return nil, errors.New("merge: no fields to match")
	}
	join, _ := ec.str(0, "joinMode", "keepMatches")
	from, _ := ec.str(0, "outputDataFrom", "both")
	dot := true
	if v, _ := ec.boolean(0, "options.disableDotNotation", false); v {
		dot = false
	}
	return [][]Item{joinItems(a, b, pairs, join, from, dot)}, nil
}

func matchKey(j map[string]interface{}, fields []string, dot bool) (string, bool) {
	parts := make([]string, len(fields))
	for i, f := range fields {
		v, ok := getPath(j, f, dot)
		if !ok {
			return "", false
		}
		parts[i] = toString(v)
	}
	return strings.Join(parts, "\x00"), true
}

func joinItems(a, b []Item, pairs []fieldPair, join, from string, dot bool) []Item {
	fa, fb := make([]string, len(pairs)), make([]string, len(pairs))
	for i, p := range pairs {
		fa[i], fb[i] = p.a, p.b
	}
	index := map[string][]int{}
	for j, it := range b {
		if k, ok := matchKey(it.JSON, fb, dot); ok {
			index[k] = append(index[k], j)
		}
	}
	usedB := map[int]bool{}
	merge := func(x, y map[string]interface{}) map[string]interface{} {
		m := cloneJSON(x)
		for k, v := range y {
			m[k] = v
		}
		return m
	}
	pi := func(i, input int) map[string]interface{} { return map[string]interface{}{"item": i, "input": input} }
	var out, unmatchedA []Item
	for i, it := range a {
		k, ok := matchKey(it.JSON, fa, dot)
		matches := index[k]
		if !ok || len(matches) == 0 {
			unmatchedA = append(unmatchedA, Item{JSON: it.JSON, PairedItem: pi(i, 0)})
			if join == "enrichInput1" {
				out = append(out, Item{JSON: it.JSON, PairedItem: pi(i, 0)})
			}
			continue
		}
		for _, j := range matches {
			usedB[j] = true
			switch {
			case join == "keepMatchesInput1" || join == "keepMatches" && from == "input1":
				out = append(out, Item{JSON: it.JSON, PairedItem: pi(i, 0)})
			case join == "keepMatches" && from == "input2":
				out = append(out, Item{JSON: b[j].JSON, PairedItem: pi(j, 1)})
			case join == "enrichInput2":
				// handled below
			case join == "keepNonMatches" || join == "keepNonMatchesInput1":
			default: // keepMatches/both, keepEverything, enrichInput1
				out = append(out, Item{JSON: merge(it.JSON, b[j].JSON), PairedItem: []interface{}{pi(i, 0), pi(j, 1)}})
			}
			if join == "keepMatchesInput1" || join == "keepMatches" && from != "both" {
				break
			}
		}
	}
	switch join {
	case "keepNonMatchesInput1":
		return unmatchedA
	case "keepNonMatches":
		out = nil
		if from != "input2" {
			out = append(out, unmatchedA...)
		}
		if from != "input1" {
			for j, it := range b {
				if !usedB[j] {
					out = append(out, Item{JSON: it.JSON, PairedItem: pi(j, 1)})
				}
			}
		}
	case "keepEverything":
		out = append(out, unmatchedA...)
		for j, it := range b {
			if !usedB[j] {
				out = append(out, Item{JSON: it.JSON, PairedItem: pi(j, 1)})
			}
		}
	case "enrichInput2":
		out = nil
		ia := map[string][]int{}
		for i, it := range a {
			if k, ok := matchKey(it.JSON, fa, dot); ok {
				ia[k] = append(ia[k], i)
			}
		}
		for j, it := range b {
			k, ok := matchKey(it.JSON, fb, dot)
			if ok && len(ia[k]) > 0 {
				i := ia[k][0]
				out = append(out, Item{JSON: merge(a[i].JSON, it.JSON), PairedItem: []interface{}{pi(i, 0), pi(j, 1)}})
			} else {
				out = append(out, Item{JSON: it.JSON, PairedItem: pi(j, 1)})
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- Split Out / Aggregate

func splitOutNode(ec *execCtx) ([][]Item, error) {
	var out []Item
	for i, it := range ec.items() {
		fieldsStr, err := ec.str(i, "fieldToSplitOut", "")
		if err != nil {
			return nil, err
		}
		fields := splitList(fieldsStr)
		if len(fields) == 0 {
			return nil, errors.New("split out: 'Fields To Split Out' is empty")
		}
		dot := true
		if v, _ := ec.boolean(i, "options.disableDotNotation", false); v {
			dot = false
		}
		dest, _ := ec.str(i, "options.destinationFieldName", "")
		include, _ := ec.str(i, "include", "noOtherFields")
		var lists [][]interface{}
		maxLen := 0
		for _, f := range fields {
			v, ok := getPath(it.JSON, f, dot)
			if !ok {
				lists = append(lists, nil)
				continue
			}
			var l []interface{}
			switch t := v.(type) {
			case []interface{}:
				l = t
			case map[string]interface{}:
				l = []interface{}{t}
			default:
				l = []interface{}{t}
			}
			lists = append(lists, l)
			if len(l) > maxLen {
				maxLen = len(l)
			}
		}
		for k := 0; k < maxLen; k++ {
			var base map[string]interface{}
			switch include {
			case "allOtherFields":
				base = cloneJSON(it.JSON)
				for _, f := range fields {
					deletePath(base, f, dot)
				}
			case "selectedOtherFields":
				base = map[string]interface{}{}
				sel, _ := ec.str(i, "fieldsToInclude", "")
				for _, f := range splitList(sel) {
					if v, ok := getPath(it.JSON, f, dot); ok {
						setPath(base, f, v, dot)
					}
				}
			default:
				base = map[string]interface{}{}
			}
			for fi, f := range fields {
				if k >= len(lists[fi]) {
					continue
				}
				el := lists[fi][k]
				name := f
				if dest != "" {
					name = dest
				}
				if obj, ok := el.(map[string]interface{}); ok && len(fields) == 1 && dest == "" {
					for kk, vv := range obj {
						base[kk] = vv
					}
					continue
				}
				setPath(base, name, el, dot && dest == "")
			}
			out = append(out, Item{JSON: base, Binary: it.Binary, PairedItem: paired(i)})
		}
	}
	return [][]Item{out}, nil
}

func aggregateNode(ec *execCtx) ([][]Item, error) {
	items := ec.items()
	mode, _ := ec.str(0, "aggregate", "aggregateIndividualFields")
	pis := make([]interface{}, len(items))
	for i := range items {
		pis[i] = paired(i)
	}
	dot := true
	if v, _ := ec.boolean(0, "options.disableDotNotation", false); v {
		dot = false
	}
	if mode == "aggregateAllItemData" {
		dest, _ := ec.str(0, "destinationFieldName", "data")
		include, _ := ec.str(0, "include", "allFields")
		list := make([]interface{}, 0, len(items))
		for i, it := range items {
			var j map[string]interface{}
			switch include {
			case "specifiedFields":
				j = map[string]interface{}{}
				f, _ := ec.str(i, "fieldsToInclude", "")
				for _, k := range splitList(f) {
					if v, ok := getPath(it.JSON, k, dot); ok {
						setPath(j, k, v, dot)
					}
				}
			case "allFieldsExcept":
				j = cloneJSON(it.JSON)
				f, _ := ec.str(i, "fieldsToExclude", "")
				for _, k := range splitList(f) {
					deletePath(j, k, dot)
				}
			default:
				j = it.JSON
			}
			list = append(list, j)
		}
		return [][]Item{{{JSON: map[string]interface{}{dest: list}, PairedItem: pis}}}, nil
	}
	fieldsRaw, err := ec.param(0, "fieldsToAggregate.fieldToAggregate", []interface{}{})
	if err != nil {
		return nil, err
	}
	keepMissing, _ := ec.boolean(0, "options.keepMissing", false)
	mergeLists, _ := ec.boolean(0, "options.mergeLists", false)
	res := map[string]interface{}{}
	for _, fr := range asList(fieldsRaw) {
		f, _ := fr.(map[string]interface{})
		field := toString(f["fieldToAggregate"])
		outName := field
		if toBool(f["renameField"]) && toString(f["outputFieldName"]) != "" {
			outName = toString(f["outputFieldName"])
		}
		list := []interface{}{}
		for _, it := range items {
			v, ok := getPath(it.JSON, field, dot)
			if !ok {
				if keepMissing {
					list = append(list, nil)
				}
				continue
			}
			if arr, isArr := v.([]interface{}); isArr && mergeLists {
				list = append(list, arr...)
			} else {
				list = append(list, v)
			}
		}
		res[outName] = list
	}
	return [][]Item{{{JSON: res, PairedItem: pis}}}, nil
}

// ---------------------------------------------------------------- Limit / Sort / Dedupe

func limitNode(ec *execCtx) ([][]Item, error) {
	items := ec.items()
	max, _ := ec.num(0, "maxItems", 1)
	keep, _ := ec.str(0, "keep", "firstItems")
	n := int(max)
	if n < 0 || max != max {
		n = 0
	}
	if n >= len(items) {
		return passthrough(ec)
	}
	start := 0
	if keep == "lastItems" {
		start = len(items) - n
	}
	out := make([]Item, 0, n)
	for i := start; i < start+n; i++ {
		it := items[i]
		it.PairedItem = paired(i)
		out = append(out, it)
	}
	return [][]Item{out}, nil
}

func sortNode(ec *execCtx) ([][]Item, error) {
	items := ec.items()
	out := make([]Item, len(items))
	for i, it := range items {
		it.PairedItem = paired(i)
		out[i] = it
	}
	typ, _ := ec.str(0, "type", "simple")
	switch typ {
	case "random":
		rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
		return [][]Item{out}, nil
	case "code":
		return nil, errors.New("sort: code mode is not supported")
	}
	fieldsRaw, err := ec.param(0, "sortFieldsUi.sortField", []interface{}{})
	if err != nil {
		return nil, err
	}
	dot := true
	if v, _ := ec.boolean(0, "options.disableDotNotation", false); v {
		dot = false
	}
	fields := asList(fieldsRaw)
	sort.SliceStable(out, func(x, y int) bool {
		for _, fr := range fields {
			f, _ := fr.(map[string]interface{})
			name := toString(f["fieldName"])
			desc := toString(f["order"]) == "descending"
			a, _ := getPath(out[x].JSON, name, dot)
			b, _ := getPath(out[y].JSON, name, dot)
			c := compareValues(a, b)
			if c == 0 {
				continue
			}
			if desc {
				return c > 0
			}
			return c < 0
		}
		return false
	})
	return [][]Item{out}, nil
}

func compareValues(a, b interface{}) int {
	fa, oka := a.(float64)
	fb, okb := b.(float64)
	if oka && okb {
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	}
	if a == nil && b != nil {
		return -1
	}
	if b == nil && a != nil {
		return 1
	}
	return strings.Compare(toString(a), toString(b))
}

func removeDuplicatesNode(ec *execCtx) ([][]Item, error) {
	switch op, _ := ec.str(0, "operation", "removeDuplicateInputItems"); op {
	case "removeDuplicateInputItems":
	case "removeItemsSeenInPreviousExecutions", "removeItemsRepeatedWithinPreviousExecutions":
		return dedupeAcrossExecutions(ec)
	case "clearDeduplicationHistory":
		key := ec.dedupeKey()
		unlock := lockPollKey(key)
		err := ec.pollStore().SetPollState(ec.ctx, key, "{}")
		unlock()
		if err != nil {
			return nil, err
		}
		return passthrough(ec)
	default:
		return nil, fmt.Errorf("remove duplicates: operation %q is not supported", op)
	}
	items := ec.items()
	compare, _ := ec.str(0, "compare", "allFields")
	dot := true
	if v, _ := ec.boolean(0, "options.disableDotNotation", false); v {
		dot = false
	}
	seen := map[string]bool{}
	var out []Item
	for i, it := range items {
		var key string
		switch compare {
		case "selectedFields":
			f, _ := ec.str(i, "fieldsToCompare", "")
			vals := []interface{}{}
			for _, k := range splitList(f) {
				v, _ := getPath(it.JSON, k, dot)
				vals = append(vals, v)
			}
			key = mustJSON(vals)
		case "allFieldsExcept":
			j := cloneJSON(it.JSON)
			f, _ := ec.str(i, "fieldsToExclude", "")
			for _, k := range splitList(f) {
				deletePath(j, k, dot)
			}
			key = mustJSON(j)
		default:
			key = mustJSON(it.JSON)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		it.PairedItem = paired(i)
		out = append(out, it)
	}
	return [][]Item{out}, nil
}

// ---------------------------------------------------------------- Wait / Respond / Stop

// durableWaitThreshold: shorter waits sleep in the worker, longer ones are
// stored and resumed by the engine's reaper (checked every 30s).
var durableWaitThreshold = 65 * time.Second

func waitNode(ec *execCtx) ([][]Item, error) {
	resume, _ := ec.str(0, "resume", "timeInterval")
	var d time.Duration
	switch resume {
	case "timeInterval":
		amount, _ := ec.num(0, "amount", 1)
		unit, _ := ec.str(0, "unit", "hours")
		mult := map[string]time.Duration{"seconds": time.Second, "minutes": time.Minute, "hours": time.Hour, "days": 24 * time.Hour}[unit]
		if mult == 0 {
			mult = time.Hour
		}
		d = time.Duration(amount * float64(mult))
	case "specificTime":
		s, _ := ec.str(0, "dateTime", "")
		t, ok := timeOf(s)
		if !ok {
			return nil, fmt.Errorf("wait: invalid date %q", s)
		}
		d = time.Until(t)
	case "form":
		if _, err := ec.resumeURL(); err != nil {
			return nil, err
		}
		return nil, errWaitSignal
	default:
		return nil, errWaitSignal
	}
	if d > durableWaitThreshold {
		// Long waits are durable and do not hold a worker (like n8n).
		outs, _ := passthrough(ec)
		out, err := encodeOutput(outs, nil)
		if err != nil {
			return nil, err
		}
		return nil, &engine.WaitUntilError{At: time.Now().Add(d), Output: out}
	}
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ec.ctx.Done():
			return nil, ec.ctx.Err()
		}
	}
	return passthrough(ec)
}

func respondToWebhookNode(ec *execCtx) ([][]Item, error) {
	items := ec.items()
	with, _ := ec.str(0, "respondWith", "firstIncomingItem")
	code, _ := ec.num(0, "options.responseCode", 200)
	resp := map[string]interface{}{"statusCode": code}
	headers := map[string]interface{}{}
	if hs, err := ec.param(0, "options.responseHeaders.entries", nil); err == nil {
		for _, e := range asList(hs) {
			m, _ := e.(map[string]interface{})
			headers[toString(m["name"])] = toString(m["value"])
		}
	}
	switch with {
	case "allIncomingItems":
		list := make([]interface{}, len(items))
		for i, it := range items {
			list[i] = it.JSON
		}
		resp["body"] = list
	case "json":
		v, err := ec.param(0, "responseBody", "{}")
		if err != nil {
			return nil, err
		}
		if s, ok := v.(string); ok {
			var parsed interface{}
			if json.Unmarshal([]byte(s), &parsed) == nil {
				v = parsed
			}
		}
		resp["body"] = v
	case "text":
		v, err := ec.str(0, "responseBody", "")
		if err != nil {
			return nil, err
		}
		resp["body"] = v
		resp["text"] = true
		if _, ok := headers["Content-Type"]; !ok {
			headers["Content-Type"] = "text/plain; charset=utf-8"
		}
	case "noData":
		resp["body"] = nil
	case "redirect":
		u, _ := ec.str(0, "redirectURL", "")
		headers["Location"] = u
		if code == 200 {
			resp["statusCode"] = 307
		}
	case "binary":
		field := "data"
		if src, _ := ec.str(0, "responseDataSource", "automatically"); src == "set" {
			field, _ = ec.str(0, "inputFieldName", "data")
		} else if len(items) > 0 {
			for k := range items[0].Binary {
				field = k
				break
			}
		}
		if len(items) == 0 {
			return nil, permanentError{"respond to webhook: no item with binary data"}
		}
		data, mt, name, err := binaryOf(items[0], field)
		if err != nil {
			return nil, err
		}
		resp["binary"] = base64.StdEncoding.EncodeToString(data)
		if _, ok := headers["Content-Type"]; !ok {
			headers["Content-Type"] = mt
		}
		if _, ok := headers["Content-Disposition"]; !ok {
			headers["Content-Disposition"] = fmt.Sprintf("inline; filename=%q", name)
		}
	case "jwt":
		return nil, fmt.Errorf("respond to webhook: respondWith %q is not supported", with)
	default: // firstIncomingItem
		if len(items) > 0 {
			resp["body"] = items[0].JSON
		} else {
			resp["body"] = map[string]interface{}{}
		}
	}
	resp["headers"] = headers
	ec.extra = map[string]interface{}{"__response": resp}
	return passthrough(ec)
}

func stopAndErrorNode(ec *execCtx) ([][]Item, error) {
	typ, _ := ec.str(0, "errorType", "errorMessage")
	if typ == "errorObject" {
		v, _ := ec.param(0, "errorObject", "{}")
		return nil, permanentError{fmt.Sprintf("%v", toString(v))}
	}
	msg, _ := ec.str(0, "errorMessage", "Workflow stopped")
	return nil, permanentError{msg}
}

type permanentError struct{ msg string }

func (e permanentError) Error() string   { return e.msg }
func (e permanentError) Permanent() bool { return true }

// dedupeAcrossExecutions drops items whose dedupe value was seen in previous
// executions (history kept in the poll state store, last 10000 values).
// dedupeState is the persisted history of "Remove Items Seen in Previous
// Executions": seen keys (newest last) or the highest incremental/date value.
type dedupeState struct {
	Keys []string `json:"keys,omitempty"`
	Max  *float64 `json:"max,omitempty"`
	Date string   `json:"date,omitempty"`
}

// dedupeKey is the history key: per node (default) or shared by the workflow.
func (ec *execCtx) dedupeKey() string {
	if scope, _ := ec.str(0, "options.scope", "node"); scope == "workflow" {
		return engine.NamespaceFromContext(ec.ctx) + "/" + ec.data.wf.ID + "#dedupe"
	}
	return ec.pollKey() + "#dedupe"
}

func dedupeAcrossExecutions(ec *execCtx) ([][]Item, error) {
	key := ec.dedupeKey()
	unlock := lockPollKey(key)
	defer unlock()
	store := ec.pollStore()
	raw, _, err := store.GetPollState(ec.ctx, key)
	if err != nil {
		return nil, err
	}
	var st dedupeState
	if raw != "" && json.Unmarshal([]byte(raw), &st) != nil {
		// History written before the state became an object: a plain key list.
		_ = json.Unmarshal([]byte(raw), &st.Keys)
	}
	logic, _ := ec.str(0, "logic", "removeItemsWithAlreadySeenKeys")
	var out []Item
	switch logic {
	case "removeItemsUpToStoredIncrementalKey":
		max := st.Max
		for i, it := range ec.items() {
			v, err := ec.param(i, "incrementalDedupeValue", nil)
			if err != nil {
				return nil, err
			}
			f, ok := toFloat(v)
			if !ok {
				return nil, fmt.Errorf("remove duplicates: incremental value %v of item %d is not a number", v, i)
			}
			if st.Max != nil && f <= *st.Max {
				continue
			}
			if max == nil || f > *max {
				f := f
				max = &f
			}
			it.PairedItem = paired(i)
			out = append(out, it)
		}
		st.Max = max
	case "removeItemsUpToStoredDate":
		var prev, max time.Time
		if st.Date != "" {
			prev, _ = time.Parse(time.RFC3339Nano, st.Date)
			max = prev
		}
		for i, it := range ec.items() {
			v, err := ec.param(i, "dateDedupeValue", nil)
			if err != nil {
				return nil, err
			}
			t, ok := timeOf(v)
			if !ok {
				return nil, fmt.Errorf("remove duplicates: date value %v of item %d is not a valid date", v, i)
			}
			if !prev.IsZero() && !t.After(prev) {
				continue
			}
			if t.After(max) {
				max = t
			}
			it.PairedItem = paired(i)
			out = append(out, it)
		}
		if !max.IsZero() {
			st.Date = max.UTC().Format(time.RFC3339Nano)
		}
	default:
		set := make(map[string]bool, len(st.Keys))
		for _, s := range st.Keys {
			set[s] = true
		}
		for i, it := range ec.items() {
			v, err := ec.param(i, "dedupeValue", "")
			if err != nil {
				return nil, err
			}
			k := toString(v)
			if set[k] {
				continue
			}
			set[k] = true
			st.Keys = append(st.Keys, k)
			it.PairedItem = paired(i)
			out = append(out, it)
		}
		size := 10000
		if v, ok := ec.raw("options.historySize"); ok {
			if f, ok := toFloat(v); ok && f >= 1 {
				size = int(f)
			}
		}
		if len(st.Keys) > size {
			st.Keys = st.Keys[len(st.Keys)-size:]
		}
	}
	b, _ := json.Marshal(st)
	if err := store.SetPollState(ec.ctx, key, string(b)); err != nil {
		return nil, err
	}
	return [][]Item{out}, nil
}

// applyIgnoreCase honors the node-level "Ignore Case" option, which n8n
// applies on top of the stored conditions.caseSensitive flag.
func applyIgnoreCase(ec *execCtx, i int, c map[string]interface{}) map[string]interface{} {
	ig, ok := ec.raw("options.ignoreCase")
	if !ok || c == nil {
		return c
	}
	v, _ := ec.boolean(i, "options.ignoreCase", toBool(ig))
	out := map[string]interface{}{}
	for k, x := range c {
		out[k] = x
	}
	opts := map[string]interface{}{}
	for k, x := range asMap(c["options"]) {
		opts[k] = x
	}
	opts["caseSensitive"] = !v
	out["options"] = opts
	return out
}

// evaluationNode is n8n's Evaluation node outside evaluation runs (the only
// mode supported): "Check if evaluating" routes items to its "Normal"
// output; setting outputs or metrics passes the items on unchanged.
func evaluationNode(ec *execCtx) ([][]Item, error) {
	op, _ := ec.str(0, "operation", "setOutputs")
	out, _ := passthrough(ec)
	if op == "checkIfEvaluating" {
		return [][]Item{nil, out[0]}, nil
	}
	return out, nil
}
