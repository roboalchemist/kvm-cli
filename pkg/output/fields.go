package output

import (
	"bytes"
	"encoding/json"
	"strings"
)

// orderedObject is a JSON object that marshals its keys in insertion order.
// encoding/json sorts map keys alphabetically, so this type is required to
// honour the field order requested via --fields.
type orderedObject struct {
	keys   []string
	values map[string]any
}

func newOrderedObject() *orderedObject {
	return &orderedObject{values: map[string]any{}}
}

func (o *orderedObject) set(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// MarshalJSON emits the object with keys in insertion order.
func (o *orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(o.values[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// fieldTree is a trie of the requested --fields paths. Each node corresponds to
// a JSON object key; a node marked whole requests the entire value at that
// path, while child nodes request nested members.
type fieldTree struct {
	whole    bool
	children map[string]*fieldTree
	order    []string
}

// newFieldTree builds a lookup tree from sanitized field strings. A dotted path
// such as "system.kvmd.version" descends one node per segment; a plain name
// such as "id" is a single-segment path.
func newFieldTree(fields []string) *fieldTree {
	root := &fieldTree{children: map[string]*fieldTree{}}
	for _, f := range fields {
		node := root
		for _, seg := range strings.Split(f, ".") {
			child, ok := node.children[seg]
			if !ok {
				child = &fieldTree{children: map[string]*fieldTree{}}
				node.children[seg] = child
				node.order = append(node.order, seg)
			}
			node = child
		}
		node.whole = true
	}
	return root
}

// ProjectFields returns a copy of v containing only the requested JSON fields.
//
// Field names are matched against the JSON representation of v, so struct tags
// (not Go field names) are used. Dotted paths select nested values (for example
// "system.kvmd.version"), while plain names match top-level keys. It handles
// both a single JSON object and an array of objects, and preserves the order of
// the requested fields. Fields that are not present are omitted. Blank field
// names are ignored.
//
// When fields is empty, v is returned unchanged.
func ProjectFields(v any, fields []string) any {
	if len(fields) == 0 {
		return v
	}
	return projectValue(toGeneric(v), newFieldTree(sanitizeFields(fields)))
}

func sanitizeFields(fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// toGeneric converts an arbitrary value into the generic shape produced by
// encoding/json (map[string]any, []any, string, float64, bool, nil) by way of a
// JSON round-trip. This makes projection independent of the concrete Go type.
func toGeneric(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}

func projectValue(v any, tree *fieldTree) any {
	switch t := v.(type) {
	case map[string]any:
		return projectObject(t, tree)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = projectValue(item, tree)
		}
		return out
	default:
		// Scalars (and null) have no fields to project.
		return v
	}
}

// projectObject builds the ordered projection of m according to tree. A whole
// node includes its entire value; otherwise only the requested descendants are
// kept, and a key whose descendants all miss is omitted.
func projectObject(m map[string]any, tree *fieldTree) any {
	if tree.whole {
		// The whole object was requested; pass it through unchanged.
		return m
	}

	obj := newOrderedObject()
	for _, key := range tree.order {
		child := tree.children[key]
		val, ok := m[key]
		if !ok {
			continue
		}
		if child.whole {
			obj.set(key, val)
			continue
		}
		// Only containers can satisfy a nested path.
		switch val.(type) {
		case map[string]any, []any:
		default:
			continue
		}
		projected := projectValue(val, child)
		if isEmptyProjection(projected) {
			continue
		}
		obj.set(key, projected)
	}
	return obj
}

// isEmptyProjection reports whether a projected value carries no data, in which
// case its parent key is omitted. Empty JSON objects count as empty; arrays and
// scalars always count as present.
func isEmptyProjection(v any) bool {
	switch t := v.(type) {
	case *orderedObject:
		return len(t.keys) == 0
	case map[string]any:
		return len(t) == 0
	case nil:
		return true
	default:
		return false
	}
}
