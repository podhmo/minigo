package minigo_test

// Test for the inspect-body experiment: testdata/inspectbody analyzes
// testdata/inspectapp handlers via inspect.Ops (the lifted op-dataflow
// view of function bodies) and reports an OpenAPI-ish parameter table.

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
)

func TestInspectBodyAnalyze(t *testing.T) {
	e := minigo.NewEngine(".")
	v := run(t, e, "./testdata/inspectbody", "Analyze", "github.com/podhmo/minigo/testdata/inspectapp")
	got, ok := v.(string)
	if !ok {
		t.Fatalf("Analyze returned %T (%v), want string", v, v)
	}
	want := `DELETE /items/{id}: id:path:string; reason:query:string
GET /items/{id}: id:path:string; note:form:string; sess:cookie:string
GET /items: X-Token:header:string; first:query:string; flag:query:string; limit:query:string; n:query:int; other:query:string; q:query:string; sort:query:string; tag:query:string; theme:query:string
POST /echo/{word}: f:form:string; word:path:string
PUT /items/{id}: Payload:body:string; id:path:string`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Analyze() mismatch (-want +got):\n%s\ngot:\n%s", diff, got)
	}
}
