package main

import (
	"fmt"
	"sort"
	"strings"

	goruntime "runtime"
)

// exampleOutputEqual compares captured stdout with the // Output: text
// exactly like go test's processRunResult (testing/example.go): trim
// whitespace on the whole string only (a per-line trailing space
// difference still fails), normalize \r\n to \n on windows, and for a
// `// Unordered output:` comment compare the sorted line orderings.
func exampleOutputEqual(got, want string, unordered bool) bool {
	got, want = strings.TrimSpace(got), strings.TrimSpace(want)
	if goruntime.GOOS == "windows" {
		got = strings.ReplaceAll(got, "\r\n", "\n")
		want = strings.ReplaceAll(want, "\r\n", "\n")
	}
	if unordered {
		return sortLines(got) == sortLines(want)
	}
	return got == want
}

// sortLines is testing/example.go's sortLines: the output's lines
// sorted and rejoined, so an unordered comparison is one string equal.
func sortLines(s string) string {
	lines := strings.Split(s, "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func indentBlock(s, prefix string) string {
	if s == "" {
		return "(empty)"
	}
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}

var errTimeout = fmt.Errorf("timeout")

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "Traceback") && !strings.HasPrefix(l, "File ") {
			return l
		}
	}
	return ""
}
