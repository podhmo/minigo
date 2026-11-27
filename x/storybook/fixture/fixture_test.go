package fixture

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type inner struct {
	Enabled bool
}

type view struct {
	Name  string
	Count int
	Tags  []string
	Inner *inner
}

func TestNativeModes(t *testing.T) {
	empty := Empty[view]()
	if diff := cmp.Diff(view{}, empty); diff != "" {
		t.Fatalf("Empty mismatch (-want +got):\n%s", diff)
	}
	minimal := Minimal[view]()
	if minimal.Name != "x" || minimal.Count != 1 || len(minimal.Tags) != 1 || minimal.Inner == nil || !minimal.Inner.Enabled {
		t.Fatalf("unexpected Minimal: %+v", minimal)
	}
	maximal := Maximal[view]()
	if len(maximal.Name) != 64 || maximal.Count != int(^uint(0)>>1) || len(maximal.Tags) != 3 {
		t.Fatalf("unexpected Maximal: %+v", maximal)
	}
	longText := LongText[view]()
	if !strings.Contains(longText.Name, "表示確認") {
		t.Fatalf("unexpected LongText: %q", longText.Name)
	}
}
