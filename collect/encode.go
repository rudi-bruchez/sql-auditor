package collect

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Shape int

const (
	ShapeObject Shape = iota
	ShapeArray
)

// RootSetName is the reserved result-set name whose properties merge into the
// document's top level instead of nesting under a key.
const RootSetName = "root"

type ResultSpec struct {
	Name  string
	Shape Shape
}

// ResultSet is one materialised result set. Types is optional and used only
// to sharpen warning messages.
type ResultSet struct {
	Columns []string
	Types   []string
	Rows    [][]any
}

type NamedResultSet struct {
	Spec ResultSpec
	Set  ResultSet
}

// node is an ordered JSON object. The slice drives marshalling order; the map
// exists only for lookup. Marshalling never touches the map's iteration order,
// which is what keeps output in SELECT order.
type node struct {
	keys []string
	vals map[string]any // value is json.RawMessage or *node
}

func newNode() *node { return &node{vals: map[string]any{}} }

func (n *node) child(key string) (*node, error) {
	if existing, ok := n.vals[key]; ok {
		c, ok := existing.(*node)
		if !ok {
			return nil, fmt.Errorf("conflict at %q: used as both a value and an object", key)
		}
		return c, nil
	}
	c := newNode()
	n.keys = append(n.keys, key)
	n.vals[key] = c
	return c, nil
}

func (n *node) set(key string, raw json.RawMessage) error {
	if existing, ok := n.vals[key]; ok {
		if _, isNode := existing.(*node); isNode {
			return fmt.Errorf("conflict at %q: used as both an object and a value", key)
		}
		return fmt.Errorf("duplicate property %q", key)
	}
	n.keys = append(n.keys, key)
	n.vals[key] = raw
	return nil
}

// merge folds another node's properties into this one, preserving order and
// refusing to overwrite. Used only for the root result set.
func (n *node) merge(other *node) error {
	for _, k := range other.keys {
		if _, exists := n.vals[k]; exists {
			return fmt.Errorf("duplicate property %q while merging the root result set", k)
		}
		n.keys = append(n.keys, k)
		n.vals[k] = other.vals[k]
	}
	return nil
}

