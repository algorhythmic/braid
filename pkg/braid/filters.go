package braid

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Predicate uses JSON scalar types, with no string/number/bool coercion.
// Paths address nested object fields in attrs using dot-separated identifiers.
type Predicate struct {
	Op       string      `json:"op"`
	Path     string      `json:"path,omitempty"`
	Value    any         `json:"value"`
	Values   []any       `json:"values,omitempty"`
	Children []Predicate `json:"children,omitempty"`
}

var attrPath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

func scalarSQL(expr, typ string, v any) (string, []any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", nil, err
	}
	var scalar any
	if err = json.Unmarshal(b, &scalar); err != nil {
		return "", nil, err
	}
	switch scalar.(type) {
	case nil:
		return typ + `='null'`, nil, nil
	case bool:
		return typ + `=json_type(?,'$')`, []any{string(b)}, nil
	case string:
		return `(` + typ + `='text' AND ` + expr + `=json_extract(?,'$'))`, []any{string(b)}, nil
	case float64:
		return `(` + typ + ` IN ('integer','real') AND ` + expr + `=json_extract(?,'$'))`, []any{string(b)}, nil
	default:
		return "", nil, fmt.Errorf("predicate values must be JSON scalars")
	}
}

func predicateSQL(p Predicate, depth int, count *int) (string, []any, error) {
	(*count)++
	if depth > 32 || *count > 256 {
		return "", nil, fmt.Errorf("predicate exceeds depth 32 or 256 clauses")
	}
	if p.Op == "and" || p.Op == "or" {
		if len(p.Children) == 0 || p.Path != "" || p.Value != nil || p.Values != nil {
			return "", nil, fmt.Errorf("and/or require only nonempty children")
		}
		parts := []string{}
		args := []any{}
		for _, c := range p.Children {
			s, a, err := predicateSQL(c, depth+1, count)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, s)
			args = append(args, a...)
		}
		return "(" + strings.Join(parts, " "+strings.ToUpper(p.Op)+" ") + ")", args, nil
	}
	if !attrPath.MatchString(p.Path) || len(p.Children) > 0 {
		return "", nil, fmt.Errorf("predicate requires a dot-separated attrs path and no children")
	}
	// Validated identifiers alone enter SQL; all consumer values are parameters.
	path := "$." + p.Path
	expr := `json_extract(n.attrs,'` + path + `')`
	typ := `json_type(n.attrs,'` + path + `')`
	if p.Op != "in" && p.Values != nil {
		return "", nil, fmt.Errorf("values is only valid for in")
	}
	switch p.Op {
	case "exists":
		v, ok := p.Value.(bool)
		if !ok {
			return "", nil, fmt.Errorf("exists requires a boolean value")
		}
		if v {
			return typ + ` IS NOT NULL`, nil, nil
		}
		return typ + ` IS NULL`, nil, nil
	case "eq":
		return scalarSQL(expr, typ, p.Value)
	case "in":
		if p.Value != nil {
			return "", nil, fmt.Errorf("in requires values, not value")
		}
		if len(p.Values) > 256 {
			return "", nil, fmt.Errorf("in accepts at most 256 scalar values; use allowed_ids for large ID sets")
		}
		parts := []string{}
		args := []any{}
		for _, v := range p.Values {
			s, a, err := scalarSQL(expr, typ, v)
			if err != nil {
				return "", nil, err
			}
			parts = append(parts, s)
			args = append(args, a...)
		}
		if len(parts) == 0 {
			return "0", nil, nil
		}
		return "(" + strings.Join(parts, " OR ") + ")", args, nil
	case "contains":
		s, a, err := scalarSQL("j.value", "j.type", p.Value)
		return `(` + typ + `='array' AND EXISTS(SELECT 1 FROM json_each(n.attrs,'` + path + `') j WHERE ` + s + `))`, a, err
	default:
		return "", nil, fmt.Errorf("unknown predicate op %q", p.Op)
	}
}

func filterSQL(f Filters) (string, []any, error) {
	parts := []string{"1"}
	args := []any{}
	ids := func(column string, values []string, exclude bool) {
		// json_each avoids SQLite's parameter limit for large consumer ID sets.
		b, _ := json.Marshal(values)
		op := " IN "
		if exclude {
			op = " NOT IN "
		}
		parts = append(parts, column+op+`(SELECT value FROM json_each(?))`)
		args = append(args, string(b))
	}
	if f.AllowedIDs != nil {
		ids("n.id", f.AllowedIDs, false)
	}
	if len(f.ExcludeIDs) > 0 {
		ids("n.id", f.ExcludeIDs, true)
	}
	if len(f.Types) > 0 {
		ids("n.type", f.Types, false)
	}
	if !f.Since.IsZero() {
		parts = append(parts, "n.ts>=?")
		args = append(args, timestamp(f.Since))
	}
	if f.Predicate != nil {
		count := 0
		s, a, err := predicateSQL(*f.Predicate, 0, &count)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, s)
		args = append(args, a...)
	}
	return strings.Join(parts, " AND "), args, nil
}
