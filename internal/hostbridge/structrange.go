package hostbridge

import (
	"go/ast"

	"github.com/podhmo/minigo/runtime"
)

func unparen(x ast.Expr) ast.Expr {
	for {
		p, ok := x.(*ast.ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

func receiverName(m *runtime.Function) string {
	if m == nil || m.Decl == nil || m.Decl.Recv == nil || len(m.Decl.Recv.List) == 0 {
		return ""
	}
	for _, n := range m.Decl.Recv.List[0].Names {
		return n.Name
	}
	return ""
}

func receiverFieldType(m *runtime.Function, name string) (ast.Expr, bool) {
	ts := typeSpecOf(m, m.Recv)
	if ts == nil {
		return nil, false
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return nil, false
	}
	for _, f := range st.Fields.List {
		for _, n := range f.Names {
			if n.Name == name {
				return f.Type, true
			}
		}
	}
	return nil, false
}

// nonChannelTypeExpr reports whether x is known not to be a channel.
// Unknown shapes are false so ranging over them stays conservative.
func nonChannelTypeExpr(m *runtime.Function, x ast.Expr, seen map[string]bool) bool {
	return typeExprMatches(m, x, seen, func(x ast.Expr) bool {
		switch x.(type) {
		case *ast.ArrayType, *ast.MapType:
			return true
		}
		return false
	})
}

// rangeKnownNonChannel recognizes the common receiver-field shape
// `range p.Items` when the field is statically a slice, array, or map.
func rangeKnownNonChannel(m *runtime.Function, x ast.Expr) bool {
	sel, ok := unparen(x).(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	name := receiverName(m)
	if !ok || recv.Name != name || receiverShadowed(m, name) {
		return false
	}
	typ, ok := receiverFieldType(m, sel.Sel.Name)
	return ok && nonChannelTypeExpr(m, typ, map[string]bool{})
}
