package minigo

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/runtime"
)

// TestHostBoxReachesIOHelpersUnadapted pins that a host struct value
// (`var sb strings.Builder`, `&bytes.Buffer{}`) reaches io.Copy & co. as
// the host value itself. Adapting it through the script-callback proxy
// works too, but converts every chunk to a script []byte and back — a
// 32MB io.Copy into a strings.Builder took ~100ms instead of ~15ms.
func TestHostBoxReachesIOHelpersUnadapted(t *testing.T) {
	sb := &strings.Builder{}
	sbBox := runtime.Tag(hostType("strings.Builder", func() any { return &strings.Builder{} }), &runtime.GoValue{V: sb})
	buf := bytes.NewBufferString("x")
	bufBox := runtime.Tag(hostType("bytes.Buffer", func() any { return &bytes.Buffer{} }), &runtime.GoValue{V: buf})

	w, err := asWriterVM(nil, &runtime.Cell{Elem: sbBox}) // &sb
	if err != nil {
		t.Fatalf("asWriterVM: %v", err)
	}
	h := &hostHelpers{e: NewEngine("testdata")}
	r, err := h.asReaderVM(nil, bufBox)
	if err != nil {
		t.Fatalf("asReaderVM: %v", err)
	}
	got := []string{fmt.Sprintf("%T", w), fmt.Sprintf("%T", r), fmt.Sprint(w == any(sb)), fmt.Sprint(r == any(buf))}
	want := []string{"*strings.Builder", "*bytes.Buffer", "true", "true"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("host box adaptation (-want +got):\n%s", diff)
	}
}
