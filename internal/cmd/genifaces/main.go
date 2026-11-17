// Command genifaces generates vm/scriptiface_gen.go: the scriptIface
// adapter family's optional-method variants. Each family is a base
// proxy type plus a table of optional methods; every non-empty subset
// gets a variant named <base><suffixes>, so adding an optional surface
// does not mean hand-writing 2^n-1 structs. The table lives in
// table.go.
//
// Regenerate after editing the table:
//
//	go run ./internal/cmd/genifaces
package main

import (
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	// go generate runs in the directive file's dir and exports GOFILE;
	// plain `go run ./internal/cmd/genifaces` runs at the repo root.
	out := "vm/scriptiface_gen.go"
	if f := os.Getenv("GOFILE"); f != "" {
		out = filepath.Join(filepath.Dir(f), "scriptiface_gen.go")
	}
	var b strings.Builder
	emit(&b)
	src, err := format.Source([]byte(b.String()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "genifaces:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, src, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "genifaces:", err)
		os.Exit(1)
	}
}
