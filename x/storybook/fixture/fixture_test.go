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
