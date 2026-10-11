package script

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// emit renders a doc (nested map[string]any / []any / scalars) as
// indented JSON text. Map keys come out sorted — deterministic by
// construction, a deliberate deviation from the reference tool's
// declaration-ordered output.
func emit(v any, indent string) string {
	var b strings.Builder
	writeJSON(&b, v, indent, 0)
	return b.String()
}

func writeJSON(b *strings.Builder, v any, indent string, depth int) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case map[string]any:
		writeMap(b, x, indent, depth)
	case []any:
		writeList(b, x, indent, depth)
	case []string:
		xs := make([]any, len(x))
		for i, e := range x {
			xs[i] = e
		}
		writeList(b, xs, indent, depth)
	case string:
		b.WriteString(quoteJSON(x))
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case float32:
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	default:
		b.WriteString(quoteJSON(fmt.Sprintf("%v", x)))
	}
}

func writeMap(b *strings.Builder, m map[string]any, indent string, depth int) {
	if len(m) == 0 {
		b.WriteString("{}")
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
		b.WriteString(strings.Repeat(indent, depth+1))
		b.WriteString(quoteJSON(k))
		b.WriteString(": ")
		writeJSON(b, m[k], indent, depth+1)
	}
	b.WriteString("\n")
	b.WriteString(strings.Repeat(indent, depth))
	b.WriteString("}")
}

// quoteJSON renders a Go string as a JSON string literal — unlike
// strconv.Quote it emits JSON-legal escapes only: control characters
// become \uXXXX and none of Go's \xNN forms leak into the document.
func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func writeList(b *strings.Builder, xs []any, indent string, depth int) {
	if len(xs) == 0 {
		b.WriteString("[]")
		return
	}
	b.WriteString("[")
	for i, x := range xs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
		b.WriteString(strings.Repeat(indent, depth+1))
		writeJSON(b, x, indent, depth+1)
	}
	b.WriteString("\n")
	b.WriteString(strings.Repeat(indent, depth))
	b.WriteString("]")
}
