package generator

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo/examples/convert-define/model"
)

// TestEmitterFreshAvoidsVisibleNames pins the temporary-name invariant:
// hoisted temporaries skip every identifier visible in the converter —
// params, user-declared variables, same-package funcs, import aliases.
func TestEmitterFreshAvoidsVisibleNames(t *testing.T) {
	im := NewImportManager("example.com/self")
	im.Add("example.com/s", "s")
	info := &model.ParsedInfo{GlobalRules: []model.TypeRule{{UsingFunc: "item"}}}
	pair := &TemplatePair{Pair: model.ConversionPair{Variables: []model.Variable{{Name: "v", Type: "int"}}}}
	e := newEmitter(im, nil, info, funcNamer{}, newGenDiags(), pair)

	got := []string{e.fresh("s"), e.fresh("item"), e.fresh("v"), e.fresh("src"), e.fresh("s")}
	want := []string{"s2", "item2", "v2", "src2", "s3"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
