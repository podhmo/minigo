package minigo

import (
	"fmt"
	"text/template/parse"

	"github.com/podhmo/minigo/runtime"
)

// bindTemplateParse binds text/template/parse natively. An interpreted
// text/template (`--src text/template`) then parses on the host — the
// lexer and parser were ~46% of oapi-codegen's instructions — while its
// exec walks the host trees: type switches match the boxed node types
// through HostNew, and field reads go through reflection.
func (e *Engine) bindTemplateParse() {
	node := func(name string, newT func() any) *runtime.TypeDef {
		return hostType("text/template/parse."+name, newT)
	}
	syms := map[string]runtime.Value{
		"Node": &runtime.TypeDef{Name: "parse.Node", Kind: runtime.KindInterface,
			MReqs: []string{"Type", "String", "Copy", "Position"}},
		// NodeType, Pos and Mode name the real host enums like
		// reflect.Kind does: constants are boxed host values, which
		// compare and compute like named ints and equal the Type(),
		// Pos and Mode values host nodes hand back.
		"NodeType": &runtime.TypeDef{Name: "parse.NodeType", Kind: runtime.KindNamedBasic,
			HostNew: func() any { return parse.NodeType(0) }},
		"Pos": &runtime.TypeDef{Name: "parse.Pos", Kind: runtime.KindNamedBasic,
			HostNew: func() any { return parse.Pos(0) }},
		"Mode": &runtime.TypeDef{Name: "parse.Mode", Kind: runtime.KindNamedBasic,
			HostNew: func() any { return parse.Mode(0) }},
		"ParseComments": &runtime.GoValue{V: parse.ParseComments},
		"SkipFuncCheck": &runtime.GoValue{V: parse.SkipFuncCheck},

		"Tree":           node("Tree", func() any { return new(parse.Tree) }),
		"ActionNode":     node("ActionNode", func() any { return new(parse.ActionNode) }),
		"BoolNode":       node("BoolNode", func() any { return new(parse.BoolNode) }),
		"BranchNode":     node("BranchNode", func() any { return new(parse.BranchNode) }),
		"BreakNode":      node("BreakNode", func() any { return new(parse.BreakNode) }),
		"ChainNode":      node("ChainNode", func() any { return new(parse.ChainNode) }),
		"CommandNode":    node("CommandNode", func() any { return new(parse.CommandNode) }),
		"CommentNode":    node("CommentNode", func() any { return new(parse.CommentNode) }),
		"ContinueNode":   node("ContinueNode", func() any { return new(parse.ContinueNode) }),
		"DotNode":        node("DotNode", func() any { return new(parse.DotNode) }),
		"FieldNode":      node("FieldNode", func() any { return new(parse.FieldNode) }),
		"IdentifierNode": node("IdentifierNode", func() any { return new(parse.IdentifierNode) }),
		"IfNode":         node("IfNode", func() any { return new(parse.IfNode) }),
		"ListNode":       node("ListNode", func() any { return new(parse.ListNode) }),
		"NilNode":        node("NilNode", func() any { return new(parse.NilNode) }),
		"NumberNode":     node("NumberNode", func() any { return new(parse.NumberNode) }),
		"PipeNode":       node("PipeNode", func() any { return new(parse.PipeNode) }),
		"RangeNode":      node("RangeNode", func() any { return new(parse.RangeNode) }),
		"StringNode":     node("StringNode", func() any { return new(parse.StringNode) }),
		"TemplateNode":   node("TemplateNode", func() any { return new(parse.TemplateNode) }),
		"TextNode":       node("TextNode", func() any { return new(parse.TextNode) }),
		"VariableNode":   node("VariableNode", func() any { return new(parse.VariableNode) }),
		"WithNode":       node("WithNode", func() any { return new(parse.WithNode) }),

		"New": &runtime.BuiltinFunc{Name: "parse.New", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("parse.New: want at least 1 argument, got %d", len(args))
			}
			funcs, err := funcNameSets(args[1:])
			if err != nil {
				return nil, err
			}
			return &runtime.GoValue{V: parse.New(str(goNative(args[0])), funcs...)}, nil
		}},
		"NewIdentifier": &runtime.BuiltinFunc{Name: "parse.NewIdentifier", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("parse.NewIdentifier: want 1 argument, got %d", len(args))
			}
			return &runtime.GoValue{V: parse.NewIdentifier(str(goNative(args[0])))}, nil
		}},
		"IsEmptyTree": &runtime.BuiltinFunc{Name: "parse.IsEmptyTree", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("parse.IsEmptyTree: want 1 argument, got %d", len(args))
			}
			if isNilIface(args[0]) {
				return true, nil // parse.IsEmptyTree(nil) is true
			}
			n, ok := goNative(args[0]).(parse.Node)
			if !ok {
				return nil, fmt.Errorf("parse.IsEmptyTree: %T is not a parse.Node", args[0])
			}
			return parse.IsEmptyTree(n), nil
		}},
		"Parse": &runtime.BuiltinFunc{Name: "parse.Parse", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 4 {
				return nil, fmt.Errorf("parse.Parse: want at least 4 arguments, got %d", len(args))
			}
			funcs, err := funcNameSets(args[4:])
			if err != nil {
				return nil, err
			}
			trees, err := parse.Parse(str(goNative(args[0])), str(goNative(args[1])),
				str(goNative(args[2])), str(goNative(args[3])), funcs...)
			m := &runtime.Map{Pairs: map[runtime.Value]runtime.Value{}}
			for name, t := range trees {
				m.Insert(name, &runtime.GoValue{V: t})
			}
			return &runtime.Tuple{Elems: []runtime.Value{m, errVal(err)}}, nil
		}},
	}
	for _, nt := range []parse.NodeType{
		parse.NodeText, parse.NodeAction, parse.NodeBool, parse.NodeChain,
		parse.NodeCommand, parse.NodeDot, parse.NodeField, parse.NodeIdentifier,
		parse.NodeIf, parse.NodeList, parse.NodeNil, parse.NodeNumber,
		parse.NodePipe, parse.NodeRange, parse.NodeString, parse.NodeTemplate,
		parse.NodeVariable, parse.NodeWith, parse.NodeComment, parse.NodeBreak,
		parse.NodeContinue,
	} {
		syms[nodeTypeName(nt)] = &runtime.GoValue{V: nt}
	}
	e.Bind("text/template/parse", syms)
}

