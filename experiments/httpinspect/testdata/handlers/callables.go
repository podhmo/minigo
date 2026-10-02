package handlers

import (
	web "net/http"
	"strconv"

	util "github.com/podhmo/minigo/experiments/httpinspect/testdata/helpers"
)

type localReader struct {
	Request *web.Request
	Key     string
}

func (x localReader) Read() string { return query(x.Request, x.Key) }
func LocalMethod(_ web.ResponseWriter, r *web.Request) {
	x := localReader{Request: r, Key: "local-method"}
	_ = x.Read()
}
func ImportedMethod(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "method"}
	_, _ = strconv.Atoi(x.Read())
}
func MethodValues(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "old"}
	valueRead := x.Read
	pointerRead := x.PointerRead
	x.Key = "new"
	_ = valueRead()
	_ = pointerRead()
}
func MethodExpression(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "expression"}
	_ = util.Reader.Read(x)
}
func PointerMethodExpression(_ web.ResponseWriter, r *web.Request) {
	x := &util.Reader{Request: r, Key: "pointer-expression"}
	_ = (*util.Reader).PointerRead(x)
}
func PointerWrite(_ web.ResponseWriter, r *web.Request) {
	x := &util.Reader{Request: r, Key: "before"}
	x.Rename("renamed")
	_ = x.PointerRead()
}
func receiverCopy(x util.Reader) string { x.Key = "copy"; return x.Read() }
func ReceiverCopy(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "original"}
	_ = receiverCopy(x)
	_ = x.Read()
}
func ClosureReassign(_ web.ResponseWriter, r *web.Request) {
	key := "before"
	read := func() string { return query(r, key) }
	key = "after"
	_ = read()
}
func ClosureWrite(_ web.ResponseWriter, r *web.Request) {
	key := "before"
	change := func() { key = "written" }
	change()
	_ = query(r, key)
}
func ClosureShadow(_ web.ResponseWriter, r *web.Request) {
	key := "outer"
	read := func() string { return query(r, key) }
	{
		key := "inner"
		readInner := func() string { return query(r, key) }
		_ = readInner()
	}
	_ = read()
}
func EscapedClosure(_ web.ResponseWriter, r *web.Request) {
	read := util.Factory(r, "escaped")
	_, _ = strconv.Atoi(util.Apply(read))
}
func NestedClosure(_ web.ResponseWriter, r *web.Request) {
	factory := util.Nested(r, "nested")
	read := factory()
	_ = read()
}
func ReceiverClosure(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "bound"}
	read := x.Bound()
	x.Key = "changed"
	_ = read()
}
func BranchCapture(_ web.ResponseWriter, r *web.Request) {
	key := "initial"
	read := func() string { return query(r, key) }
	if r == nil {
		key = "left"
	} else {
		key = "right"
	}
	_ = read()
}
func BranchCallable(_ web.ResponseWriter, r *web.Request) {
	var read func() string
	if r == nil {
		read = func() string { return query(r, "left-fn") }
	} else {
		read = func() string { return query(r, "right-fn") }
	}
	_ = read()
}
func ClosureReturnEffects(_ web.ResponseWriter, r *web.Request) {
	key := "initial"
	choose := func() {
		if r == nil {
			key = "early"
			return
		}
		key = "late"
	}
	choose()
	_ = query(r, key)
}
func RecursiveClosure(_ web.ResponseWriter, r *web.Request) {
	var read func() string
	read = func() string { _ = query(r, "recursive-closure"); return read() }
	_ = read()
}
func InterfaceUnknown(_ web.ResponseWriter, r *web.Request, reader interface{ Read() string }) {
	_ = r
	_ = reader.Read()
}

type embeddedReader struct{ util.Reader }

func Embedded(_ web.ResponseWriter, r *web.Request) {
	x := embeddedReader{Reader: util.Reader{Request: r, Key: "embedded"}}
	_ = x.Read()
}

func PointerReassign(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "old-variable"}
	read := x.PointerRead
	x = util.Reader{Request: r, Key: "new-variable"}
	_ = read()
}
func AddressReassign(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "old-address"}
	p := &x
	x = util.Reader{Request: r, Key: "new-address"}
	_ = p.PointerRead()
}
func PointerValueReassign(_ web.ResponseWriter, r *web.Request) {
	p := &util.Reader{Request: r, Key: "old-pointer"}
	read := p.PointerRead
	p = &util.Reader{Request: r, Key: "new-pointer"}
	_ = read()
}
func callableAlternatives(r *web.Request) func() string {
	key := "initial"
	if r == nil {
		return func() string { key = "left-call"; return query(r, key) }
	}
	return func() string { key = "right-call"; return query(r, key) }
}
func CallableAlternatives(_ web.ResponseWriter, r *web.Request) {
	read := callableAlternatives(r)
	_ = read()
}

func AssignmentOrder(_ web.ResponseWriter, r *web.Request) {
	first := &util.Reader{Request: r, Key: "first"}
	second := &util.Reader{Request: r, Key: "second"}
	p := first
	move := func() string { p = second; return "written-first" }
	p.Key = move()
	_ = first.PointerRead()
	_ = second.PointerRead()
}
func MethodEvaluationOrder(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "before-argument"}
	change := func() string { x.Key = "after-argument"; return "ignored" }
	_ = x.ReadAfter(change())
	_ = x.Read()
}
func KnownInterface(_ web.ResponseWriter, r *web.Request) {
	var reader interface{ Read() string } = util.Reader{Request: r, Key: "known-interface"}
	_ = reader.Read()
}

type nestedHolder struct{ Value util.Reader }

func NonlocalReceiver(_ web.ResponseWriter, r *web.Request) {
	h := nestedHolder{Value: util.Reader{Request: r, Key: "old-field"}}
	read := h.Value.PointerRead
	h.Value = util.Reader{Request: r, Key: "new-field"}
	_ = read()
}

func PointerValueMethodExpression(_ web.ResponseWriter, r *web.Request) {
	p := &util.Reader{Request: r, Key: "pointer-value-expression"}
	_ = (*util.Reader).Read(p)
}
func ArgumentCopyOrder(_ web.ResponseWriter, r *web.Request) {
	x := util.Reader{Request: r, Key: "before-copy"}
	change := func() string { x.Key = "after-copy"; return "ignored" }
	_ = util.ReadWithArg(x, change())
	_ = x.Read()
}
