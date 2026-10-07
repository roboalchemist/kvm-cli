package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/itchyny/gojq"
)

// normalizeJSON converts a value into the generic shape understood by gojq
// (map[string]any, []any, string, float64, bool, nil) via a JSON round-trip.
// This also strips custom marshalers such as orderedObject so that jq operates
// on plain data.
func normalizeJSON(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("json marshal: %w", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return out, nil
}

// runJQ applies the jq expression expr to v and writes each result as
// pretty-printed JSON, one result per line. An expression that produces no
// results (e.g. `empty`) writes nothing and is not an error.
func runJQ(w io.Writer, v any, expr string) error {
	query, err := gojq.Parse(expr)
	if err != nil {
		// A malformed expression is a usage error (exit 2), not a runtime
		// failure (exit 1): the user's invocation is incorrect.
		return NewCodedError("USAGE", fmt.Sprintf("invalid jq expression %q: %v", expr, err))
	}

	iter := query.Run(v)
	for {
		item, ok := iter.Next()
		if !ok {
			break
		}
		if jqErr, isErr := item.(error); isErr {
			return fmt.Errorf("jq %q: %w", expr, jqErr)
		}
		out, err := json.MarshalIndent(item, "", "  ")
		if err != nil {
			return fmt.Errorf("json marshal: %w", err)
		}
		if _, err := fmt.Fprintln(w, string(out)); err != nil {
			return fmt.Errorf("write jq result: %w", err)
		}
	}
	return nil
}