// funcNameSets turns parse.Parse's func-map arguments into the name
// sets the host parser checks identifiers against: it reads only the
// keys (and that the value is non-nil), so script func values never
// cross. A spread slice of maps arrives as a single Spread argument.
func funcNameSets(args []runtime.Value) ([]map[string]any, error) {
	var maps []runtime.Value
	for _, a := range args {
		if sp, ok := a.(*runtime.Spread); ok {
			maps = append(maps, sp.S.Elems...)
			continue
		}
		maps = append(maps, a)
	}
	out := make([]map[string]any, 0, len(maps))
	for _, mv := range maps {
		set := map[string]any{}
		switch m := runtime.Unwrap(mv).(type) {
		case *runtime.Map:
			for i := range m.Len() {
				k, v := m.At(i)
				if s, ok := runtime.Unwrap(k).(string); ok && !isNilIface(v) {
					set[s] = struct{}{}
				}
			}
		case runtime.Nil, *runtime.TypedNil:
		default:
			return nil, fmt.Errorf("parse.Parse: func map argument is %T", mv)
		}
		out = append(out, set)
	}
	return out, nil
}

// isNilIface reports whether v is a nil interface value. A typed nil
// (a nil func stored in an `any` map slot) is a non-nil interface, so
// the parser accepts its name like Go does.
func isNilIface(v runtime.Value) bool {
	switch v.(type) {
	case nil, runtime.Nil:
		return true
	}
	return runtime.IsNilIface(v)
}

// nodeTypeName spells a NodeType constant's identifier.
func nodeTypeName(nt parse.NodeType) string {
	names := map[parse.NodeType]string{
		parse.NodeText: "NodeText", parse.NodeAction: "NodeAction", parse.NodeBool: "NodeBool",
		parse.NodeChain: "NodeChain", parse.NodeCommand: "NodeCommand", parse.NodeDot: "NodeDot",
		parse.NodeField: "NodeField", parse.NodeIdentifier: "NodeIdentifier", parse.NodeIf: "NodeIf",
		parse.NodeList: "NodeList", parse.NodeNil: "NodeNil", parse.NodeNumber: "NodeNumber",
		parse.NodePipe: "NodePipe", parse.NodeRange: "NodeRange", parse.NodeString: "NodeString",
		parse.NodeTemplate: "NodeTemplate", parse.NodeVariable: "NodeVariable", parse.NodeWith: "NodeWith",
		parse.NodeComment: "NodeComment", parse.NodeBreak: "NodeBreak", parse.NodeContinue: "NodeContinue",
	}
	return names[nt]
}
