package fixture

import (
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

type recursiveView struct {
	Name string
	Next *recursiveView
}

type namedIntPointer *int

func TestNativeModes(t *testing.T) {
	if diff := cmp.Diff(view{}, Empty[view]()); diff != "" {
		t.Fatalf("Empty mismatch (-want +got):\n%s", diff)
	}
	wantMinimal := view{Name: "x", Count: 1, Tags: []string{"x"}, Inner: &inner{Enabled: true}}
	if diff := cmp.Diff(wantMinimal, Minimal[view]()); diff != "" {
		t.Fatalf("Minimal mismatch (-want +got):\n%s", diff)
	}
	maxText := "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
	wantMaximal := view{
		Name:  maxText,
		Count: int(^uint(0) >> 1),
		Tags:  []string{maxText, maxText, maxText},
		Inner: &inner{Enabled: true},
	}
	if diff := cmp.Diff(wantMaximal, Maximal[view]()); diff != "" {
		t.Fatalf("Maximal mismatch (-want +got):\n%s", diff)
	}
	longText := "supercalifragilisticexpialidocious-and-very-long-japanese-text-これはとても長い表示確認用の文章です"
	wantLong := wantMinimal
	wantLong.Name = longText
	wantLong.Tags = []string{longText}
	if diff := cmp.Diff(wantLong, LongText[view]()); diff != "" {
		t.Fatalf("LongText mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(recursiveView{Name: "x"}, Minimal[recursiveView]()); diff != "" {
		t.Fatalf("recursive Minimal mismatch (-want +got):\n%s", diff)
	}
	p := Minimal[namedIntPointer]()
	if p == nil {
		t.Fatal("named pointer Minimal is nil")
	}
	if diff := cmp.Diff(1, *p); diff != "" {
		t.Fatalf("named pointer Minimal mismatch (-want +got):\n%s", diff)
	}
}

func TestNativeCompositeShapes(t *testing.T) {
	type pair [2]string
	type shaped struct {
		Pair   pair
		Scores map[string]int
	}
	want := shaped{Pair: pair{"x", "x"}, Scores: map[string]int{"x": 1}}
	if diff := cmp.Diff(want, Minimal[shaped]()); diff != "" {
		t.Fatalf("composite Minimal mismatch (-want +got):\n%s", diff)
	}
}

func TestNativeComplexAndDeepShapes(t *testing.T) {
	if diff := cmp.Diff(complex128(complex(1, 1)), Minimal[complex128]()); diff != "" {
		t.Fatalf("complex128 Minimal mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(complex64(complex(1, 1)), Minimal[complex64]()); diff != "" {
		t.Fatalf("complex64 Minimal mismatch (-want +got):\n%s", diff)
	}

	type n1 struct{ S string }
	type n2 struct{ F n1 }
	type n3 struct{ F n2 }
	type n4 struct{ F n3 }
	type n5 struct{ F n4 }
	type n6 struct{ F n5 }
	type n7 struct{ F n6 }
	if diff := cmp.Diff("x", Minimal[n7]().F.F.F.F.F.F.S); diff != "" {
		t.Fatalf("deep Minimal mismatch (-want +got):\n%s", diff)
	}
}
