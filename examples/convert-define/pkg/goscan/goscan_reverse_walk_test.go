package goscan

import (
	"context"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFindImporters(t *testing.T) {
	s, err := New(WithWorkDir("../testdata/walk"))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	tests := []struct {
		name          string
		targetPackage string
		wantImporters []string
	}{
		{
			name:          "importers of c",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/c",
			wantImporters: []string{
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/a",
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/b",
			},
		},
		{
			name:          "importers of a",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/a",
			wantImporters: []string{
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/d",
			},
		},
		{
			name:          "importers of b",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/b",
			wantImporters: []string{
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/a",
			},
		},
		{
			name:          "importers of d",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/d",
			wantImporters: []string{
				"github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/a",
			},
		},
		{
			name:          "no importers",
			targetPackage: "github.com/podhmo/minigo/examples/convert-define/pkg/testdata/walk/e", // non-existent package
			wantImporters: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			importers, err := s.Walker.FindImporters(context.Background(), tt.targetPackage)
			if err != nil {
				t.Fatalf("FindImporters() failed: %v", err)
			}

			gotImporters := make([]string, len(importers))
			for i, pkg := range importers {
				gotImporters[i] = pkg.ImportPath
			}

			sort.Strings(gotImporters)
			sort.Strings(tt.wantImporters)

			if diff := cmp.Diff(tt.wantImporters, gotImporters); diff != "" {
				t.Errorf("mismatch importers (-want +got):\n%s", diff)
			}
		})
	}
}
