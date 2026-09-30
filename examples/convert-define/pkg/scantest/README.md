# scantest

The `scantest` package provides a powerful test harness for tools built with the `go-scan` library. It simplifies integration testing by managing temporary file creation, scanner configuration, and result verification.

## Quick Start

The main function is `scantest.Run`, which sets up a test scenario. It creates a temporary directory, writes your source files to it, configures and runs the scanner, and then executes an `ActionFunc` where you can perform your analysis and assertions.

### Example

Here's how to test a simple package scan:

```go
package scantest_test

import (
	"context"
	"testing"

	"github.com/podhmo/minigo/examples/convert-define"
	"github.com/podhmo/minigo/examples/convert-define/pkg/scantest"
)

func TestScantest(t *testing.T) {
	// 1. Define the source files for the test.
	files := map[string]string{
		"go.mod": "module my-module",
		"main.go": `
package main
type Person struct {
    Name string
}`,
	}

	// 2. Create a temporary directory with these files.
	dir, cleanup := scantest.WriteFiles(t, files)
	defer cleanup()

	// 3. Define an action function to run after the scan.
	action := func(ctx context.Context, s *scan.Scanner, pkgs []*scan.Package) error {
		// 4. Perform assertions inside the action.
		if len(pkgs) != 1 {
			t.Errorf("expected 1 package, but got %d", len(pkgs))
			return nil
		}
		pkg := pkgs[0]
		person, ok := pkg.LookupType("Person")
		if !ok {
			t.Error("Person type not found")
			return nil
		}
		if person.Name != "Person" {
			t.Errorf("expected type name to be Person, but got %s", person.Name)
		}
		return nil
	}

	// 5. Run the test.
	// scantest.Run handles creating the scanner and scanning the package.
	// Here, we scan the package located at the root of our temporary directory.
	_, err := scantest.Run(t, context.Background(), dir, []string{"."}, action)
	if err != nil {
		t.Fatalf("scantest.Run failed: %v", err)
	}
}
```

The `scantest` package manages the complexity of setting up a realistic scanning environment, allowing you to focus on testing the logic of your analysis tool.