func (n *node) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range n.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(n.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Encode assembles named result sets into one JSON document, preserving
// SELECT order throughout. It returns the document, any non-fatal warnings,
// and an error for structural problems.
func Encode(sets []NamedResultSet) ([]byte, []string, error) {
	root := newNode()
	var warnings []string

	for _, ns := range sets {
		isRoot := ns.Spec.Name == RootSetName
		if isRoot && ns.Spec.Shape != ShapeObject {
			return nil, nil, fmt.Errorf("result set %q must be declared object, not array", RootSetName)
		}
		switch ns.Spec.Shape {
		case ShapeObject:
			if len(ns.Set.Rows) > 1 {
				return nil, nil, fmt.Errorf("result set %q is declared object but returned %d rows",
					ns.Spec.Name, len(ns.Set.Rows))
			}
			if len(ns.Set.Rows) == 0 {
				if isRoot {
					continue // nothing to merge
				}
				// "asked, nothing there" — distinct from an empty object.
				if err := root.set(ns.Spec.Name, json.RawMessage("null")); err != nil {
					return nil, nil, err
				}
				continue
			}
			row, w, err := buildRow(ns.Spec.Name, ns.Set.Columns, ns.Set.Types, ns.Set.Rows[0])
			if err != nil {
				return nil, nil, err
			}
			warnings = append(warnings, w...)
			if isRoot {
				// Merge the root set's properties into the document itself,
				// preserving order, so dotted groups land at the top level.
				if err := root.merge(row); err != nil {
					return nil, nil, err
				}
				continue
			}
			if err := root.set(ns.Spec.Name, mustRaw(row)); err != nil {
				return nil, nil, err
			}
		case ShapeArray:
			items := make([]json.RawMessage, 0, len(ns.Set.Rows))
			for _, r := range ns.Set.Rows {
				row, w, err := buildRow(ns.Spec.Name, ns.Set.Columns, ns.Set.Types, r)
				if err != nil {
					return nil, nil, err
				}
				warnings = append(warnings, w...)
				items = append(items, mustRaw(row))
			}
			raw, err := json.Marshal(items)
			if err != nil {
				return nil, nil, err
			}
			if err := root.set(ns.Spec.Name, raw); err != nil {
				return nil, nil, err
			}
		}
	}
	out, err := json.Marshal(root)
	return out, warnings, err
}

func mustRaw(n *node) json.RawMessage {
	b, err := json.Marshal(n)
	if err != nil {
		// node.MarshalJSON only fails on values already vetted by encodeValue.
		panic(fmt.Sprintf("encode: unexpected marshal failure: %v", err))
	}
	return b
}

// buildRow expands dotted column aliases into nested nodes. types is indexed
// like columns and may be shorter or empty, in which case formatting falls
// back to the Go value.
func buildRow(setName string, columns []string, types []string, values []any) (*node, []string, error) {
	if len(columns) != len(values) {
		return nil, nil, fmt.Errorf("result set %q: %d columns but %d values",
			setName, len(columns), len(values))
	}
	root := newNode()
	var warnings []string
	for i, col := range columns {
		parts := splitAlias(col)
		cur := root
		for _, p := range parts[:len(parts)-1] {
			c, err := cur.child(p)
			if err != nil {
				return nil, nil, fmt.Errorf("result set %q, column %q: %w", setName, col, err)
			}
			cur = c
		}
		sqlType := ""
		if i < len(types) {
			sqlType = strings.ToUpper(types[i])
		}
		raw, warn := encodeValue(values[i], sqlType)
		if warn != "" {
			warnings = append(warnings, fmt.Sprintf("%s.%s: %s", setName, col, warn))
		}
		if err := cur.set(parts[len(parts)-1], raw); err != nil {
			return nil, nil, fmt.Errorf("result set %q, column %q: %w", setName, col, err)
		}
	}
	return root, warnings, nil
}

// splitAlias splits on dots. A literal dot is written [[a.b]] and is not a
// separator. Not expected in this corpus; defined so lint can reject anything
// ambiguous rather than guess.
func splitAlias(col string) []string {
	if strings.HasPrefix(col, "[[") && strings.HasSuffix(col, "]]") {
		return []string{col[2 : len(col)-2]}
	}
	return strings.Split(col, ".")
}

// encodeValue formats one value. sqlType is the driver's DatabaseTypeName,
// upper-cased; it is required because four SQL types are indistinguishable
// from the Go value alone — DATETIME and DATETIMEOFFSET and TIME and DATE all
// arrive as time.Time, and DECIMAL, VARBINARY and UNIQUEIDENTIFIER all arrive
// as []byte. Formatting from the dynamic type would emit a decimal as a hex
// string.
func encodeValue(v any, sqlType string) (json.RawMessage, string) {
	if v == nil {
		return json.RawMessage("null"), ""
	}
	switch sqlType {
	case "DATETIMEOFFSET":
		if t, ok := v.(time.Time); ok {
			return mustMarshal(t.Format(time.RFC3339)), ""
		}
	case "DATETIME", "DATETIME2", "SMALLDATETIME":
		if t, ok := v.(time.Time); ok {
			// ISO 8601 local, no offset. Emitting Z or -00:00 would assert a
			// UTC conversion that never happened.
			return mustMarshal(t.Format("2006-01-02T15:04:05")), ""
		}
	case "DATE":
		if t, ok := v.(time.Time); ok {
			return mustMarshal(t.Format("2006-01-02")), ""
		}
	case "TIME":
		if t, ok := v.(time.Time); ok {
			return mustMarshal(t.Format("15:04:05")), ""
		}
	case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
		return encodeNumericText(numericText(v))
	case "UNIQUEIDENTIFIER":
		if s, ok := guidString(v); ok {
			return mustMarshal(s), ""
		}
	case "BINARY", "VARBINARY", "IMAGE", "TIMESTAMP", "ROWVERSION":
		if b, ok := v.([]byte); ok {
			return mustMarshal("0x" + strings.ToUpper(hex.EncodeToString(b))), ""
		}
	case "SQL_VARIANT":
		return mustMarshal(fmt.Sprint(v)), "sql_variant column: cast it explicitly in the query"
	}

	// Fall through on the Go value for the ordinary cases.
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return json.RawMessage("null"), "non-finite float encoded as null"
		}
		return mustMarshal(x), ""
	case float32:
		return encodeValue(float64(x), "")
	case []byte:
		return mustMarshal(string(x)), ""
	case time.Time:
		return mustMarshal(x.Format("2006-01-02T15:04:05")), ""
	default:
		return mustMarshal(v), ""
	}
}

// numericText normalises whatever the driver hands back for a decimal —
// []byte, string, or an already-numeric value — into its text form.
func numericText(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprint(x)
	}
}

// encodeNumericText emits a decimal as a JSON number when float64 represents
// it exactly, and as a string beyond that, so precision is never silently
// lost. The current corpus tops out at DECIMAL(14,1), far inside the ceiling.
func encodeNumericText(s string) (json.RawMessage, string) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return mustMarshal(s), "decimal value not parseable as a number, carried as a string"
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return json.RawMessage("null"), "non-finite decimal encoded as null"
	}
	if strconv.FormatFloat(f, 'f', -1, 64) != trimNumeric(s) {
		return mustMarshal(s), "decimal beyond float64 precision, carried as a string"
	}
	return json.RawMessage(strconv.FormatFloat(f, 'f', -1, 64)), ""
}

// trimNumeric drops a leading + and trailing zeros after a decimal point, so
// "10.50" and "10.5" compare equal when checking round-trip exactness.
func trimNumeric(s string) string {
	s = strings.TrimPrefix(s, "+")
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return s
}

// guidString renders a uniqueidentifier canonically. go-mssqldb returns
// mssql.UniqueIdentifier, which is a [16]byte with its own String method; a
// raw [16]byte or []byte is handled too so the encoder stays testable without
// importing the driver.
func guidString(v any) (string, bool) {
	type stringer interface{ String() string }
	if s, ok := v.(stringer); ok {
		return strings.ToLower(s.String()), true
	}
	var b []byte
	switch x := v.(type) {
	case []byte:
		b = x
	case [16]byte:
		b = x[:]
	default:
		return "", false
	}
	if len(b) != 16 {
		return "", false
	}
	return strings.ToLower(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])), true
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return mustMarshal(fmt.Sprint(v))
	}
	return b
}
