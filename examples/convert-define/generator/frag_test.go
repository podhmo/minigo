package generator

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/examples/convert-define/model"
)

// TestEmitterFreshAvoidsVisibleNames pins the temporary-name invariant:
// hoisted temporaries skip every identifier visible in the converter —
// params, same-package funcs and identifiers, import aliases.
func TestEmitterFreshAvoidsVisibleNames(t *testing.T) {
	im := NewImportManager("example.com/self")
	im.Add("example.com/s", "s")
	info := &model.ParsedInfo{GlobalRules: []model.TypeRule{{UsingFunc: "item"}}}
	pair := &TemplatePair{Fields: []FieldMap{{Converter: "v"}}}
	e := newEmitter(im, nil, info, funcNamer{}, newGenDiags(), pair, []string{"m", "key"})

	got := []string{e.fresh("s"), e.fresh("item"), e.fresh("v"), e.fresh("src"), e.fresh("s"), e.fresh("m"), e.fresh("key")}
	want := []string{"s2", "item2", "v2", "src2", "s3", "m2", "key2"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
