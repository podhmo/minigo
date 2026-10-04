// A realistic JSON-lines transform: decode each event line, keep the
// error events, and re-emit a compact report — the kind of pipeline a
// generated probe cannot express.
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func main() {
	lines := []string{
		`{"level":"info","msg":"boot","ms":3}`,
		`{"level":"error","msg":"dial timeout","ms":812}`,
		`{"level":"error","msg":"retry","ms":44}`,
		`{"level":"warn","msg":"slow query","ms":150}`,
	}
	var errs []map[string]any
	for _, ln := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(ln), &ev); err != nil {
			panic(err)
		}
		if ev["level"] == "error" {
			errs = append(errs, ev)
		}
	}
	sort.Slice(errs, func(i, j int) bool {
		return errs[i]["ms"].(float64) > errs[j]["ms"].(float64)
	})
	var b strings.Builder
	for _, ev := range errs {
		out, _ := json.Marshal(ev)
		fmt.Fprintf(&b, "%s\n", out)
	}
	fmt.Fprintf(&b, "errors: %d\n", len(errs))
	fmt.Print(b.String())
}
