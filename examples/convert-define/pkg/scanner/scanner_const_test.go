package scanner_test

import (
	"context"
	"testing"

	"fmt"
	"regexp"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scantest"

	scan "github.com/podhmo/minigo/examples/convert-define/pkg/goscan"
)

func TestConstantEvaluation(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		wantConsts    map[string]string // map[constName]constValue (literal representation)
		wantConstsRaw map[string]string // map[constName]constRawValue (raw string value)
		wantErr       bool
		only          bool
	}{
		{
			name: "simple literal",
			content: `
package main
const MyConst = "hello"
`,
			wantConsts:    map[string]string{"MyConst": `"hello"`},
			wantConstsRaw: map[string]string{"MyConst": "hello"},
		},
		{
			name: "integer literal",
			content: `
package main
const MyInt = 123
`,
			wantConsts: map[string]string{"MyInt": "123"},
		},
		{
			name: "binary expression",
			content: `
package main
const Two = 2
const Four = Two * 2
`,
			wantConsts: map[string]string{"Two": "2", "Four": "4"},
		},
		{
			name: "iota",
			content: `
package main
const (
	A = iota // 0
	B        // 1
	C        // 2
)
`,
			wantConsts: map[string]string{"A": "0", "B": "1", "C": "2"},
		},
		{
			name: "iota with expression",
			content: `
package main
const (
	KB = 1 << (10 * iota)
	MB
	GB
)
`,
			wantConsts: map[string]string{"KB": "1", "MB": "1024", "GB": "1048576"},
		},
		{
			name: "unary expression",
			content: `
package main
const X = 10
const Y = -X
`,
			wantConsts: map[string]string{"X": "10", "Y": "-10"},
		},
		{
			name: "parentheses",
			content: `
package main
const Z = (1 + 2) * 3
`,
			wantConsts: map[string]string{"Z": "9"},
		},
		{
			name: "cross-file constants",
			content: `
package main
const A = 1
---
package main
const B = A + 1
`,
			wantConsts: map[string]string{"A": "1", "B": "2"},
		},
		{
			name: "untyped float",
			content: `
package main
const Pi = 3.14
`,
			wantConsts: map[string]string{"Pi": "3.14"},
		},
		{
			name: "complex constant dependency",
			content: `
package main
const (
    c0 = iota
    c1 = iota
    c2 = iota
)
const (
    a = 1
    b = 2
    c = 3
)
const z = a + b + c
`,
			wantConsts: map[string]string{
				"c0": "0", "c1": "1", "c2": "2",
				"a": "1", "b": "2", "c": "3",
				"z": "6",
			},
		},
		{
			name: "simple shift",
			content: `
package main
const C = 1 << 0
`,
			wantConsts: map[string]string{"C": "1"},
		},
		{
			name: "stdlib panic",
			content: `
package main
const C = -1 << 63
`,
			wantConsts: map[string]string{"C": "-9223372036854775808"},
		},
		{
			name: "string with escape sequences",
			content: `
package main
const SpecialChars = "hello\x00world\n\t\""
`,
			wantConsts:    map[string]string{"SpecialChars": `"hello\x00world\n\t\""`},
			wantConstsRaw: map[string]string{"SpecialChars": "hello\x00world\n\t\""},
		},
	}

	for _, tt := range tests {
		if tt.only {
			tests = []struct {
				name          string
				content       string
				wantConsts    map[string]string
				wantConstsRaw map[string]string
				wantErr       bool
				only          bool
			}{tt}
			break
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 1. Create file map from content string
			fileContents := strings.Split(tt.content, "---")
			files := make(map[string]string)
			files["go.mod"] = "module my-temp-module\n" // Make the temp dir a module
			var pkgName string
			for i, fc := range fileContents {
				trimmedContent := strings.TrimSpace(fc)
				if pkgName == "" {
					if matches := regexp.MustCompile(`^package\s+([a-zA-Z0-9_]+)`).FindStringSubmatch(trimmedContent); len(matches) > 1 {
						pkgName = matches[1]
					} else {
						pkgName = "main" // fallback
					}
				}
				fileName := fmt.Sprintf("%s/file%d.go", pkgName, i)
				files[fileName] = trimmedContent
			}

			// 2. Write files to temp directory
			dir, cleanup := scantest.WriteFiles(t, files)
			defer cleanup()

			// 3. Define the action to run after scanning
			action := func(ctx context.Context, s *scan.Scanner, pkgs []*scan.Package) error {
				if len(pkgs) != 1 {
					return fmt.Errorf("expected 1 package, got %d", len(pkgs))
				}
				pkg := pkgs[0]

				if len(pkg.Constants) != len(tt.wantConsts) {
					return fmt.Errorf("got %d constants, want %d", len(pkg.Constants), len(tt.wantConsts))
				}

				gotConsts := make(map[string]string)
				gotConstsRaw := make(map[string]string)
				for _, c := range pkg.Constants {
					gotConsts[c.Name] = c.Value
					if c.RawValue != "" || tt.wantConstsRaw != nil {
						gotConstsRaw[c.Name] = c.RawValue
					}
				}

				if diff := cmp.Diff(tt.wantConsts, gotConsts); diff != "" {
					return fmt.Errorf("constants 'Value' mismatch (-want +got):\n%s", diff)
				}

				if tt.wantConstsRaw != nil {
					if diff := cmp.Diff(tt.wantConstsRaw, gotConstsRaw); diff != "" {
						return fmt.Errorf("constants 'RawValue' mismatch (-want +got):\n%s", diff)
					}
				}
				return nil
			}

			// 4. Run the scan and action
			ctx := context.Background()
			_, err := scantest.Run(t, ctx, dir, []string{"./" + pkgName}, action)
			if err != nil {
				if !tt.wantErr {
					t.Fatalf("scantest.Run() failed: %v", err)
				}
			} else if tt.wantErr {
				t.Fatalf("scantest.Run() did not return an error, but one was expected")
			}
		})
	}
}
