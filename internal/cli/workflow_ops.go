package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

type manifestDoc = map[string]any

var (
	editFieldName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	editStepID    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
)

// maxPatchOps is the gateway's cap on the property changes of one updateStep.
const maxPatchOps = 100

// editOwnFields have an operation of their own; every other top-level field
// travels as setMeta.
var editOwnFields = map[string]bool{"steps": true, "params": true, "input": true, "output": true, "invocations": true, "verify": true, "limits": true}

// canonicalJSON is the text two equal documents share: encoding/json sorts the
// keys of every object.
func canonicalJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func cloneDoc[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

// opsFromManifestDiff returns the edit operations the draft API takes to turn
// prev into next. A step whose place in the tree is unchanged is updated in
// place; when steps were added, removed or moved the step tree is replaced,
// which saves the same manifest as one revision.
func opsFromManifestDiff(prev, next manifestDoc) ([]map[string]any, error) {
	var ops []map[string]any
	emit := func(op map[string]any) { ops = append(ops, op) }

	fields := map[string]bool{}
	for _, doc := range []manifestDoc{prev, next} {
		for k := range doc {
			if !editOwnFields[k] {
				fields[k] = true
			}
		}
	}
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, field := range names {
		if canonicalJSON(prev[field]) == canonicalJSON(next[field]) {
			continue
		}
		if !editFieldName.MatchString(field) {
			return nil, fmt.Errorf("the manifest field %q cannot be saved: use letters, digits, - and _", field)
		}
		emit(map[string]any{"op": "setMeta", "field": field, "value": next[field]})
	}
	for _, f := range []struct{ field, op, key string }{
		{"input", "setInput", "schema"}, {"output", "setOutput", "schema"}, {"verify", "setVerify", "verify"}, {"limits", "setLimits", "limits"},
	} {
		if canonicalJSON(prev[f.field]) != canonicalJSON(next[f.field]) {
			emit(map[string]any{"op": f.op, f.key: next[f.field]})
		}
	}
	for _, op := range paramOps(prev, next) {
		emit(op)
	}
	for _, op := range invocationOps(prev, next) {
		emit(op)
	}
	steps, err := stepOps(prev, next)
	if err != nil {
		return nil, err
	}
	return append(ops, steps...), nil
}

func paramOps(prev, next manifestDoc) []map[string]any {
	var ops []map[string]any
	wanted, wantedIsList := next["params"].([]any)
	if !wantedIsList {
		if _, has := prev["params"]; has {
			ops = append(ops, map[string]any{"op": "setMeta", "field": "params", "value": nil})
		}
		return ops
	}
	cur := cloneDoc(prev["params"])
	list, _ := cur.([]any)
	if _, isList := prev["params"].([]any); !isList {
		ops = append(ops, map[string]any{"op": "setMeta", "field": "params", "value": []any{}})
		list = nil
	}
	keyOf := func(p any) string {
		m, _ := p.(map[string]any)
		s, _ := m["key"].(string)
		return s
	}
	keep := map[string]bool{}
	for _, p := range wanted {
		keep[keyOf(p)] = true
	}
	for _, p := range slices.Clone(list) {
		if keep[keyOf(p)] {
			continue
		}
		ops = append(ops, map[string]any{"op": "removeParam", "key": keyOf(p)})
		list = slices.DeleteFunc(list, func(q any) bool { return keyOf(q) == keyOf(p) })
	}
	for index, param := range wanted {
		at := slices.IndexFunc(list, func(q any) bool { return keyOf(q) == keyOf(param) })
		if at == index && canonicalJSON(list[at]) == canonicalJSON(param) {
			continue
		}
		ops = append(ops, map[string]any{"op": "setParam", "param": param, "index": index})
		if at >= 0 {
			list = slices.Delete(list, at, at+1)
		}
		list = slices.Insert(list, min(index, len(list)), param)
	}
	return ops
}

var invocationKinds = []string{"mcp", "api", "manual", "triggers"}

func invocationOps(prev, next manifestDoc) []map[string]any {
	if canonicalJSON(prev["invocations"]) == canonicalJSON(next["invocations"]) {
		return nil
	}
	wanted, ok := next["invocations"].(map[string]any)
	if !ok {
		return []map[string]any{{"op": "removeInvocation", "kind": "all"}}
	}
	for k := range wanted {
		if !slices.Contains(invocationKinds, k) {
			return []map[string]any{{"op": "setInvocation", "kind": "all", "value": wanted}}
		}
	}
	have, _ := prev["invocations"].(map[string]any)
	var ops []map[string]any
	for _, kind := range invocationKinds {
		value, set := wanted[kind]
		_, had := have[kind]
		switch {
		case !set && had:
			ops = append(ops, map[string]any{"op": "removeInvocation", "kind": kind})
		case set && canonicalJSON(have[kind]) != canonicalJSON(value):
			ops = append(ops, map[string]any{"op": "setInvocation", "kind": kind, "value": value})
		}
	}
	return ops
}

// stepSlot is where one step sits in the tree.
type stepSlot struct {
	step   map[string]any
	parent string
	branch string
	index  int
}

// stepSlots lists every step with its place, containers before their children.
func stepSlots(steps []any, parent, branch string, out *[]stepSlot, seen map[string]bool) error {
	for i, raw := range steps {
		step, ok := raw.(map[string]any)
		id, _ := step["id"].(string)
		if !ok || !editStepID.MatchString(id) {
			return errors.New("every step needs an id of lowercase letters, digits and _, starting with a letter")
		}
		if seen[id] {
			return fmt.Errorf("the step id %q is used twice", id)
		}
		seen[id] = true
		*out = append(*out, stepSlot{step: step, parent: parent, branch: branch, index: i})
		if step["kind"] == "foreach" {
			if err := stepSlots(childSteps(step, ""), id, "", out, seen); err != nil {
				return err
			}
		}
		if step["kind"] == "parallel" {
			branches, _ := step["branches"].(map[string]any)
			for _, name := range sortedKeys(branches) {
				if err := stepSlots(childSteps(step, name), id, name, out, seen); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// childSteps is the list a container holds: a foreach's own steps, or one
// branch of a parallel step.
func childSteps(step map[string]any, branch string) []any {
	switch step["kind"] {
	case "foreach":
		list, _ := step["steps"].([]any)
		return list
	case "parallel":
		b, _ := step["branches"].(map[string]any)[branch].(map[string]any)
		list, _ := b["steps"].([]any)
		return list
	}
	return nil
}

// stepShell is the step without the steps it contains.
func stepShell(step map[string]any) map[string]any {
	shell := cloneDoc(step)
	switch shell["kind"] {
	case "foreach":
		shell["steps"] = []any{}
	case "parallel":
		if branches, ok := shell["branches"].(map[string]any); ok {
			for _, b := range branches {
				if bm, ok := b.(map[string]any); ok {
					bm["steps"] = []any{}
				}
			}
		}
	}
	return shell
}

func layoutOf(slots []stepSlot) string {
	var b strings.Builder
	for _, s := range slots {
		fmt.Fprintf(&b, "%s|%s|%s|%d|%s", s.step["id"], s.parent, s.branch, s.index, s.step["kind"])
		if branches, ok := s.step["branches"].(map[string]any); ok && s.step["kind"] == "parallel" {
			b.WriteString("|" + strings.Join(sortedKeys(branches), ","))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func stepOps(prev, next manifestDoc) ([]map[string]any, error) {
	prevSteps, _ := prev["steps"].([]any)
	nextSteps, _ := next["steps"].([]any)
	var before, after []stepSlot
	if err := stepSlots(prevSteps, "", "", &before, map[string]bool{}); err != nil {
		return nil, fmt.Errorf("the saved draft: %w", err)
	}
	if err := stepSlots(nextSteps, "", "", &after, map[string]bool{}); err != nil {
		return nil, err
	}
	if canonicalJSON(prevSteps) == canonicalJSON(nextSteps) {
		return nil, nil
	}
	var ops []map[string]any
	if layoutOf(before) == layoutOf(after) {
		for i := range after {
			patch := stepPatch(stepShell(before[i].step), stepShell(after[i].step), "", nil)
			for len(patch) > 0 {
				n := min(len(patch), maxPatchOps)
				ops = append(ops, map[string]any{"op": "updateStep", "id": after[i].step["id"], "patch": patch[:n]})
				patch = patch[n:]
			}
		}
		return ops, nil
	}
	for _, s := range before {
		if s.parent == "" {
			ops = append(ops, map[string]any{"op": "removeStep", "id": s.step["id"]})
		}
	}
	for _, s := range after {
		at := map[string]any{"index": s.index}
		if s.parent != "" {
			at["parentId"] = s.parent
		}
		if s.branch != "" {
			at["branch"] = s.branch
		}
		ops = append(ops, map[string]any{"op": "addStep", "step": stepShell(s.step), "at": at})
	}
	return ops, nil
}

func escapePointer(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// stepPatch lists the property changes that turn one step's own settings
// into another's.
func stepPatch(from, to map[string]any, base string, out []map[string]any) []map[string]any {
	for _, k := range sortedKeys(from) {
		if _, ok := to[k]; !ok {
			out = append(out, map[string]any{"op": "remove", "path": base + "/" + escapePointer(k)})
		}
	}
	for _, k := range sortedKeys(to) {
		path := base + "/" + escapePointer(k)
		old, had := from[k]
		switch {
		case !had:
			out = append(out, map[string]any{"op": "add", "path": path, "value": to[k]})
		case canonicalJSON(old) != canonicalJSON(to[k]):
			a, aok := old.(map[string]any)
			b, bok := to[k].(map[string]any)
			if aok && bok {
				out = stepPatch(a, b, path, out)
			} else {
				out = append(out, map[string]any{"op": "replace", "path": path, "value": to[k]})
			}
		}
	}
	return out
}
