package stdfmt

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/internal/stdlib"
	stdruntime "github.com/podhmo/minigo/internal/stdlib/goruntime"
	stdio "github.com/podhmo/minigo/internal/stdlib/io"
	stdjson "github.com/podhmo/minigo/internal/stdlib/json"
	stdtime "github.com/podhmo/minigo/internal/stdlib/time"
	"github.com/podhmo/minigo/minireflect"
	"github.com/podhmo/minigo/runtime"
)

func Install(bind stdlib.Bind, h stdlib.Helpers, ad *stdio.Adapters, out func() io.Writer) {
	bind("fmt", map[string]runtime.Value{
		// fmt's interfaces are needed as types by interpreted sources that
		// reflect on them or assert (e.g. template's fmt.Stringer probes,
		// math/big's compile-time fmt.Scanner assertion).
		// Stringer/GoStringer carry their requirement as a real interface
		// AST (same pattern as `error`) so the facade's signature
		// machinery can report NumMethod/Method/MethodByName and check
		// Implements against `String() string`.
		"Stringer": &runtime.TypeDef{
			Name: "fmt.Stringer", Kind: runtime.KindInterface, MReqs: []string{"String"},
			Anon: &ast.InterfaceType{Methods: &ast.FieldList{List: []*ast.Field{{
				Names: []*ast.Ident{{Name: "String"}},
				Type: &ast.FuncType{
					Params:  &ast.FieldList{},
					Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}},
				},
			}}}},
		},
		"GoStringer": &runtime.TypeDef{
			Name: "fmt.GoStringer", Kind: runtime.KindInterface, MReqs: []string{"GoString"},
			Anon: &ast.InterfaceType{Methods: &ast.FieldList{List: []*ast.Field{{
				Names: []*ast.Ident{{Name: "GoString"}},
				Type: &ast.FuncType{
					Params:  &ast.FieldList{},
					Results: &ast.FieldList{List: []*ast.Field{{Type: ast.NewIdent("string")}}},
				},
			}}}},
		},
		"Formatter": &runtime.TypeDef{Name: "fmt.Formatter", Kind: runtime.KindInterface, MReqs: []string{"Format"}},
		"Scanner":   &runtime.TypeDef{Name: "fmt.Scanner", Kind: runtime.KindInterface, MReqs: []string{"Scan"}},
		"State":     &runtime.TypeDef{Name: "fmt.State", Kind: runtime.KindInterface, MReqs: []string{"Write", "Width", "Precision", "Flag"}},
		// Print/Fprint/Sprint need the flat script args: gc's operand
		// spacing reads the reflect Kind, so a named type over string
		// (json.Number) suppresses the space — the host-shaped args hide
		// that inside fmtValue.
		"Print": pffn("fmt.Print", 0, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return stdlib.RetErr(fprintOperands(out(), a, flat))
		}, fmt.Print),
		"Println": ffn("fmt.Println", -1, 0, func(a []any) (any, error) { return stdlib.RetErr(fmt.Fprintln(out(), a...)) }, fmt.Println),
		"Printf": ffn("fmt.Printf", 0, 1, func(a []any) (any, error) {
			return stdlib.RetErr(fmt.Fprintf(out(), stdlib.Str(a[0]), a[1:]...))
		}, fmt.Printf),
		// Fprint* take an explicit writer — os.Stdout/os.Stderr arrive as
		// GoValue (unwrapped by fmtArg to the native *os.File).
		"Fprint": pffn("fmt.Fprint", 1, func(v runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			w, err := ad.AsWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return stdlib.RetErr(fprintOperands(w, a[1:], flat[1:]))
		}),
		"Fprintf": vffn("fmt.Fprintf", 1, 2, func(v runtime.VMCaller, a []any) (any, error) {
			w, err := ad.AsWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return stdlib.RetErr(fmt.Fprintf(w, stdlib.Str(a[1]), a[2:]...))
		}),
		"Fprintln": vffn("fmt.Fprintln", -1, 1, func(v runtime.VMCaller, a []any) (any, error) {
			w, err := ad.AsWriterVM(v, a[0])
			if err != nil {
				return nil, err
			}
			return stdlib.RetErr(fmt.Fprintln(w, a[1:]...))
		}),
		"Sprint": pffn("fmt.Sprint", 0, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			var buf bytes.Buffer
			if _, err := fprintOperands(&buf, a, flat); err != nil {
				return nil, err
			}
			return buf.String(), nil
		}, fmt.Sprint),
		"Sprintln": ffn("fmt.Sprintln", -1, 0, func(a []any) (any, error) { return fmt.Sprintln(a...), nil }, fmt.Sprintln),
		"Sprintf": ffn("fmt.Sprintf", 0, 1, func(a []any) (any, error) {
			return fmt.Sprintf(stdlib.Str(a[0]), a[1:]...), nil
		}, fmt.Sprintf),
		// The Append family formats like Sprint/Sprintf/Sprintln onto a
		// caller's byte slice. The buffer arg is read from flat (the raw
		// script value): a[i] holds fmtValue boxes for composite args,
		// which byteSlice cannot see through. Source-interpreted packages
		// reach these through e.g. log's output(), which formats via
		// fmt.Appendln.
		"Append": pffn("fmt.Append", 1, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			var buf bytes.Buffer
			if _, err := fprintOperands(&buf, a[1:], flat[1:]); err != nil {
				return nil, err
			}
			return append(stdlib.ByteSlice(flat[0]), buf.Bytes()...), nil
		}, fmt.Append),
		"Appendf": wffn("fmt.Appendf", 1, 2, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return fmt.Appendf(stdlib.ByteSlice(flat[0]), stdlib.Str(a[1]), a[2:]...), nil
		}, fmt.Appendf),
		"Appendln": pffn("fmt.Appendln", 1, func(_ runtime.VMCaller, a []any, flat []runtime.Value) (any, error) {
			return fmt.Appendln(stdlib.ByteSlice(flat[0]), a[1:]...), nil
		}, fmt.Appendln),
		// The Scan family's out-params arrive as script refs; scanFn
		// mirrors each into a host var of the pointee's type, scans, and
		// writes results back through the ref (parse/node.go's
		// complex-literal path needs Sscan).
		"Sscan": scanFn("fmt.Sscan", 1, nil, fmt.Sscan, func(head, ptrs []any) (int, error) { return fmt.Sscan(stdlib.Str(head[0]), ptrs...) }),
		"Sscanf": scanFn("fmt.Sscanf", 2, nil, fmt.Sscanf, func(head, ptrs []any) (int, error) {
			return fmt.Sscanf(stdlib.Str(head[0]), stdlib.Str(head[1]), ptrs...)
		}),
		"Sscanln": scanFn("fmt.Sscanln", 1, nil, fmt.Sscanln, func(head, ptrs []any) (int, error) { return fmt.Sscanln(stdlib.Str(head[0]), ptrs...) }),
		"Scan":    scanFn("fmt.Scan", 0, nil, fmt.Scan, func(_, ptrs []any) (int, error) { return fmt.Scan(ptrs...) }),
		"Scanf":   scanFn("fmt.Scanf", 1, nil, fmt.Scanf, func(head, ptrs []any) (int, error) { return fmt.Scanf(stdlib.Str(head[0]), ptrs...) }),
		"Scanln":  scanFn("fmt.Scanln", 0, nil, fmt.Scanln, func(_, ptrs []any) (int, error) { return fmt.Scanln(ptrs...) }),
		"Fscan":   scanFn("fmt.Fscan", 1, func(v runtime.MemberCaller, a []runtime.Value) ([]any, error) { return scanReader(ad, v, a) }, fmt.Fscan, func(head, ptrs []any) (int, error) { return fmt.Fscan(head[0].(io.Reader), ptrs...) }),
		"Fscanf": scanFn("fmt.Fscanf", 2, func(v runtime.MemberCaller, a []runtime.Value) ([]any, error) { return scanReader(ad, v, a) }, fmt.Fscanf, func(head, ptrs []any) (int, error) {
			return fmt.Fscanf(head[0].(io.Reader), stdlib.Str(head[1]), ptrs...)
		}),
		"Fscanln": scanFn("fmt.Fscanln", 1, func(v runtime.MemberCaller, a []runtime.Value) ([]any, error) { return scanReader(ad, v, a) }, fmt.Fscanln, func(head, ptrs []any) (int, error) { return fmt.Fscanln(head[0].(io.Reader), ptrs...) }),
		// Errorf is hand-bound: %w verbs wrap the cause like Go's
		// fmt.wrapError so errors.Unwrap/Is/As see the chain.
		"Errorf": &runtime.BuiltinFunc{Name: "fmt.Errorf", Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("fmt.Errorf needs 1+ args")
			}
			a, flat := fmtArgs(v, args)
			spec := stdlib.Str(a[0])
			// the %w operand indexes come from the spec as written —
			// they index the raw args; rewriteTypeVerbs renumbers the
			// directives and rebuilds the operand list for printing.
			_, wrapPos, wrapDirs, wrapStars, wrapBadIdx := rewriteWrapVerbs(spec, len(flat)-1)
			// gc prints %!w(...) for a %w operand that wraps nothing —
			// nil, a non-error, or a missing operand — instead of its %v.
			// Classify once per %w verb; the marker text rides to host fmt
			// as an appended operand under a %[K]s directive, so a shared
			// operand index stays intact and each %w gets its own marker.
			var bad []string
			for k, p := range wrapPos {
				switch {
				case wrapBadIdx[k] || p+1 >= len(a) || p+1 >= len(flat):
					// a bad [n] makes the directive %!w(BADINDEX) even
					// when its operand index lands in range; a mere
					// operand run-out prints %!w(MISSING). A bad `*`
					// operand still prefixes %!(BADWIDTH).
					mk := "MISSING"
					if wrapBadIdx[k] {
						mk = "BADINDEX"
					}
					_, bw, bp := markerInner(wrapDirs[k][1:len(wrapDirs[k])-1], wrapStars[k], flat[1:])
					lit := ""
					if bw {
						lit = "%!(BADWIDTH)"
					}
					if bp {
						lit += "%!(BADPREC)"
					}
					bad = append(bad, lit+"%!w("+mk+")")
				default:
					if lit, isBad := badWrapOperand(v, flat[p+1], wrapDirs[k], wrapStars[k], flat[1:]); isBad {
						bad = append(bad, lit)
					} else {
						bad = append(bad, "")
					}
				}
			}
			spec, tail, raws := rewriteTypeVerbs(spec, a, flat, 0, v)
			a = append(a[:1], tail...)
			// indexes a directive's scan computes address the rebuilt
			// slot list — raws carries each slot's raw value so star and
			// reflect.Value checks read the same slot, not its old index.
			rvalIdx := map[int]bool{}
			for i, x := range raws {
				if isRValueArg(x) {
					rvalIdx[i] = true
				}
			}
			spec, a2 := rewriteWrapVerbsOps(spec, a[1:], raws, bad, rvalIdx)
			a = append(a[:1], a2...)
			msg := fmt.Sprintf(spec, a[1:]...)
			// like gc: the shape follows the raw count of %w verbs that
			// had an operand — one makes a wrapError (Unwrap() error),
			// several a wrapErrors (Unwrap() []error) over the distinct
			// operands in argument order; a non-error operand wraps
			// nothing.
			var pos []int
			for k, p := range wrapPos {
				if !wrapBadIdx[k] && p < len(args)-1 {
					pos = append(pos, p)
				}
			}
			switch len(pos) {
			case 0:
				return stdlib.ErrVal(errors.New(msg)), nil
			case 1:
				return stdlib.ErrVal(&stdlib.WrapError{Msg: msg, Err: stdlib.WrapOperand(v, args[pos[0]+1])}), nil
			}
			slices.Sort(pos)
			var errs []error
			for _, p := range slices.Compact(pos) {
				if err := stdlib.WrapOperand(v, args[p+1]); err != nil {
					errs = append(errs, err)
				}
			}
			return stdlib.ErrVal(&stdlib.WrapErrors{Msg: msg, Errs: errs}), nil
		}},
	})
}

// badWrapOperand classifies a %w operand like gc's handleMethods: a
// valid error wraps (and prints under %v); nil and non-error operands
// print %!w(<nil>) / %!w(TYPE=value) literal markers instead — the
// marker's inner value renders under a markerState built from the
func badWrapOperand(v runtime.VMCaller, x runtime.Value, dir string, stars []int, vals []runtime.Value) (string, bool) {
	if stdlib.WrapOperand(v, x) != nil {
		return "", false
	}
	switch runtime.Unwrap(x).(type) {
	case nil, runtime.Nil, *runtime.Nil, *runtime.IfaceNil:
		return "%!w(<nil>)", true
	}
	// dir is the directive text ("%[flags][width][.prec]w"); its tail
	// feeds markerInner's flag/width/precision parser — gc applies the
	// directive's own flags inside the marker while suppressing
	// Formatter/Stringer methods on the inner value. stars+vals let a
	// `*` width/precision read its operand: a non-integer one reports
	// %!(BADWIDTH)/%!(BADPREC) ahead of the marker like gc.
	st, badWidth, badPrec := markerInner(dir[1:len(dir)-1], stars, vals)
	inner := (&fmtValue{c: v, x: x}).render('v', st)
	var b strings.Builder
	if badWidth {
		b.WriteString("%!(BADWIDTH)")
	}
	if badPrec {
		b.WriteString("%!(BADPREC)")
	}
	fmt.Fprintf(&b, "%%!w(%s=%s)", ScriptTypeString(x), inner)
	return b.String(), true
}

// rewriteWrapVerbsOps rewrites each %w like rewriteWrapVerbs: a good
// operand keeps the directive as %v; a bad one (non-error, nil or
// missing) emits its %!w marker. gc's bad verb still consumes the
// operand — so when the operand index feeds no other directive the
// marker is substituted into the operand slot and %[P]s prints and
// consumes it; when the index is shared (`%[1]d %[1]w`), the marker
// goes in as %-escaped literal text and the sibling verb consumes the
// operand, leaving no EXTRA. bad lists one entry per %w in verb order,
// "" for a good operand.
func rewriteWrapVerbsOps(spec string, ops []any, raws []runtime.Value, bad []string, rvalIdx map[int]bool) (string, []any) {
	// opsLen is the operand count gc's argNum and [n] indexes span:
	// operands appended mid-rewrite (a parked "" an rvalue star reads)
	// must not become reachable by an OOB directive's MISSING/BADINDEX
	// checks, so every range test below compares against this snapshot.
	opsLen := len(ops)
	if !strings.Contains(spec, "w") {
		return spec, ops
	}

	var sb strings.Builder
	argNum := 0
	wAt := 0
	consumed := map[int]bool{}
	hadIndex := false
	dirBad := false
	dirIndexed := false
	// index mirrors rewriteWrapVerbs' cursor handling for `[n]`: a
	// valid in-range index moves the cursor; an out-of-range or
	// malformed one leaves argNum alone and marks the directive bad —
	// gc prints the verb's BADINDEX marker and consumes nothing.
	index := func(i int) int {
		if i < len(spec) && spec[i] == '[' {
			// gc's reordered flag rises on the '[' alone — even an
			// unterminated or malformed index suppresses EXTRA.
			hadIndex = true
			if k := strings.IndexByte(spec[i:], ']'); k > 1 {
				dirIndexed = true
				if n, err := strconv.Atoi(spec[i+1 : i+k]); err == nil && n >= 1 {
					if n-1 >= opsLen {
						dirBad = true
					} else {
						argNum = n - 1
					}
					return i + k + 1
				}
				// [0] / [x]: a parsed index that is not a positive
				// operand number fails like an out-of-range one.
				dirBad = true
				return i + k + 1
			}
			dirBad = true
			return i + 1
		}
		return i
	}
	digits := func(i int) int {
		for i < len(spec) && spec[i] >= '0' && spec[i] <= '9' {
			i++
		}
		return i
	}
	// a verb's operand indexes: the star args plus the value arg.
	// pos, posP, posV are the offsets where an explicit [n] would sit —
	// after the flags (the width star's index site), after the `.`
	// (the precision star's), and before the verb (the value's).
	// indexed/badIdx record a valid / failed [n] in this directive.
	type verbEv struct {
		pct, pos, posP, posV, i, arg int
		stars                        []int
		// starIdx[i] is the [n] byte range in spec that selects
		// stars[i]'s operand explicitly, or {-1,-1} when the `*` is
		// implicit — pin must rewrite an explicit site, not add one.
		// valIdx is the [n] byte range selecting the value arg, or
		// {-1,-1} when the value is implicit.
		starIdx [][2]int
		valIdx  [2]int
		// nw counts the leading width stars — stars[nw:] are
		// precision stars, whose marker is BADPREC, not BADWIDTH.
		nw              int
		indexed, badIdx bool
	}
	var evs []verbEv
	argNums := []int{}
	brokenPct := -1
	var broken verbEv
	i := 0
	for i < len(spec) {
		j := strings.IndexByte(spec[i:], '%')
		if j < 0 {
			break
		}
		pct := i + j
		i = pct + 1
		for i < len(spec) && strings.IndexByte("#+- 0", spec[i]) >= 0 {
			i++
		}
		stars := []int{}
		starIdx := [][2]int{}
		valIdx := [2]int{-1, -1}
		nw := 0
		pos := i
		site := i
		i = index(i)
		if i < len(spec) && spec[i] == '*' {
			stars = append(stars, argNum)
			if i == site {
				starIdx = append(starIdx, [2]int{-1, -1})
			} else {
				starIdx = append(starIdx, [2]int{site, i})
			}
			nw++
			if argNum < opsLen {
				argNum++
			}
			i++
		} else {
			// a [n] that no `*` claimed selects the value operand.
			if i != site {
				valIdx = [2]int{site, i}
			}
			i = digits(i)
		}
		posP := -1
		if i < len(spec) && spec[i] == '.' {
			posP = i + 1
			site = i + 1
			i = index(i + 1)
			if i < len(spec) && spec[i] == '*' {
				stars = append(stars, argNum)
				if i == site {
					starIdx = append(starIdx, [2]int{-1, -1})
				} else {
					starIdx = append(starIdx, [2]int{site, i})
				}
				if argNum < opsLen {
					argNum++
				}
				i++
			} else {
				if i != site {
					valIdx = [2]int{site, i}
				}
				i = digits(i)
			}
		}
		if posP < 0 {
			posP = i
		}
		posV := i
		site = i
		i = index(i)
		if i != site {
			valIdx = [2]int{site, i}
		}
		if i >= len(spec) {
			// a truncated directive: gc consumes its `*` operands
			// then prints %!(NOVERB) where the verb was due.
			brokenPct = pct
			if i > len(spec) {
				i = len(spec)
			}
			if pos >= len(spec) {
				pos = len(spec)
			}
			if posP < 0 || posP > len(spec) {
				posP = i
			}
			broken = verbEv{pct, pos, posP, posV, i, argNum, stars, starIdx, valIdx, nw, dirIndexed, dirBad}
			break
		}
		verb := spec[i]
		i++
		evs = append(evs, verbEv{pct, pos, posP, posV, i, argNum, stars, starIdx, valIdx, nw, dirIndexed, dirBad})
		if verb != '%' {
			argNums = append(argNums, argNum)
			if !dirBad {
				argNum++
			}
		}
		dirBad = false
		dirIndexed = false
	}
	// operand indexes a surviving directive consumes: every value arg
	// plus every `*` arg of a non-%w or good-%w verb. A bad %w's own
	// directive text is replaced, so its arg and stars are consumed
	// explicitly in the second pass — but a slot already claimed here
	// must keep its value or the sibling verb misprints (and a `*` of
	// another directive would read the marker as its width).
	shared := map[int]bool{}
	valUsed := map[int]bool{}
	wIdx := map[int]int{}
	wAtShared := 0
	for n, ev := range evs {
		c := spec[ev.i-1]
		if c == 'w' {
			wIdx[n] = wAtShared
			wAtShared++
			if wIdx[n] < len(bad) && bad[wIdx[n]] != "" {
				continue
			}
		}
		if c != '%' {
			valUsed[ev.arg] = true
			shared[ev.arg] = true
			for _, st := range ev.stars {
				shared[st] = true
			}
		}
	}
	// a `*` operand only read by stars is lifted to a native int so the
	// host fmt accepts the widths gc does — a bound named int or a
	// GoValue would print BADWIDTH instead. A slot another directive
	// also reads as a value keeps its marshalled form, or %T would lie.
	for _, ev := range evs {
		for _, st := range ev.stars {
			if st < opsLen && !valUsed[st] && !rvalIdx[st] && st < len(raws) {
				ops[st] = starOperand(ops[st], raws[st])
			}
		}
	}
	// deadStar reports whether gc's `*` operand read at ops[st] fails —
	// out of range or a reflect.Value gc's intFromArg rejects — so the
	// star prints its own marker and feeds nothing to the directive.
	// a star is dead when gc's intFromArg rejects its slot — out of
	// range, a reflect.Value, a non-integer, or a magnitude past gc's
	// 1e6 cap — so the rewrite prints the marker in star order and
	// drops the '*' byte; a live width left to the host would print
	// BADWIDTH after a dead prec's BADPREC, inverting gc's diagnostic
	// order.
	deadStar := func(st int) bool {
		if st >= opsLen || rvalIdx[st] {
			return true
		}
		w, ok := starInt(ops[st])
		return !ok || w > 1e6 || w < -1e6
	}
	// starMarkers prints the BADWIDTH/BADPREC marker text of every dead
	// star, before the directive itself opens: %% inside a directive is
	// not literal, so the markers have to come first, exactly where gc
	// prints them.
	starMarkers := func(sb *strings.Builder, ev verbEv) {
		for k, st := range ev.stars {
			if !deadStar(st) {
				continue
			}
			if k < ev.nw {
				sb.WriteString("%%!(BADWIDTH)")
			} else {
				sb.WriteString("%%!(BADPREC)")
			}
		}
	}
	// sbIdx writes an operand's [n] index — any index in the final
	// spec sets the host's reordered flag, whose side effect is
	// suppressing its %!(EXTRA ...) tail (emitted by hand below).
	// wroteIndex tracks the emitted ones: a literal '[' in the format
	// text ("wrap [%w]") is not an index.
	wroteIndex := false
	verbatimIndex := false
	sbIdx := func(sb *strings.Builder, n int) {
		sb.WriteByte('[')
		sb.WriteString(strconv.Itoa(n))
		sb.WriteByte(']')
		wroteIndex = true
	}
	// emitV writes directive-interior spec text: a '[' inside a
	// directive is index syntax (unlike a literal '[' in plain text,
	// which is not), so mark a surviving index for the park check.
	emitV := func(sb *strings.Builder, s string) {
		if strings.IndexByte(s, '[') >= 0 {
			verbatimIndex = true
		}
		sb.WriteString(s)
	}
	// emitStar writes star k's `*` operand over spec[lo:hi] inside the
	// directive: an in-range, non-rvalue slot is pinned to [st+1] (an
	// explicit `[n]` the directive already carries is rewritten, not
	// duplicated) so the host reads the same operand gc's argNum stood
	// on. A dead star's `*` byte — and its `[n]` site — is dropped:
	// its marker already printed, and the directive formats its value
	// anyway.
	dropped := false
	emitStar := func(sb *strings.Builder, spec string, ev verbEv, k, lo, hi int) {
		st := ev.stars[k]
		site := ev.starIdx[k]
		if deadStar(st) {
			dropped = true
			if site[0] >= 0 {
				sb.WriteString(spec[lo:site[0]])
				sb.WriteString(spec[site[1]+1 : hi]) // site[1] holds the '*'
			} else {
				sb.WriteString(spec[lo+1 : hi]) // lo holds the implicit '*'
			}
			return
		}
		if site[0] >= 0 {
			sb.WriteString(spec[lo:site[0]])
			sbIdx(sb, st+1)
			sb.WriteString(spec[site[1]:hi])
		} else {
			sbIdx(sb, st+1)
			sb.WriteString(spec[lo:hi])
		}
	}
	// emitCore writes the directive from its '%' through the value
	// operand's index site, with each `*` pinned or dropped. A dead
	// precision star takes its '.' with it; an explicit `[n]` the
	// directive already carries is rewritten, not duplicated.
	emitCore := func(sb *strings.Builder, spec string, ev verbEv, pct string) {
		sb.WriteString(pct)
		hiW := ev.posP
		if len(ev.stars) > ev.nw && deadStar(ev.stars[ev.nw]) &&
			hiW-1 >= ev.pos && spec[hiW-1] == '.' {
			hiW--
		}
		if ev.nw > 0 {
			emitStar(sb, spec, ev, 0, ev.pos, hiW)
		} else {
			emitV(sb, spec[ev.pos:hiW])
		}
		if len(ev.stars) > ev.nw {
			emitStar(sb, spec, ev, ev.nw, ev.posP, ev.posV)
		} else {
			emitV(sb, spec[ev.posP:ev.posV])
		}
	}
	// pin emits a directive with every operand index made explicit —
	// the host's implicit cursor no longer matches the operand gc reads
	// here. A value operand that ran past the list prints gc's MISSING
	// marker: the emitted %! pair closes into a '%' verb so any `*`
	// operands are still consumed before the literal text.
	pin := func(sb *strings.Builder, spec string, ev verbEv, verb string) {
		starMarkers(sb, ev)
		emitCore(sb, spec, ev, spec[ev.pct:ev.pos])
		if ev.valIdx[0] >= 0 {
			if ev.valIdx[0] >= ev.posV {
				emitV(sb, spec[ev.valIdx[0]:ev.valIdx[1]])
			}
			// a [n] at an earlier site already printed verbatim above.
			sb.WriteString(verb)
		} else if ev.arg < opsLen {
			sbIdx(sb, ev.arg+1)
			sb.WriteString(verb)
		} else {
			// %!v + literal: after star pins the lone '%' closes the
			// directive as a '%' verb (consuming the width operand);
			// with no stars it pairs with the directive's own '%' into
			// %%. Either way "!verb(MISSING)" prints literally.
			sb.WriteString("%!" + verb + "(MISSING)")
		}
	}
	last := 0
	fixRest := false
	for n, ev := range evs {
		sb.WriteString(spec[last:ev.pct])
		// the verb is a whole rune, not one byte — a multi-byte verb's
		// tail bytes still sit inside spec[last:].
		_, vw := utf8.DecodeRuneInString(spec[ev.i-1:])
		verb := spec[ev.i-1 : ev.i-1+vw]
		last = ev.i
		c := spec[ev.i-1]
		if c != 'w' {
			switch {
			case c == '%':
				// %% takes no operand — never pin it; a %*% after a
				// replaced directive still reads its width operand,
				// which the pin must name explicitly.
				if fixRest && len(ev.stars) > 0 {
					starMarkers(&sb, ev)
					emitCore(&sb, spec, ev, "%")
					sb.WriteString("%")
				} else {
					emitV(&sb, spec[ev.pct:ev.i])
				}
			case fixRest && ev.badIdx:
				// a bad [n] prints the verb's BADINDEX marker and its
				// value consumes nothing — emit it as text; each `*`
				// operand still reads its slot, printing a marker when
				// gc's read fails. The lone '%' of "%!verb" closes the
				// directive as a '%' verb so the stars stay consumed.
				starMarkers(&sb, ev)
				emitCore(&sb, spec, ev, "%")
				sb.WriteString("%!" + verb + "(BADINDEX)")
				last = ev.i - 1 + vw
			case fixRest && !ev.badIdx:
				// the host's implicit cursor no longer matches the
				// operand gc reads here — pin it explicitly.
				pin(&sb, spec, ev, verb)
				last = ev.i - 1 + vw
			default:
				emitV(&sb, spec[ev.pct:ev.i])
			}
			continue
		}
		_ = n
		_ = wIdx
		lit := ""
		if wAt < len(bad) {
			lit = bad[wAt]
		} else if ev.arg >= opsLen {
			lit = "%!w(BADINDEX)"
			if !hadIndex {
				lit = "%!w(MISSING)"
			}
		}
		wAt++
		switch {
		case lit == "":
			if fixRest && !ev.badIdx {
				pin(&sb, spec, verbEv{
					pct: ev.pct, pos: ev.pos, posP: ev.posP,
					posV: ev.posV, i: ev.i - 1, arg: ev.arg,
					stars: ev.stars, starIdx: ev.starIdx,
					valIdx: ev.valIdx, nw: ev.nw,
				}, "v")
				last = ev.i
			} else {
				sb.WriteString(spec[ev.pct : ev.i-1])
				sb.WriteByte('v')
			}
		case ev.badIdx:
			// a bad [n] makes gc print %!w(BADINDEX) and consume
			// nothing — emit the marker as text and pin the operand
			// index of every later implicit directive instead.
			sb.WriteString(strings.ReplaceAll(lit, "%", "%%"))
			fixRest = true
		default:
			// the directive's `*` operands die with it: any slot no
			// sibling consumes gets an empty string + %[S]s so gc's
			// silent consumption leaves no EXTRA and nothing reads
			// the marker text as a width.
			for _, st := range ev.stars {
				if st != ev.arg && st < opsLen && !shared[st] && !consumed[st] {
					ops[st] = ""
					sb.WriteByte('%')
					sbIdx(&sb, st+1)
					sb.WriteByte('s')
					consumed[st] = true
				}
			}
			if ev.arg < opsLen && !shared[ev.arg] && !consumed[ev.arg] {
				ops[ev.arg] = lit
				sb.WriteByte('%')
				sbIdx(&sb, ev.arg+1)
				sb.WriteByte('s')
			} else {
				// the slot stays real for the sibling that claims it:
				// the marker prints as escaped literal text, so it
				// never lands in the operand list for an out-of-range
				// [n] to read.
				sb.WriteString(strings.ReplaceAll(lit, "%", "%%"))
				fixRest = true
			}
		}
		// the next directives are pinned whenever a %w was seen: a
		// marker's text emits shift nothing, but a marshalled
		// reflect.Value star operand reads its inner int to the host
		// where gc rejects it — only the pin prints gc's marker.
		fixRest = true
		consumed[ev.arg] = true
	}
	_ = argNums
	// text between the last complete directive and a trailing
	// truncated one (%% at spec end scans as a complete % verb, so
	// only a mid-directive break leaves a partial).
	tailEnd := len(spec)
	if brokenPct >= 0 {
		tailEnd = brokenPct
	}
	sb.WriteString(spec[last:tailEnd])
	if brokenPct >= 0 {
		starMarkers(&sb, broken)
		// emitCore takes the directive's prefix ('%' through flags);
		// its drops dead `*`s and pins live ones. A trailing '.'
		// reports %!.(MISSING) like pin's MISSING tail; otherwise the
		// lone '%' opening the marker text closes the directive as a
		// % verb, leaving "!(NOVERB)" literal — verbatim would let a
		// trailing '%' fuse with a following %% diagnostic.
		emitCore(&sb, spec, broken, spec[brokenPct:broken.pos])
		if spec[broken.i-1] == '.' {
			sb.WriteString("%!.(MISSING)")
		} else {
			sb.WriteString("%!(NOVERB)")
		}
	}
	// gc prints no EXTRA once any [n] reorders the args. When every
	// explicit index lived inside a replaced bad-%w directive — or a
	// dead star's operand vanished with its '*' — the host would
	// either report EXTRA for consumed slots or print none where gc
	// would: park a fresh empty operand on %[N]s so the spec is
	// reordered and the report below is the only one.
	// gc reports the operands past its final implicit cursor as
	// %!(EXTRA ...) — but only when no [n] in the format set its
	// reordered flag, and it names each extra's Go type, not the
	// fmtValue wrapper the host would print. Emit the report here and
	// suppress the host's: park a fresh empty operand on %[N]s when no
	// index landed yet — needed anyway when every explicit index
	// lived inside a replaced bad-%w directive or a dead star's
	// operand vanished with its '*' (the host's argNum then no longer
	// matches gc's cursor).
	needExtra := !hadIndex && argNum < opsLen
	if (hadIndex || dropped || needExtra) && !wroteIndex && !verbatimIndex {
		ops = append(ops, "")
		sb.WriteString("%[")
		sb.WriteString(strconv.Itoa(len(ops)))
		sb.WriteString("]s")
	}
	if needExtra {
		// type names print through %[N]s operands: a tag like `"%s"`
		// inside `struct { A int "%s" }` must not open a directive.
		sb.WriteString("%%!(EXTRA ")
		for k := argNum; k < opsLen; k++ {
			if k > argNum {
				sb.WriteString(", ")
			}
			switch {
			case rvalIdx[k]:
				sb.WriteString("reflect.Value")
			default:
				if typ := ScriptTypeString(raws[k]); typ == "<nil>" {
					sb.WriteString("<nil>")
					continue
				} else {
					ops = append(ops, typ)
					sb.WriteByte('%')
					sbIdx(&sb, len(ops))
					sb.WriteByte('s')
				}
			}
			sb.WriteByte('=')
			sb.WriteByte('%')
			sbIdx(&sb, k+1)
			sb.WriteByte('v')
		}
		sb.WriteByte(')')
	}
	return sb.String(), ops
}

// rewriteWrapVerbs rewrites %w verbs to %v (flags, width and explicit
// [n] indexes kept), reports the operand index of every %w, in verb
// order, the way gc's doPrintf records p.wrappedErrs, and collects each
// %w directive's text. Explicit indexes move the cursor, `*` width/
// precision consume an operand, and the list may repeat an index
// (`%[1]w %[1]w`). Indexes past the operands are reported too; the
// caller drops them (gc prints %!w(MISSING) and records nothing).
func rewriteWrapVerbs(spec string, numArgs int) (string, []int, []string, [][]int, []bool) {
	if !strings.Contains(spec, "w") {
		return spec, nil, nil, nil, nil
	}
	var sb strings.Builder
	var wrapPos []int
	var wrapDirs []string
	var wrapStars [][]int
	var wrapBadIdx []bool
	argNum := 0
	dirBad := false // a `[n]` in the current directive failed (bad parse or out of range)
	// index parses an explicit `[n]` at spec[i:]. Like gc's argNumber:
	// a valid in-range index moves the cursor; an out-of-range or
	// malformed one leaves argNum alone and makes the whole directive
	// print a BADINDEX marker.
	index := func(i int) int {
		if i < len(spec) && spec[i] == '[' {
			if k := strings.IndexByte(spec[i:], ']'); k > 1 {
				if n, err := strconv.Atoi(spec[i+1 : i+k]); err == nil && n >= 1 {
					if n-1 >= numArgs {
						dirBad = true
					} else {
						argNum = n - 1
					}
					return i + k + 1
				}
				// [0] / [x]: a parsed index that is not a positive
				// operand number fails like an out-of-range one.
				dirBad = true
				return i + k + 1
			}
			dirBad = true
			return i + 1
		}
		return i
	}
	digits := func(i int) int {
		for i < len(spec) && spec[i] >= '0' && spec[i] <= '9' {
			i++
		}
		return i
	}
	for i := 0; i < len(spec); {
		j := strings.IndexByte(spec[i:], '%')
		if j < 0 {
			sb.WriteString(spec[i:])
			break
		}
		sb.WriteString(spec[i : i+j])
		i += j
		pct := i
		i++
		for i < len(spec) && strings.IndexByte("#+- 0", spec[i]) >= 0 {
			i++
		}
		stars := []int{}
		i = index(i)
		if i < len(spec) && spec[i] == '*' {
			stars = append(stars, argNum)
			if argNum < numArgs {
				argNum++
			}
			i++
		} else {
			i = digits(i)
		}
		if i < len(spec) && spec[i] == '.' {
			i = index(i + 1)
			if i < len(spec) && spec[i] == '*' {
				stars = append(stars, argNum)
				if argNum < numArgs {
					argNum++
				}
				i++
			} else {
				i = digits(i)
			}
		}
		i = index(i)
		if i >= len(spec) {
			sb.WriteString(spec[pct:])
			break
		}
		verb := spec[i]
		i++
		switch verb {
		case '%':
			sb.WriteString(spec[pct:i])
		case 'w':
			sb.WriteString(spec[pct : i-1])
			sb.WriteByte('v')
			wrapPos = append(wrapPos, argNum)
			wrapDirs = append(wrapDirs, spec[pct:i])
			wrapStars = append(wrapStars, stars)
			wrapBadIdx = append(wrapBadIdx, dirBad)
			if !dirBad {
				argNum++
			}
		default:
			sb.WriteString(spec[pct:i])
			if !dirBad {
				argNum++
			}
		}
		dirBad = false
	}
	return sb.String(), wrapPos, wrapDirs, wrapStars, wrapBadIdx
}

// ffn wraps a host fmt-style function: each arg becomes fmtArg so host
// fmt sees the script-shaped wrapper. formatAt is the index of the
// format-string arg (-1 when none); %T verbs there are rewritten to %s
// over the value's script type spelling because host fmt never calls
// Formatter.Format for %T/%p.
func ffn(name string, formatAt, minArgs int, f func([]any) (any, error), target ...any) *runtime.BuiltinFunc {
	return vffn(name, formatAt, minArgs, func(_ runtime.VMCaller, a []any) (any, error) {
		return f(a)
	}, target...)
}

// pffn is vffn without a format string whose inner fn also receives the
// flat script args — the Print family's operand spacing keys on the
// script-level string kind, which fmtArgs hides inside fmtValue.
func pffn(name string, minArgs int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	return wffn(name, -1, minArgs, f, target...)
}

// vffn is ffn whose inner fn also receives the VM caller — needed when
// an argument must call back into the script (a script-defined
// io.Writer for fmt.Fprintf, say).
func vffn(name string, formatAt, minArgs int, f func(runtime.VMCaller, []any) (any, error), target ...any) *runtime.BuiltinFunc {
	return wffn(name, formatAt, minArgs, func(v runtime.VMCaller, a []any, _ []runtime.Value) (any, error) {
		return f(v, a)
	}, target...)
}

// wffn is the shared fmt-call wrapper: the inner fn receives both the
// host-shaped args and the flat script args (before fmtArg unboxing).
func wffn(name string, formatAt, minArgs int, f func(runtime.VMCaller, []any, []runtime.Value) (any, error), target ...any) *runtime.BuiltinFunc {
	bf := &runtime.BuiltinFunc{Name: name, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < minArgs {
			return nil, fmt.Errorf("%s needs %d args, got %d", name, minArgs, len(args))
		}
		a, flat := fmtArgs(v, args)
		if formatAt >= 0 && formatAt < len(a) {
			spec, ok := a[formatAt].(string)
			if !ok && formatAt < len(flat) {
				// the spec may reach the call as a script value — a
				// named string, a cell holding one — that fmtArg boxed
				// for tag-aware rendering. The spec itself must be a
				// plain string for host fmt and for %T/%p rewriting.
				spec, ok = formatString(flat[formatAt])
			}
			if ok {
				ns, tail, _ := rewriteTypeVerbs(spec, a, flat, formatAt, v)
				a[formatAt] = ns
				a = append(a[:formatAt+1], tail...)
			}
		}
		r, err := f(v, a, flat)
		if err != nil {
			return nil, err
		}
		return stdlib.ScriptVal(r), nil
	}}
	if len(target) > 0 {
		bf.Target = target[0]
	}
	return bf
}

// fmtValue wraps one script value for host fmt: Format renders %v/%+v/%#v
// with Go's composite layout and honors script String/Error/GoString.
type fmtValue struct {
	c     runtime.MemberCaller
	x     runtime.Value
	et    *runtime.TypeDef // declared element/field typedef, for bad-verb naming
	depth int
	// nilSyntax spells a nil slice or map in %#v's T(nil) form under %v
	// too, so REPL output tells it apart from an empty one ([] / map[]).
	nilSyntax bool
}

// zeroState is a flag-less fmt.State for rendering values outside a
// live Format call — %p substitution happens before host fmt runs.
type zeroState struct{}

// markerState is the state a bad-verb marker's inner %v renders
// against: it carries the directive's own flags/width/precision (gc
// applies them inside the parens — %!p(int=         1)) while methods
// stay suppressed like every other nested state.
type markerState struct {
	zeroState
	w, p  int
	hasW  bool
	hasP  bool
	flags string
}

func (s markerState) Width() (int, bool)     { return s.w, s.hasW }
func (s markerState) Precision() (int, bool) { return s.p, s.hasP }
func (s markerState) Flag(c int) bool        { return strings.IndexByte(s.flags, byte(c)) >= 0 }

// suppressMethods reports whether f is a nested stand-in state —
// zeroState or markerState — under which gc's p.erroring declines
// every Formatter and Stringer-family call.
func suppressMethods(f fmt.State) bool {
	switch f.(type) {
	case zeroState, markerState:
		return true
	}
	return false
}

// captureState delegates formatting flags to the live state but
// records writes: an element inside a composite returns its Format
// output as the element's string for the parent's assembled output —
// writing to the live state would emit the fragment ahead of the
// surrounding brackets (gc renders elements through a sub-buffer).
type captureState struct {
	f   fmt.State
	buf bytes.Buffer
}

// nested returns the state a value inside a composite renders against —
// a capture of the live state; internal stand-ins pass through.
func nested(f fmt.State) fmt.State {
	switch f.(type) {
	case zeroState, markerState, *captureState:
		return f
	}
	return &captureState{f: f}
}

// ptrSpelling renders a value for %p — host fmt never calls Format on
// %p so rewriteTypeVerbs substitutes this string. Typed nils print 0x0
// like Go; slices, maps, chans and script pointers print an address;
// anything else produces Go's %!p(type=value) marker.
// precIntOK reports whether v is a valid star-precision operand for the
// %p rewrite — the same rule as gc's intFromArg: any integer kind that
// fits an int (a named int64 like time.Duration qualifies), and for a
// precision non-negative.
// starInt reads a *-operand value as an int like gc's intFromArg: any
// integer kind that fits an int (a named int64 like time.Duration
// qualifies), wrapper boxes and const tokens peeled first.
func starInt(v runtime.Value) (int, bool) {
	n := runtime.Unwrap(v)
	if fv, ok := n.(*fmtValue); ok {
		n = runtime.Unwrap(fv.x)
	}
	if uc, ok := n.(*runtime.UConst); ok {
		if nv, err := runtime.UConstNative(uc); err == nil {
			n = nv
		}
	}
	if gv, ok := n.(*runtime.GoValue); ok {
		n = gv.V
	}
	switch rv := reflect.ValueOf(n); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i := rv.Int()
		if int64(int(i)) == i {
			return int(i), true
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if int64(u) >= 0 && uint64(int(u)) == u {
			return int(u), true
		}
	}
	return 0, false
}

func precIntOK(v runtime.Value) bool {
	i, ok := starInt(v)
	return ok && i >= 0
}

// unnamedBasicTypes maps a basic kind to its unnamed type — a named
// host scalar converts here to shed its methods where gc suppresses
// them (bad-verb markers: time.Duration must print "3", not "3ns").
var unnamedBasicTypes = map[reflect.Kind]reflect.Type{
	reflect.Bool:       reflect.TypeOf(false),
	reflect.Int:        reflect.TypeOf(int(0)),
	reflect.Int8:       reflect.TypeOf(int8(0)),
	reflect.Int16:      reflect.TypeOf(int16(0)),
	reflect.Int32:      reflect.TypeOf(int32(0)),
	reflect.Int64:      reflect.TypeOf(int64(0)),
	reflect.Uint:       reflect.TypeOf(uint(0)),
	reflect.Uint8:      reflect.TypeOf(uint8(0)),
	reflect.Uint16:     reflect.TypeOf(uint16(0)),
	reflect.Uint32:     reflect.TypeOf(uint32(0)),
	reflect.Uint64:     reflect.TypeOf(uint64(0)),
	reflect.Uintptr:    reflect.TypeOf(uintptr(0)),
	reflect.Float32:    reflect.TypeOf(float32(0)),
	reflect.Float64:    reflect.TypeOf(float64(0)),
	reflect.Complex64:  reflect.TypeOf(complex64(0)),
	reflect.Complex128: reflect.TypeOf(complex128(0)),
	reflect.String:     reflect.TypeOf(""),
}

// markerInner builds the state a %p bad-verb marker's inner %v renders
// against from the directive's tail — flags, width and precision,
// reading * operands like gc's intFromArg. A non-integer width reports
// badWidth, a negative or non-integer precision badPrec; gc prints the
// matching %! marker ahead of the bad-verb marker and drops the value.
func markerInner(tail string, stars []int, vals []runtime.Value) (st markerState, badWidth, badPrec bool) {
	var fl strings.Builder
	starAt := 0
	dot := false
	i := 0
	for i < len(tail) {
		ch := tail[i]
		if ch == '.' {
			i++
			dot = true
			break
		}
		if ch == '[' {
			for i < len(tail) && tail[i] != ']' {
				i++
			}
			if i < len(tail) {
				i++
			}
			continue
		}
		if ch == '*' {
			i++
			if starAt < len(stars) {
				v, ok := 0, false
				if stars[starAt] < len(vals) {
					v, ok = starInt(vals[stars[starAt]])
				}
				switch {
				case !ok:
					badWidth = true
				case v < 0:
					// gc treats a negative width as minus-flag + abs.
					fl.WriteByte('-')
					st.w, st.hasW = -v, true
				default:
					st.w, st.hasW = v, true
				}
				starAt++
			}
			continue
		}
		if strings.IndexByte("#+- 0", ch) >= 0 {
			fl.WriteByte(ch)
			i++
			continue
		}
		start := i
		for i < len(tail) && tail[i] >= '0' && tail[i] <= '9' {
			i++
		}
		if start == i {
			i++
			continue
		}
		if w, err := strconv.Atoi(tail[start:i]); err == nil {
			st.w, st.hasW = w, true
		}
	}
	// the '.' is consumed: an empty precision is present-as-zero —
	// %.p truncates a string to "" like gc. Precision digits
	// accumulate; a [n] operand index may still follow.
	if dot {
		st.hasP = true
	}
	for ; i < len(tail); i++ {
		ch := tail[i]
		switch {
		case ch >= '0' && ch <= '9':
			st.p = st.p*10 + int(ch-'0')
		case ch == '*':
			if starAt < len(stars) {
				v, ok := 0, false
				if stars[starAt] < len(vals) {
					v, ok = starInt(vals[stars[starAt]])
				}
				if !ok || v < 0 {
					badPrec = true
				} else {
					st.p = v
				}
				starAt++
			}
		case ch == '[':
			for i < len(tail) && tail[i] != ']' {
				i++
			}
		}
	}
	if badPrec || !dot {
		st.hasP = false
	}
	flagsStr := fl.String()
	if strings.IndexByte(flagsStr, '-') >= 0 {
		// a left-justified field pads trailing spaces; 0 is inert.
		flagsStr = strings.ReplaceAll(flagsStr, "0", "")
	}
	st.flags = flagsStr
	return st, badWidth, badPrec
}

// starOperand lifts a *-operand arg to the integer the host fmt reads.
// The marshalled form can keep wrapper boxes (GoValue, UConst) that
// intFromArg cannot see through — a bound time.Duration reaches the
// host as its box — so the raw script value is peeled when the
// marshalled one is not already an integer kind. A non-integer operand
// passes through so the host reports BADWIDTH/BADPREC like gc.
func starOperand(x any, raw runtime.Value) any {
	intKind := func(k reflect.Kind) bool {
		switch k {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
			reflect.Uint64, reflect.Uintptr:
			return true
		}
		return false
	}
	if u := runtime.Unwrap(raw); u != nil {
		if gv, ok := u.(*runtime.GoValue); ok {
			if _, isRV := gv.V.(*minireflect.RValue); isRV {
				// gc's intFromArg rejects a reflect.Value — keep the
				// box so the host emits BADWIDTH/BADPREC instead of
				// reading the viewed int.
				return u
			}
		}
	}
	if x != nil && intKind(reflect.ValueOf(x).Kind()) {
		return x
	}
	n := runtime.Unwrap(raw)
	if uc, ok := n.(*runtime.UConst); ok {
		if nv, err := runtime.UConstNative(uc); err == nil {
			n = nv
		}
	}
	if gv, ok := n.(*runtime.GoValue); ok {
		n = gv.V
	}
	if n != nil && intKind(reflect.ValueOf(n).Kind()) {
		return n
	}
	return x
}

// ptrZeroPadWidth reports whether the flags text is a '0'-flagged
// literal width (`030`), for gc's pad-inside-0x %p rule — returns the
// width. A `*` width stays host-side instead.
func ptrZeroPadWidth(flags string) (int, bool) {
	i := 0
	zero, minus := false, false
	for i < len(flags) && strings.IndexByte("#+- 0", flags[i]) >= 0 {
		zero = zero || flags[i] == '0'
		minus = minus || flags[i] == '-'
		i++
	}
	// a left-justified field pads trailing spaces; the 0 flag is inert.
	if !zero || minus {
		return 0, false
	}
	w := 0
	for ; i < len(flags); i++ {
		if flags[i] < '0' || flags[i] > '9' {
			return 0, false
		}
		w = w*10 + int(flags[i]-'0')
	}
	// gc counts the width over the hex digits inside the 0x prefix,
	// not over the whole spelling: %015p of a 12-digit address is
	// "0x" + "000" + addr.
	return w, w > 0
}

func ptrSpelling(c runtime.MemberCaller, x runtime.Value) string {
	zs := zeroState{}
	switch t := x.(type) {
	case runtime.Nil:
		return badVerb('p', "", "<nil>")
	case *runtime.TypedNil:
		if t.Typ != nil {
			switch t.Typ.Kind {
			case runtime.KindSlice, runtime.KindMap, runtime.KindChan,
				runtime.KindFunc, runtime.KindPointer:
				return "0x0"
			}
		}
		return badVerb('p', "", "<nil>")
	case *runtime.IfaceNil:
		// a non-nil interface holding a nil pointer still prints 0x0
		return "0x0"
	case *runtime.GoValue:
		if rv, ok := t.V.(*minireflect.RValue); ok {
			// %p never unwraps the Value — Go emits the
			// %!p(reflect.Value=<underlying %v>) bad-verb form.
			return badVerb('p', "reflect.Value", fmt.Sprintf("%v", fmtRValue(c, rv)))
		}
		return fmt.Sprintf("%p", t.V)
	case *runtime.Slice:
		if stdlib.IsArrayTyp(t.Typ) {
			fv := &fmtValue{c: c, x: t}
			return badVerb('p', stdlib.TypedefSpelling(t.Typ), fv.renderList(t, 'v', zs))
		}
		if len(t.Elems) == 0 {
			return "0x0"
		}
		return fmt.Sprintf("%p", t)
	case *runtime.Map, *runtime.Chan,
		*runtime.Cell, *runtime.FieldRef, *runtime.IndexRef,
		*runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return fmt.Sprintf("%p", t)
	case *runtime.Struct:
		fv := &fmtValue{c: c, x: t}
		return badVerb('p', stdlib.TypedefSpelling(t.Def), fv.render('v', zs))
	case *runtime.Named:
		inner := ptrSpelling(c, t.V)
		// a named scalar keeps its declared type in the marker — gc
		// writes %!p(main.U=...), not the underlying kind's name. An
		// interface-declared tag never names the dynamic type.
		if t.Typ != nil && t.Typ.Kind != runtime.KindInterface && !t.Typ.IfaceBase &&
			t.Typ.Name != "" && strings.HasPrefix(inner, "%!") {
			if eq := strings.IndexByte(inner, '='); eq >= 0 {
				open := strings.IndexByte(inner, '(')
				return inner[:open+1] + stdlib.TypedefSpelling(t.Typ) + "=" + inner[eq+1:]
			}
		}
		return inner
	case *runtime.UConst:
		if nv, err := runtime.UConstNative(t); err == nil {
			return ptrSpelling(c, nv)
		}
	}
	fv := &fmtValue{c: c, x: x}
	return badVerb('p', scriptScalarName(x), fv.render('v', zs))
}

// ifaceCallStringPanic is runtime's panic-reporting Stringer probe — a
// script panic raised inside the method surfaces as the third return
// like gc's catchPanic — absent methods and ordinary call failures
// still report ok=false only. The method DOES run on a nil pointer
// arg (a nil-receiver-friendly Error still prints), but a panic from
// one renders <nil> rather than the PANIC marker — gc's catchPanic
// swaps the marker for <nil> when the arg is a nil pointer.
func ifaceCallStringPanic(c runtime.MemberCaller, x runtime.Value, name string) (string, bool, *runtime.Panic) {
	s, ok, pan := runtime.IfaceCallStringPanic(c, x, name)
	if pan != nil && fmtNilPointer(x) {
		return "", false, nil
	}
	return s, ok, pan
}

// fmtNilPointer reports whether x is a nil pointer for fmt's probe
// skip — Named and ref layers peel to the pointer-shaped TypedNil
// underneath.
func fmtNilPointer(x runtime.Value) bool {
	td, ok := stdlib.PeelNil(x)
	return ok && td != nil && td.Kind == runtime.KindPointer
}

// fmtPanicMarker spells gc's catchPanic marker for a Stringer-family
// method that panicked: %!v(PANIC=Error method: boom) — the verb as
// used (no width), the method name, and the panic payload rendered %v.
func fmtPanicMarker(verb rune, method string, p *runtime.Panic) string {
	return fmt.Sprintf("%%!%c(PANIC=%s method: %s)", verb, method, runtime.PanicPrintString(p))
}

// nilGoSyntax spells a typed nil the way Go's %#v does — T(nil), with
// parens around the anonymous pointer/chan/func spellings that need them
// and the top-level []byte special case for byte slices.
func nilGoSyntax(td *runtime.TypeDef) string {
	if td == nil {
		return "<nil>"
	}
	if td.Name == "" {
		switch td.Kind {
		case runtime.KindPointer, runtime.KindChan, runtime.KindFunc:
			return "(" + stdlib.TypedefSpelling(td) + ")(nil)"
		case runtime.KindSlice:
			if stdlib.ByteSliceTyp(td) {
				return "[]byte(nil)"
			}
		}
	}
	return stdlib.TypedefSpelling(td) + "(nil)"
}

// badVerb emits Go's %!verb(type=value) marker for an incompatible verb.
func badVerb(verb rune, typ, val string) string {
	if typ == "" {
		return fmt.Sprintf("%%!%c(%s)", verb, val)
	}
	return fmt.Sprintf("%%!%c(%s=%s)", verb, typ, val)
}

// leafBadVerb reports whether verb is incompatible with a scalar leaf —
// Go's fmt then emits %!verb(type=value) instead of the value.
func leafBadVerb(x runtime.Value, verb rune) bool {
	switch x.(type) {
	case string:
		return !strings.ContainsRune("sqxXv", verb)
	case bool:
		return !strings.ContainsRune("tv", verb)
	case float64:
		return !strings.ContainsRune("eEfFgGxXbv", verb)
	case int64:
		return !strings.ContainsRune("dobcxXUqcv", verb)
	}
	return false
}

// scriptScalarName names a scalar leaf the way Go's fmt spells its type
// inside a bad-verb marker.
func scriptScalarName(x runtime.Value) string {
	switch t := x.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case float64:
		return "float64"
	case int64:
		return "int"
	case *runtime.UConst:
		if nv, err := runtime.UConstNative(t); err == nil {
			return scriptScalarName(nv)
		}
	case *runtime.Named:
		// an interface-declared tag is never the dynamic type: the
		// payload's own tag (or raw scalar) names it — %T of `var a
		// A = N(1)` for `type A any` prints main.N like gc.
		n := t
		for n.Typ != nil && (n.Typ.IfaceBase || n.Typ.Kind == runtime.KindInterface) {
			nx, ok := n.V.(*runtime.Named)
			if !ok {
				break
			}
			n = nx
		}
		return stdlib.TypedefSpelling(n.Typ)
	case *runtime.GoValue:
		return ScriptTypeString(t)
	}
	return fmt.Sprintf("%T", x)
}

// formatDirective rebuilds a fmt directive from the verb and the
// flags/width a host call set on the state. hash drops the '#' flag for
// scalar leaves inside composites, where %#v keeps decimal ints (the
// 0x spelling is a top-level-only form).
func formatDirective(f fmt.State, verb rune, hash bool) string {
	var sb strings.Builder
	sb.WriteByte('%')
	flags := "+- 0"
	if hash {
		flags = "+-# 0"
	}
	for _, c := range flags {
		if f.Flag(int(c)) {
			sb.WriteByte(byte(c))
		}
	}
	if w, ok := f.Width(); ok {
		sb.WriteString(strconv.Itoa(w))
	}
	if p, ok := f.Precision(); ok {
		sb.WriteString("." + strconv.Itoa(p))
	}
	sb.WriteRune(verb)
	return sb.String()
}

// formatOfNoHash is formatOf without the '#' flag.
func formatOfNoHash(f fmt.State, verb rune) string {
	return formatDirective(f, verb, false)
}

// unsignedIntTyp reports whether td denotes an unsigned integer — the
// declared name or its underlying ident (a `type U uint64` decl).
// Unsigned kinds format their int64-carried bits as unsigned, and %#v
// spells them in hex like Go.
func unsignedIntTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	switch td.Name {
	case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
		return true
	}
	x := td.TypeExpr()
	if id, ok := x.(*ast.Ident); ok {
		switch id.Name {
		case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
			return true
		}
	}
	return false
}

// elemVerb picks the verb applied to elements inside a composite: every
// verb descends elementwise like Go's fmt — []int under %q quotes each
// element as a char, under %s each becomes a bad-verb marker — except
// %T which describes the composite itself, not the elements.
func elemVerb(verb rune) rune {
	if verb == 'T' {
		return 'v'
	}
	return verb
}

// sliceBytes reports the slice as bytes when its element type is
// byte/uint8 and every element is an int in byte range — the one slice
// fmt renders as text, not a bracketed list.
func sliceBytes(v *runtime.Slice) ([]byte, bool) {
	if !stdlib.ByteSliceTyp(v.Typ) {
		return nil, false
	}
	bs := make([]byte, len(v.Elems))
	for i, e := range v.Elems {
		n, ok := runtime.Unwrap(e).(int64)
		if !ok || n < 0 || n > 255 {
			return nil, false
		}
		bs[i] = byte(n)
	}
	return bs, true
}

// funcSigSpelling renders a function value's signature for %T and the
// %#v pointer wrapper — `func(int) int`, `func(...int)` — falling back
// to `func()` when the declaration isn't reachable (plain builtins).
func funcSigSpelling(v runtime.Value) string {
	if s, ok := runtime.FuncGoSpelling(v); ok {
		return s
	}
	if bf, ok := v.(*runtime.BuiltinFunc); ok && bf.Target != nil {
		// a bound host func spells its real signature — host fmt's
		// %T of a func IS the signature (func(...interface {}) (int, error))
		return fmt.Sprintf("%T", bf.Target)
	}
	return "func()"
}

// chanTypSpelling spells a channel's typedef for the %#v wrapper —
// `chan int`/`chan<- T`/`<-chan T` — `chan interface{}` when unknown.
func chanTypSpelling(td *runtime.TypeDef) string {
	if td == nil {
		return "chan interface{}"
	}
	return stdlib.TypedefSpelling(td)
}

// formatString recovers a plain string from a script value that reached
// a fmt call's format slot as a named string or a cell holding one.
func formatString(x runtime.Value) (string, bool) {
	for {
		switch v := x.(type) {
		case string:
			return v, true
		case *runtime.Named:
			x = v.V
		case *fmtValue:
			// a bound-fmt result boxed for rendering can land in a
			// named string slot — the spec is the boxed string.
			x = v.x
		case *runtime.GoValue:
			// a host value is the peel's leaf — dereferencing a *T
			// box would mint an endless chain of pointee boxes.
			return "", false
		default:
			if dv, ok := runtime.Deref(x); ok {
				x = dv
				continue
			}
			return "", false
		}
	}
}

// rewriteTypeVerbs substitutes args for the verbs host fmt handles
// without calling Formatter — %T (type spelling) and %p (address) —
// rewriting each spec verb to %s over a pre-rendered string. Positional
// indexes %[n] count arguments after the format string, as does
// implicit order. The rewrite renumbers every directive to sequential
// order and rebuilds the argument list to match, so a shared operand —
// "%v %[1]p" — keeps its original value for %v while %p sees the
// address string; mutating the operand slot in place would corrupt
// both — the rebuilt list can outgrow the original tail, so callers
// replace a[formatAt+1:] with the returned tail. Exotic mixes of %[n]
// and * in one directive, or operands past the argument list, pass
// through untouched.
// raws carries, for every slot of the returned args, the raw script
// value it came from — the slot list is rebuilt in directive order so
// an index into it is not the arg position a bare read would imply.
func rewriteTypeVerbs(spec string, a []any, rawArgs []runtime.Value, formatAt int, c runtime.MemberCaller) (string, []any, []runtime.Value) {
	vals := rawArgs[formatAt+1:]
	off := formatAt + 1
	type dir struct {
		start, end int   // the directive's span in spec
		pos        int   // resolved operand index, 0-based
		verb       byte  // final verb letter
		stars      []int // operand positions each * consumed, in order
		starIdx    bool  // mixes * and %[n] — bail out below
	}
	var dirs []dir
	argNum, maxArg := 0, 0
	reordered := false
	for i := 0; i < len(spec); {
		j := strings.IndexByte(spec[i:], '%')
		if j < 0 {
			break
		}
		i += j
		pct := i
		i++ // past '%'
		if i >= len(spec) {
			break
		}
		pos := -1
		var stars []int
		var star, indexed bool
	scan:
		for i < len(spec) {
			ch := spec[i]
			switch {
			case ch == '[':
				k := i + 1
				for k < len(spec) && spec[k] != ']' {
					k++
				}
				if n, err := strconv.Atoi(spec[i+1 : k]); err == nil {
					pos = n - 1
					// after %[n] the next implicit argument is n (Go
					// fmt: "subsequent verbs will use arguments
					// n+1, n+2", 1-based).
					argNum = n
					indexed = true
					reordered = true
				}
				i = k + 1
			case ch == '*':
				if pos < 0 {
					stars = append(stars, argNum)
					argNum++
					if argNum > maxArg {
						maxArg = argNum
					}
				}
				star = true
				i++
			case ch == '#' || ch == '+' || ch == '-' || ch == ' ' || ch == '.' || (ch >= '0' && ch <= '9'):
				i++
			default:
				break scan
			}
		}
		if i >= len(spec) {
			// a truncated directive: gc consumes its `*` operands
			// before printing %!(NOVERB) — keep it in dirs (verb=0)
			// so those operands still land in the rebuilt list. i
			// can sit past spec's end (an unterminated [n] index
			// scans off the format); clamp it for the tail slice.
			if len(stars) > 0 {
				dirs = append(dirs, dir{start: pct, end: min(i, len(spec)), pos: -1, verb: 0, stars: stars, starIdx: star && indexed})
			}
			break
		}
		verb := spec[i]
		i++
		if verb == '%' {
			// % takes no value operand — but its `*` operands still
			// read arguments, so a %*% must sit in dirs for its stars
			// to land in the rebuilt operand list (pos stays -1: the
			// verb reads nothing itself).
			if len(stars) > 0 {
				dirs = append(dirs, dir{start: pct, end: i, pos: -1, verb: '%', stars: stars, starIdx: star && indexed})
			}
			continue
		}
		if pos < 0 {
			pos = argNum
			argNum++
		}
		if pos+1 > maxArg {
			maxArg = pos + 1
		}
		dirs = append(dirs, dir{start: pct, end: i, pos: pos, verb: verb, stars: stars, starIdx: star && indexed})
	}
	// rebuild only when every operand resolves inside the arg list and
	// no directive mixes * with an index — anything else keeps the
	// original spec (and operands) so host fmt reports it as before.
	for _, d := range dirs {
		if d.starIdx || (d.verb != '%' && d.verb != 0 && (d.pos < 0 || d.pos >= len(vals) || off+d.pos >= len(a))) {
			return spec, a[off:], vals
		}
		for _, sp := range d.stars {
			if sp < 0 || sp >= len(vals) || off+sp >= len(a) {
				return spec, a[off:], vals
			}
		}
	}
	var sb strings.Builder
	var na []any
	var nraws []runtime.Value
	prev := 0
	for _, d := range dirs {
		sb.WriteString(spec[prev:d.start])
		// flags/width survive; the [n] index does not — newArgs is
		// emitted in directive order, so every operand is implicit.
		// a verb-less truncated directive's tail runs to spec's end.
		tailEnd := d.end - 1
		if d.verb == 0 {
			tailEnd = d.end
		}
		tail := spec[d.start+1 : tailEnd]
		var fb strings.Builder
		precAt, precStars := -1, 0
		for k := 0; k < len(tail); {
			if tail[k] == '[' {
				for k < len(tail) && tail[k] != ']' {
					k++
				}
				k++
				continue
			}
			if tail[k] == '.' && precAt < 0 {
				precAt = fb.Len()
			} else if precAt >= 0 && tail[k] == '*' {
				precStars++
			}
			fb.WriteByte(tail[k])
			k++
		}
		flags := fb.String()
		stars := d.stars
		badPrec := false
		badWidth := false
		innerSt := markerState{}
		var spelling string
		pBadVerb := false
		if d.verb == 'p' {
			spelling = ptrSpelling(c, vals[d.pos])
			pBadVerb = strings.HasPrefix(spelling, "%!")
			if pBadVerb {
				// a bad-verb marker renders its inner %v literally and
				// consumes every operand — none reach the rebuilt
				// directive — but width/precision operands are still
				// validated first, so a bad one reports its %! marker.
				var bw, bp bool
				innerSt, bw, bp = markerInner(tail, d.stars, vals)
				badWidth, badPrec = bw, bp
				stars = nil
			} else if precAt >= 0 {
				// for a plain pointer gc ignores precision entirely but
				// still validates a * operand — a bad one prints
				// %!(BADPREC) ahead of the value.
				bad := false
				for _, sp := range d.stars[len(d.stars)-precStars:] {
					if !precIntOK(vals[sp]) {
						bad = true
					}
				}
				badPrec = bad
				flags = flags[:precAt]
				stars = stars[:len(stars)-precStars]
			}
		}
		for _, sp := range stars {
			na = append(na, starOperand(a[off+sp], vals[sp]))
			nraws = append(nraws, vals[sp])
		}
		if badPrec && !pBadVerb {
			// gc's marker precedes the formatted directive: write it
			// as escaped text before the directive's '%' opens. On the
			// bad-verb path it is emitted with the marker below.
			sb.WriteString("%%!(BADPREC)")
		}
		switch d.verb {
		case 0:
			// truncated: emit verbatim — the flags/stars and any
			// unterminated '[' go through untouched so the host
			// prints its own %!(NOVERB) and still counts the index
			// as reordering (gc's reordered rises on '[' alone).
			sb.WriteString(spec[d.start:d.end])
		case '%':
			// % reads no value operand — its stars were appended
			// above; the directive itself emits verbatim.
			sb.WriteByte('%')
			sb.WriteString(flags)
			sb.WriteByte('%')
		case 'T':
			// %T is %s over the type spelling — every flag, width and
			// precision applies (%.1T prints "i"), and star operands
			// diagnose through the host fmt as themselves.
			sb.WriteByte('%')
			sb.WriteString(flags)
			sb.WriteByte('s')
			na = append(na, ScriptTypeString(vals[d.pos]))
			nraws = append(nraws, vals[d.pos])
		case 'p':
			if pBadVerb {
				if badWidth {
					sb.WriteString("%%!(BADWIDTH)")
				}
				if badPrec {
					sb.WriteString("%%!(BADPREC)")
				}
				// gc renders the arg inside the marker's parens:
				// %!p(int=         1) — the value is its %v with the
				// directive's own flags/width/precision applied and
				// display methods suppressed. fmtValue renders it under
				// the marker's state, so unsigned/float32 tags, +/#
				// flags and composite spellings all survive.
				inner := spelling[len("%!p(") : len(spelling)-1]
				if eq := strings.IndexByte(inner, '='); eq >= 0 {
					sb.WriteString("%%!p(")
					sb.WriteString(inner[:eq+1])
					// the rendered value is literal text in the rebuilt
					// spec — a % inside it must not reparse as a
					// directive.
					innerVal := (&fmtValue{c: c, x: vals[d.pos]}).render('v', innerSt)
					sb.WriteString(strings.ReplaceAll(innerVal, "%", "%%"))
					sb.WriteByte(')')
				} else {
					sb.WriteString("%%")
					sb.WriteString(spelling[1:])
				}
				break
			}
			sb.WriteByte('%')
			if w, ok := ptrZeroPadWidth(flags); ok && strings.HasPrefix(spelling, "0x") && len(spelling)-2 < w {
				// gc's '0' flag pads the digits inside the 0x prefix
				// to the width, which %s cannot express — pad the
				// spelled address directly.
				spelling = "0x" + strings.Repeat("0", w-(len(spelling)-2)) + spelling[2:]
				sb.WriteByte('s')
			} else {
				sb.WriteString(flags)
				sb.WriteByte('s')
			}
			na = append(na, spelling)
			nraws = append(nraws, vals[d.pos])
		default:
			sb.WriteByte('%')
			sb.WriteString(flags)
			sb.WriteByte(d.verb)
			na = append(na, a[off+d.pos])
			nraws = append(nraws, vals[d.pos])
		}
		prev = d.end
	}
	sb.WriteString(spec[prev:])
	// args past the highest consumed position print as %!(EXTRA ...) —
	// carry them across untouched. Go suppresses the EXTRA report once
	// any %[n] index reordered the arg list.
	if !reordered {
		na = append(na, a[off+maxArg:]...)
		nraws = append(nraws, vals[maxArg:]...)
	}
	return sb.String(), na, nraws
}

// formatOf rebuilds a fmt directive from the verb and the flags/width a
// host call set on the state — for scalar leaf values the host formatter
// does the real work.
func formatOf(f fmt.State, verb rune) string {
	return formatDirective(f, verb, true)
}

func withWidth(f fmt.State, s string) string {
	return fmt.Sprintf(formatOf(f, 's'), s)
}

// callFormat invokes a script fmt.Formatter — Format(fmt.State, rune) —
// handing the live printer through as a host value so the method's
// writes land on the real output (math/big's %d). An absent or
// panicking method reports false, like IfaceCallString.
// callFormatReport invokes a script Formatter like callFormat, and
// also reports a panic the call raised — fmt's catchPanic turns it into
// the %!v(PANIC=Format method: ...) marker rather than a silent miss.
func callFormatReport(c runtime.MemberCaller, x runtime.Value, f fmt.State, verb rune) (bool, *runtime.Panic) {
	m, ok := runtime.IfaceMember(c, x, "Format")
	if !ok {
		return false, nil
	}
	_, err := c.Call(m, []runtime.Value{
		&runtime.GoValue{V: f},
		&runtime.UConst{V: constant.MakeInt64(int64(verb)), Rune: true},
	})
	if err != nil {
		var p *runtime.Panic
		if errors.As(err, &p) {
			// gc's catchPanic swaps the marker for <nil> on a nil
			// pointer arg — suppressing it here lets the value fall
			// through to its own <nil> render.
			if fmtNilPointer(x) {
				return false, nil
			}
			return false, p
		}
		return false, nil
	}
	return true, nil
}

// fprintOperands renders the fmt.Print family's operands with gc's
// doPrint spacing: a space between operands only when neither is a
// string — where "string" is the reflect Kind, so a named type over
// string (json.Number) also suppresses it. The host-shaped args are
// *fmtValue wrappers (reflect sees Ptr), so the named-string check
// reads the flat script values.
func fprintOperands(w io.Writer, a []any, flat []runtime.Value) (int, error) {
	var buf bytes.Buffer
	prevStr := false
	for i, arg := range a {
		isStr := arg != nil && reflect.TypeOf(arg).Kind() == reflect.String
		if !isStr && i < len(flat) {
			isStr = isNamedString(flat[i])
		}
		if i > 0 && !isStr && !prevStr {
			buf.WriteByte(' ')
		}
		fmt.Fprint(&buf, arg) // bytes.Buffer writes never fail
		prevStr = isStr
	}
	return w.Write(buf.Bytes())
}

// isNamedString reports whether a script value is a named type whose
// underlying kind is string: Named layers unwrap to the stored scalar.
func isNamedString(x runtime.Value) bool {
	for {
		n, ok := x.(*runtime.Named)
		if !ok {
			_, isStr := x.(string)
			return isStr
		}
		x = n.V
	}
}

// fmtArgs maps script call args to host fmt args: a multi-value call
// result (Tuple) spreads into individual operands — Go treats
// `fmt.Sprint(f())` as `Sprint(v0, v1, ...)` so its space-between-
// operands rule (only when neither is a string) sees the real scalars.
// Returns the host args plus the flattened script args for spec
// rewriting (%T/%p positional lookup).
func fmtArgs(v runtime.MemberCaller, args []runtime.Value) ([]any, []runtime.Value) {
	a := make([]any, 0, len(args))
	flat := make([]runtime.Value, 0, len(args))
	for _, x := range args {
		if tup, ok := x.(*runtime.Tuple); ok {
			for _, e := range tup.Elems {
				a = append(a, fmtArg(v, e))
				flat = append(flat, e)
			}
			continue
		}
		a = append(a, fmtArg(v, x))
		flat = append(flat, x)
	}
	return a, flat
}

// fmtArg routes a script value into a host fmt call: scalars unbox to
// Go natives; composites keep their script shape inside a fmtValue.
func fmtArg(v runtime.MemberCaller, x runtime.Value) any {
	switch x := x.(type) {
	case *runtime.Named:
		// a fmtValue box inside the payload is already the argument
		// box — re-wrapping hides the inner value from %T rewriting
		// and %v alike (a Sprintf-accumulated named string slot can
		// carry one). Recurse on the boxed value so it renders and
		// types as itself.
		if fv, ok := x.V.(*fmtValue); ok {
			return fmtArg(v, fv.x)
		}
		// a host-boxed payload unwraps to fmtRValue — but only while
		// the tag is the bound typedef itself: a script-declared type
		// over a host struct inherits no methods, so gc renders it
		// field-wise and the native box must stay wrapped.
		if gv, ok := x.V.(*runtime.GoValue); ok {
			td := x.Typ
			if _, isRV := gv.V.(*minireflect.RValue); isRV || td == nil || td.HostNew != nil || td.HostIdent != nil {
				return fmtArg(v, gv)
			}
		}
		// a still-constant payload materializes at the declared width
		// first: `fmt.Println(uint64(1<<64 - 1))` is max-uint where the
		// untyped default reading overflows int inside fmtValue.
		mc, _ := v.(runtime.ConstMaterializer)
		if mv, ok := hostbridge.ConstAtWidth(mc, x); ok {
			return fmtArg(v, runtime.Tag(x.Typ, mv))
		}
		return &fmtValue{c: v, x: x}
	case int64:
		// script ints store int64 but spell int — bad-verb markers
		// (%!s(int=1)) and %T-adjacent spellings need the real width.
		return int(x)
	case float64, string, bool:
		return x
	case *runtime.UConst:
		nv := hostbridge.MustNativeConst(x)
		if i, ok := nv.(int64); ok {
			return int(i)
		}
		return nv
	case *runtime.GoValue:
		if rv, ok := x.V.(*minireflect.RValue); ok {
			return fmtRValue(v, rv)
		}
		return hostbridge.GoNative(x)
	case *runtime.TypedNil, *runtime.IfaceNil:
		// a nil named host slice (net.IP) crosses as the host nil so
		// fmt runs its String method on the nil receiver ("<nil>")
		if rt := hostbridge.HostNilType(runtime.BoxedNilTyp(x)); rt != nil {
			return reflect.Zero(rt).Interface()
		}
		if tn, ok := x.(*runtime.TypedNil); ok {
			if rt := hostbridge.HostNilType(tn.Typ); rt != nil {
				return reflect.Zero(rt).Interface()
			}
		}
		return &fmtValue{c: v, x: x}
	default:
		return &fmtValue{c: v, x: x}
	}
}

// isRValueArg reports whether a script arg was a facade reflect.Value:
// fmtArg unwraps it to the viewed payload, but Go's `*` width/precision
// read still sees the reflect.Value box and rejects it with
// %!(BADWIDTH)/%!(BADPREC) — the caller tracks the provenance so a star
// operand landing on it stays invalid.
func isRValueArg(x runtime.Value) bool {
	if _, ok := x.(*minireflect.RValue); ok {
		return true
	}
	if gv, ok := x.(*runtime.GoValue); ok {
		_, ok := gv.V.(*minireflect.RValue)
		return ok
	}
	if nv, ok := x.(*runtime.Named); ok {
		return isRValueArg(nv.V)
	}
	return false
}

// fmtRValue routes a facade reflect.Value to host fmt the way Go routes
// a reflect.Value: the argument unwraps exactly once — an invalid Value
// prints `<invalid reflect.Value>` and a payload that is itself a Value
// stays wrapped so its String still yields Go's nested `<T Value>` form
// — while every other payload formats as the viewed value under any
// verb. %T/%p are rewritten before this runs (rewriteTypeVerbs).
func fmtRValue(c runtime.MemberCaller, rv *minireflect.RValue) any {
	if !rv.IsValid() {
		return "<invalid reflect.Value>"
	}
	switch u := rv.Payload().(type) {
	case *minireflect.RValue:
		return u
	case minireflect.RValue:
		return &u
	case *runtime.GoValue:
		if inner, ok := u.V.(*minireflect.RValue); ok {
			return inner
		}
		return hostbridge.GoNative(u)
	case runtime.Value:
		return fmtArg(c, u)
	default:
		return u
	}
}

func (s *fmtValue) Format(f fmt.State, verb rune) {
	io.WriteString(f, s.render(verb, f))
}

func (zeroState) Write(b []byte) (int, error) { return len(b), nil }

func (zeroState) Width() (int, bool) { return 0, false }

func (zeroState) Precision() (int, bool) { return 0, false }

func (zeroState) Flag(int) bool { return false }

func (s *captureState) Write(b []byte) (int, error) { return s.buf.Write(b) }

func (s *captureState) Width() (int, bool) { return s.f.Width() }

func (s *captureState) Precision() (int, bool) { return s.f.Precision() }

func (s *captureState) Flag(ch int) bool { return s.f.Flag(ch) }

func (s *fmtValue) render(verb rune, f fmt.State) string {
	// fmt consults Formatter before the Stringer family, for every verb
	// but %T/%p — a script Formatter writes to the live state itself, so
	// a successful call already emitted its output. zeroState is the
	// flag-less stand-in for nested renderings (bad-verb markers, %p):
	// gc's badVerb sets p.erroring, so handleMethods declines every
	// Formatter AND every Stringer-family method there — a Stringer
	// argument renders its fields, never its String() output.
	nested := suppressMethods(f)
	// a ref operand is a *T where T is the pointee's declared type:
	// `&p` on a *bytes.Buffer is a **bytes.Buffer and `&i` on an
	// interface var is a *interface{} — unnamed pointer-to-pointer
	// and pointer-to-interface carry no methods, so the Stringer and
	// Formatter families never run on them (gc prints 0xADDR).
	opaque := refOpaque(s.x)
	if s.c != nil && !nested && !opaque && verb != 'T' && verb != 'p' {
		if cs, isCap := f.(*captureState); isCap {
			wrote, pan := callFormatReport(s.c, s.x, cs, verb)
			if pan != nil {
				return fmtPanicMarker(verb, "Format", pan)
			}
			if wrote {
				return cs.buf.String()
			}
		} else {
			wrote, pan := callFormatReport(s.c, s.x, f, verb)
			if pan != nil {
				return fmtPanicMarker(verb, "Format", pan)
			}
			if wrote {
				return ""
			}
		}
	}
	// Stringer family first, like fmt does.
	if s.c != nil && !nested && !opaque {
		switch verb {
		case 'v':
			if f.Flag('#') {
				// %#v consults GoString, not String/Error.
				if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "GoString"); pan != nil {
					return fmtPanicMarker(verb, "GoString", pan)
				} else if ok {
					return str
				}
				break
			}
			// Go's fmt consults error before Stringer — a value
			// implementing both prints its Error().
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return withWidth(f, str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return withWidth(f, str)
			}
		case 's':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return withWidth(f, str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return withWidth(f, str)
			}
		case 'q':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return strconv.Quote(str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return strconv.Quote(str)
			}
		case 'x', 'X':
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "Error"); pan != nil {
				return fmtPanicMarker(verb, "Error", pan)
			} else if ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
			if str, ok, pan := ifaceCallStringPanic(s.c, s.x, "String"); pan != nil {
				return fmtPanicMarker(verb, "String", pan)
			} else if ok {
				return fmt.Sprintf("%"+string(verb), str)
			}
		}
	}
	return s.renderValue(s.x, verb, f)
}

func (s *fmtValue) renderValue(x runtime.Value, verb rune, f fmt.State) string {
	// Go's fmt has no render-depth cap — a deeply nested composite
	// prints every level (issue29264), and a cyclic one overflows the
	// host stack just as it does under go run.
	switch v := x.(type) {
	case *runtime.Named:
		// %T keeps the declared name; other verbs render through.
		if verb == 'T' {
			if runtime.IfaceTaggedNil(v.V) != nil {
				return "<nil>" // a nil interface has no dynamic type
			}
			return stdlib.TypedefSpelling(v.Typ)
		}
		// a bound host scalar (time.Month/time.Duration) whose payload is
		// still script-shaped — a stored int64 or a const-domain UConst —
		// renders through the host scalar so String() and every verb
		// apply like gc (%v → "January", %d → 1). Nested bad-verb
		// renderings skip it: gc's p.erroring suppresses String().
		if td := v.Typ; td != nil && td.HostScalar != nil && !suppressMethods(f) {
			if hv, ok := runtime.HostScalarOf(td, v.V); ok {
				return fmt.Sprintf(formatOf(f, verb), hv)
			}
		}
		// an int64 carrying a uint64/uintptr tag must format its bits as
		// unsigned — host fmt would read the int64 as signed otherwise.
		if unsignedIntTyp(v.Typ) {
			if iv, ok := v.V.(int64); ok {
				return fmt.Sprintf(formatOf(f, verb), uint64(iv))
			}
		}
		// a float64 carrying a float32 tag formats in float32 — the
		// rounded value, not the wider box's digits. A payload still
		// riding the constant domain reads at float32 width the same
		// way (`float32(c)` conversions keep UConst).
		if hostbridge.Float32Tag(v.Typ) {
			if fv, ok := v.V.(float64); ok {
				return fmt.Sprintf(formatOf(f, verb), float32(fv))
			}
			if uc, ok := v.V.(*runtime.UConst); ok {
				if nv, err := runtime.UConstNative(uc); err == nil {
					if fv, ok := nv.(float64); ok {
						return fmt.Sprintf(formatOf(f, verb), float32(fv))
					}
				}
			}
		}
		// a complex128 carrying a complex64 tag rounds each half to
		// float32 — same gap as float32, one level wider.
		if hostbridge.Complex64Tag(v.Typ) {
			if cv, ok := hostbridge.Complex64Value(v.V); ok {
				return fmt.Sprintf(formatOf(f, verb), cv)
			}
		}
		// a script-declared type over a host box inherits no host
		// methods: gc renders it field-wise, so the native payload
		// must not reach Sprintf where its String() would run
		// (`type MyBuf bytes.Buffer` prints {[104 105] 0 0}, not "hi").
		// A declared-over-INTERFACE tag is the opposite — transparent
		// to the payload (`var a A = buf` with `type A any` calls
		// buf.String()), so it renders through the payload below.
		if td := v.Typ; td != nil && declaredOverHost(td) {
			if gv, ok := runtime.Unwrap(v.V).(*runtime.GoValue); ok {
				if rv := reflect.ValueOf(hostbridge.GoNative(gv)); rv.IsValid() {
					// the box stores a pointer to the host struct —
					// incidental unless the declared type itself is
					// a pointer type (`type P *bytes.Buffer`).
					if rv.Kind() == reflect.Pointer && td.Kind != runtime.KindPointer && rv.Elem().Kind() == reflect.Struct {
						// a Named-over-host-struct stores *T where
						// gc holds T — a pointer pointee keeps its &.
						rv = rv.Elem()
					}
					return hostFields(rv, verb, f, stdlib.TypedefSpelling(td))
				}
			}
		}
		return (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: v.V, et: v.Typ, depth: s.depth + 1}).render(verb, f)
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		dv, ok := runtime.Deref(v)
		if !ok {
			return "<nil>"
		}
		// Go's printPtr descends one level only when the pointee is a
		// composite — struct, array, slice, or map — spelling `&[...]`/
		// `&{...}`/`&map[...]`; every other pointee (scalars, other
		// pointers, chans, funcs) reads as the address. The descent only
		// happens at the top level: a pointer nested inside a composite
		// prints 0x... — []*S{p} renders [0xADDR], and under %#v
		// [(*S)(0xADDR)].
		composite := func(x runtime.Value) bool {
			switch t := x.(type) {
			case *runtime.Struct, *runtime.Slice, *runtime.Map:
				return true
			case *runtime.GoValue:
				// a host box counts only when it holds a composite
				// directly — a *T box is a pointer (`&p` on a
				// *bytes.Buffer prints 0xADDR, not &{...}). The
				// incidental *T a Named-over-host stores is handled
				// through the Named arm's declared type below.
				rv := reflect.ValueOf(hostbridge.GoNative(t))
				if !rv.IsValid() {
					return false
				}
				switch rv.Kind() {
				case reflect.Struct, reflect.Array, reflect.Slice, reflect.Map:
					return true
				}
				return false
			case *runtime.TypedNil:
				// &ns for a nil slice/map spells &[]/&map[]
				return t.Typ != nil &&
					(t.Typ.Kind == runtime.KindSlice || t.Typ.Kind == runtime.KindMap)
			}
			return false
		}
		if s.depth == 0 && composite(dv) {
			return "&" + (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: dv, depth: s.depth + 1}).render(verb, nested(f))
		}
		if s.depth == 0 {
			if n, isNamed := dv.(*runtime.Named); isNamed && (composite(n.V) || declaredHostComposite(n)) {
				px := n.V
				if td := n.Typ; td != nil && td.File != nil {
					// a declared-over-host value keeps its tag through
					// the descent so hostFields renders the fields.
					px = dv
				}
				return "&" + (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: px, depth: s.depth + 1}).render(verb, nested(f))
			}
		}
		// scalar pointer: Go prints the address — a host pointer repr
		// is the closest readable stand-in; %#v wraps it as (*T)(0xADDR).
		if verb == 'v' && f.Flag('#') {
			return fmt.Sprintf("(*%s)(%p)", ScriptTypeString(dv), v)
		}
		if verb == 'x' || verb == 'X' {
			// %x of a pointer prints the bare hex address; # restores
			// the 0x/0X prefix (%p's shape) and width/precision pad
			// the spelled address.
			sp := strings.TrimPrefix(fmt.Sprintf("%p", v), "0x")
			if f.Flag('#') {
				if verb == 'X' {
					sp = "0X" + strings.ToUpper(sp)
				} else {
					sp = "0x" + sp
				}
			} else if verb == 'X' {
				sp = strings.ToUpper(sp)
			}
			return fmt.Sprintf(formatOfNoHash(f, 's'), sp)
		}
		return fmt.Sprintf("%p", v)
	case *runtime.Struct:
		switch verb {
		case 'T':
			return stdlib.TypedefSpelling(v.Def)
		case 'q':
			return strconv.Quote(s.renderValue(x, 'v', f))
		}
		// +/# field naming and the T{...} go-syntax are live-%v forms;
		// inside a bad-verb marker gc passes the flags down to the leaf
		// renders instead (%+p of S{X:7} prints {+7}). The x-family
		// verbs keep the flags' numeric meaning at the leaf (%#x of a
		// pointer field prints 0xADDR, no field names, no T{} wrapper).
		nameFields := !suppressMethods(f)
		vFlags := nameFields && verb == 'v'
		parts := make([]string, len(v.Fields))
		for i, e := range v.Fields {
			fv := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: stdlib.FieldTypOf(v.Def, i), depth: s.depth + 1}).render(elemVerb(verb), nested(f))
			if vFlags && (f.Flag('+') || f.Flag('#')) && i < len(v.Def.Fields) {
				fv = v.Def.Fields[i] + ":" + fv
			}
			parts[i] = fv
		}
		sep := " "
		if vFlags && f.Flag('#') {
			sep = ", "
		}
		body := strings.Join(parts, sep)
		if vFlags && f.Flag('#') {
			return stdlib.TypedefSpelling(v.Def) + "{" + body + "}"
		}
		return "{" + body + "}"
	case *runtime.Slice:
		switch verb {
		case 'T':
			return stdlib.TypedefSpelling(v.Typ)
		case 'p':
			if stdlib.IsArrayTyp(v.Typ) {
				return badVerb(verb, stdlib.TypedefSpelling(v.Typ), s.renderList(v, 'v', f))
			}
			return fmt.Sprintf("%p", v)
		case 's', 'q', 'x', 'X':
			// a byte-wise slice formats as text like Go's fmt does
			if bs, ok := sliceBytes(v); ok {
				switch verb {
				case 's':
					return string(bs)
				case 'q':
					return strconv.Quote(string(bs))
				default:
					return fmt.Sprintf(formatOf(f, verb), bs)
				}
			}
		case 'v':
			// %#v renders a byte slice as []byte{0xNN, ...} — the byte
			// spelling applies only at the top level (reflect cannot
			// distinguish byte from uint8 below it).
			if f.Flag('#') && !suppressMethods(f) && stdlib.ByteSliceTyp(v.Typ) {
				if bs, ok := sliceBytes(v); ok {
					spell := stdlib.TypedefSpelling(v.Typ)
					if v.Typ.Name == "" && s.depth == 0 && !stdlib.IsArrayTyp(v.Typ) {
						spell = "[]byte"
					}
					parts := make([]string, len(bs))
					for i, b := range bs {
						parts[i] = fmt.Sprintf("0x%x", b)
					}
					return spell + "{" + strings.Join(parts, ", ") + "}"
				}
			}
		}
		return s.renderList(v, verb, f)
	case *runtime.Map:
		switch verb {
		case 'T':
			return stdlib.TypedefSpelling(v.Typ)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		// Go's fmt prints maps in sorted-key order (fmtsort), not
		// insertion order — sort a copy of Order the same way.
		order, canon := v.SnapshotKeys()
		// keyCmp is a 3-way compare like fmtsort's — returning the
		// whole ordering keeps equal prefixes from doubling the
		// recursion a bool less(...) would need at every level.
		var keyCmp func(a, b runtime.Value) int
		keyCmp = func(a, b runtime.Value) int {
			// Mixed-type keys (map[any]) compare by the concrete
			// type first, like fmtsort's interface rule.
			ra, ta := mapKeyRank(a)
			rb, tb := mapKeyRank(b)
			if ra != rb {
				return cmp.Compare(ra, rb)
			}
			if ta != tb {
				return strings.Compare(ta, tb)
			}
			if stdlib.LessScript(a, b) {
				return -1
			}
			if stdlib.LessScript(b, a) {
				return 1
			}
			// composite keys compare element by element on the
			// underlying values like gc's fmtsort — a key's own
			// rendered text never decides ([2]B whose String maps
			// true to "a" still orders false before true).
			ae, be, ok := fmtsortKeyElems(a, b)
			if ok {
				n := min(len(ae), len(be))
				for i := 0; i < n; i++ {
					if c := keyCmp(ae[i], be[i]); c != 0 {
						return c
					}
				}
				return cmp.Compare(len(ae), len(be))
			}
			// unordered kinds (mixed, unrenderable): order by the
			// rendered key like fmtsort's fallback.
			ka := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: a, depth: s.depth + 1}).render('v', nested(f))
			kb := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: b, depth: s.depth + 1}).render('v', nested(f))
			return strings.Compare(ka, kb)
		}
		// sort an index permutation: the canonical key stays paired
		// with its display key, so a NaN key's value still resolves —
		// Map.Get on a NaN script key never finds it.
		sortedIdx := make([]int, len(order))
		for i := range sortedIdx {
			sortedIdx[i] = i
		}
		sort.SliceStable(sortedIdx, func(i, j int) bool {
			return keyCmp(order[sortedIdx[i]], order[sortedIdx[j]]) < 0
		})
		kt, vt := stdlib.MapElemTyps(v.Typ)
		ev := elemVerb(verb)
		parts := make([]string, 0, len(order))
		for _, i := range sortedIdx {
			k := order[i]
			e, _ := v.LookupCanonical(canon[i])
			kr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: k, et: kt, depth: s.depth + 1}).render(ev, nested(f))
			vr := (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: vt, depth: s.depth + 1}).render(ev, nested(f))
			parts = append(parts, kr+":"+vr)
		}
		if f.Flag('#') && !suppressMethods(f) {
			return stdlib.TypedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
		}
		return "map[" + strings.Join(parts, " ") + "]"
	case *runtime.Tuple:
		parts := make([]string, len(v.Elems))
		for i, e := range v.Elems {
			parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, depth: s.depth + 1}).render(verb, nested(f))
		}
		return strings.Join(parts, " ")
	case *runtime.TypedNil:
		if verb == 'T' {
			return stdlib.TypedefSpelling(v.Typ)
		}
		if rt := hostbridge.HostNilType(v.Typ); rt != nil {
			// a nil named host slice (net.IP) renders as the host nil
			// would — its String method on the nil receiver
			return fmt.Sprintf(formatOf(f, verb), reflect.Zero(rt).Interface())
		}
		return s.nilTyp(verb, f, v.Typ)
	case *runtime.IfaceNil:
		if v.Typ != nil && v.Typ.Kind == runtime.KindInterface {
			// a nil interface value prints like a bare nil — <nil> at
			// top level, the slot's static type inside %#v composites.
			if verb == 'v' && f.Flag('#') && s.et != nil {
				return nilGoSyntax(s.et)
			}
			if verb == 'T' || verb == 'v' || s.depth > 0 || s.et != nil {
				return "<nil>"
			}
			return badVerb(verb, "", "<nil>")
		}
		if verb == 'T' {
			return stdlib.TypedefSpelling(v.Typ)
		}
		if rt := hostbridge.HostNilType(v.Typ); rt != nil {
			return fmt.Sprintf(formatOf(f, verb), reflect.Zero(rt).Interface())
		}
		return s.nilTyp(verb, f, v.Typ)
	case runtime.Nil:
		// %#v spells a nil sitting in an interface-typed field or
		// element with the slot's static type — error(nil),
		// interface {}(nil) — the way reflect sees the declared type.
		// Every other verb (and an untyped top-level nil) keeps <nil>.
		if verb == 'v' && f.Flag('#') && s.et != nil {
			return nilGoSyntax(s.et)
		}
		// a nil interface element inside a composite renders <nil>
		// under every verb; top-level non-%v verbs get the marker.
		if verb == 'T' || verb == 'v' || s.depth > 0 || s.et != nil {
			return "<nil>"
		}
		return badVerb(verb, "", "<nil>")
	case *runtime.Chan:
		switch verb {
		case 'T':
			return stdlib.TypedefSpelling(v.Typ)
		case 'v':
			// %#v spells pointer-shaped values as (T)(0xADDR).
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", chanTypSpelling(v.Typ), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, stdlib.TypedefSpelling(v.Typ), fmt.Sprintf("%p", v))
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		switch verb {
		case 'T':
			return funcSigSpelling(v)
		case 'v':
			if f.Flag('#') {
				return fmt.Sprintf("(%s)(%p)", funcSigSpelling(v), v)
			}
			return fmt.Sprintf("%p", v)
		case 'p':
			return fmt.Sprintf("%p", v)
		}
		return badVerb(verb, funcSigSpelling(v), fmt.Sprintf("%p", v))
	case *runtime.GoValue:
		if verb == 'T' {
			// a proxy box spells its API type, not the box's name.
			if rt := stdtime.ProxyHostTypeOf(v.V); rt != nil {
				return rt.String()
			}
		}
		hv := v.V
		if suppressMethods(f) {
			// gc's p.erroring suppresses display methods — a named
			// host scalar renders at its unnamed basic kind
			// (time.Duration → 3, not 3ns).
			if rv := reflect.ValueOf(hv); rv.IsValid() && rv.Type().NumMethod() > 0 {
				if bt := unnamedBasicTypes[rv.Kind()]; bt != nil {
					hv = rv.Convert(bt).Interface()
				}
			}
		}
		return fmt.Sprintf(formatOf(f, verb), hv)
	case *runtime.TypeDef:
		if verb == 'T' {
			return "type"
		}
		return stdlib.TypedefSpelling(v)
	}
	return s.leaf(x, verb, f)
}

// refOpaque reports whether a ref operand (&x) is a pointer whose
// own method set is empty: the pointee's declared type is an unnamed
// pointer (`&p` is **bytes.Buffer), an interface (`&i` is
// *interface{}), or unstamped while the stored value is a raw host
// pointer box (an inferred `p := bytes.NewBufferString` slot — its
// *T payload makes &p a **T). A Named payload is left for its own
// declared-method walk, and a NAMED pointer pointee (`type P *T`)
// keeps its declared methods.
func refOpaque(x runtime.Value) bool {
	switch x.(type) {
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
	default:
		return false
	}
	if td := stdlib.PointeeDeclTyp(x); td != nil {
		return td.Kind == runtime.KindInterface || td.IfaceBase ||
			(td.Kind == runtime.KindPointer && td.Spec == nil)
	}
	dv, ok := runtime.Deref(x)
	if !ok {
		return false
	}
	if _, named := dv.(*runtime.Named); named {
		return false
	}
	if gv, ok := dv.(*runtime.GoValue); ok {
		if rv := reflect.ValueOf(hostbridge.GoNative(gv)); rv.IsValid() && rv.Kind() == reflect.Pointer {
			return true
		}
	}
	return false
}

// declaredOverHost reports whether td is a script-declared typedef
// whose value sits on a host box: a declaration in source (File set),
// or the anonymous *T over one (`&b` on `type MyBuf bytes.Buffer`).
// Declared-over-interface tags (`type A any`, `type E error`) are not
// host wraps — the interface is transparent to its payload's type.
func declaredOverHost(td *runtime.TypeDef) bool {
	if td == nil || td.Kind == runtime.KindInterface || td.IfaceBase {
		return false
	}
	if td.File != nil {
		return true
	}
	return td.Kind == runtime.KindPointer && td.Spec == nil &&
		td.Elem != nil && td.Elem.File != nil &&
		td.Elem.Kind != runtime.KindInterface && !td.Elem.IfaceBase
}

// declaredHostComposite reports whether a Named-over-host value is a
// composite for the `&x` descent: the declared type's underlying host
// kind decides (`type MyBuf bytes.Buffer` is a struct) — a declared
// pointer type (`type P *T`) and an interface tag are not composite.
func declaredHostComposite(n *runtime.Named) bool {
	td := n.Typ
	if td == nil || td.Kind == runtime.KindPointer ||
		td.Kind == runtime.KindInterface || td.IfaceBase || td.File == nil {
		return false
	}
	if gv, ok := runtime.Unwrap(n.V).(*runtime.GoValue); ok {
		rv := reflect.ValueOf(hostbridge.GoNative(gv))
		for rv.IsValid() && rv.Kind() == reflect.Pointer {
			rv = rv.Elem()
		}
		switch rv.Kind() {
		case reflect.Struct, reflect.Array, reflect.Slice, reflect.Map:
			return true
		}
	}
	return false
}

// hostFields renders a host value the way gc's fmt renders a value
// whose declared type has no methods: a field-wise walk where no
// String()/Error() runs. Unexported fields read through the kind
// accessors (Interface() would panic on them), so a nested Stringer
// under %v renders its own fields — one level deeper than gc goes.
func hostFields(rv reflect.Value, verb rune, f fmt.State, typ string) string {
	return hostFieldsAt(rv, verb, f, typ, true)
}

// hostFieldsAt is hostFields's recursive walk: top marks the entry
// value, where gc descends a pointer's composite pointee once
// (`&{...}`). A nested pointer prints its address instead — which is
// also what bounds the walk, so cyclic host shapes (a template's
// shared parse tables) cannot exhaust the stack.
func hostFieldsAt(rv reflect.Value, verb rune, f fmt.State, typ string, top bool) string {
	for rv.IsValid() && rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return "<nil>"
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return "<nil>"
	}
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return "<nil>"
		}
		if top {
			switch rv.Elem().Kind() {
			case reflect.Struct, reflect.Array, reflect.Slice, reflect.Map:
				return "&" + hostFieldsAt(rv.Elem(), verb, f, "", false)
			}
		}
		if f.Flag('#') && verb == 'v' {
			// %#v spells a pointer the Go-syntax way: (*T)(0xADDR).
			return fmt.Sprintf("(*%s)(0x%x)", rv.Type().Elem(), rv.Pointer())
		}
		// %x prints the bare hex address; every other verb keeps %p's
		// 0xADDR shape (%#x's 0x prefix lands the same).
		if verb == 'x' || verb == 'X' {
			// fmt's own # flag on %x spells the 0x/0X prefix and
			// width/precision apply to the hex like %p's.
			return fmt.Sprintf(formatOf(f, verb), rv.Pointer())
		}
		return fmt.Sprintf("0x%x", rv.Pointer())
	case reflect.Struct:
		nameFields := !suppressMethods(f)
		// field names and the T{...} wrapper are %v family spellings:
		// %+v/%#v name fields and %#v wraps in the type's Go name —
		// under %x-family verbs the flags keep their numeric meaning
		// (%+x signs the hex, %#x prefixes 0x).
		nameThem := nameFields && (f.Flag('+') || f.Flag('#')) && verb == 'v'
		parts := make([]string, rv.NumField())
		for i := range parts {
			fv := hostFieldsAt(rv.Field(i), verb, f, "", false)
			if nameThem {
				fv = rv.Type().Field(i).Name + ":" + fv
			}
			parts[i] = fv
		}
		if f.Flag('#') && verb == 'v' && nameFields {
			if typ == "" {
				typ = rv.Type().String()
			}
			return typ + "{" + strings.Join(parts, ", ") + "}"
		}
		return "{" + strings.Join(parts, " ") + "}"
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return "[]"
		}
		if v, ok := hostIface(rv); ok && !f.Flag('#') {
			// byte slices print through host fmt like gc — "hi"
			// under %s, "hi" quoted under %q, hex under %x. Under
			// %#v the manual path spells reflect's type name
			// ([]uint8 where host fmt writes []byte).
			return fmt.Sprintf(formatOf(f, verb), v)
		}
		parts := make([]string, rv.Len())
		for i := range parts {
			parts[i] = hostFieldsAt(rv.Index(i), verb, f, "", false)
		}
		if f.Flag('#') && !suppressMethods(f) {
			return rv.Type().String() + "{" + strings.Join(parts, ", ") + "}"
		}
		return "[" + strings.Join(parts, " ") + "]"
	case reflect.Map:
		if rv.IsNil() {
			return "map[]"
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return hostFields(keys[i], 'v', f, "") < hostFields(keys[j], 'v', f, "")
		})
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = hostFieldsAt(k, verb, f, "", false) + ":" + hostFieldsAt(rv.MapIndex(k), verb, f, "", false)
		}
		if f.Flag('#') && !suppressMethods(f) {
			if typ == "" {
				typ = rv.Type().String()
			}
			return typ + "{" + strings.Join(parts, ", ") + "}"
		}
		return "map[" + strings.Join(parts, " ") + "]"
	}
	if v, ok := hostIface(rv); ok {
		return fmt.Sprintf(formatOf(f, verb), v)
	}
	// chans, funcs, unsafe pointers — an address repr is the closest
	// readable stand-in.
	return fmt.Sprintf("0x%x", rv.Pointer())
}

// hostIface materializes a possibly-unexported host field into a plain
// value host fmt can print — rv.Interface() panics on unexported fields,
// but the per-kind Set* copies need only the readable accessor.
func hostIface(rv reflect.Value) (any, bool) {
	nv := reflect.New(rv.Type()).Elem()
	switch rv.Kind() {
	case reflect.Bool:
		nv.SetBool(rv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		nv.SetInt(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		nv.SetUint(rv.Uint())
	case reflect.Float32, reflect.Float64:
		nv.SetFloat(rv.Float())
	case reflect.Complex64, reflect.Complex128:
		nv.SetComplex(rv.Complex())
	case reflect.String:
		nv.SetString(rv.String())
	case reflect.Slice:
		if rv.Type().Elem().Kind() != reflect.Uint8 {
			return nil, false
		}
		nv.SetBytes(rv.Bytes())
	default:
		return nil, false
	}
	return nv.Interface(), true
}

// renderList renders a slice's bracketed element list — every verb
// descends elementwise like Go's fmt ([]int under %q quotes each element
// as a char, under %s each becomes a %!s marker).
func (s *fmtValue) renderList(v *runtime.Slice, verb rune, f fmt.State) string {
	et := stdlib.SliceElemTyp(v.Typ)
	ev := elemVerb(verb)
	parts := make([]string, len(v.Elems))
	for i, e := range v.Elems {
		parts[i] = (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: e, et: et, depth: s.depth + 1}).render(ev, nested(f))
	}
	if f.Flag('#') && verb == 'v' && !suppressMethods(f) {
		return stdlib.TypedefSpelling(v.Typ) + "{" + strings.Join(parts, ", ") + "}"
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// leaf formats a scalar value: compatible verbs go to host fmt after
// normalizing to the script-canonical Go type (script ints are int64 but
// spell int), incompatible verbs produce Go's %!verb(type=value) marker
// spelled with the declared element type when the leaf has one.
func (s *fmtValue) leaf(x runtime.Value, verb rune, f fmt.State) string {
	if u, ok := x.(*runtime.UConst); ok {
		x = hostbridge.MustNativeConst(u)
	}
	if suppressMethods(f) {
		// gc's p.erroring suppresses display methods — a bare named
		// host scalar (time.Duration) renders at its unnamed kind.
		if rv := reflect.ValueOf(x); rv.IsValid() && rv.Type().NumMethod() > 0 {
			if bt := unnamedBasicTypes[rv.Kind()]; bt != nil {
				x = rv.Convert(bt).Interface()
			}
		}
	}
	if leafBadVerb(x, verb) {
		name := ""
		// an interface-typed element (incl. any) names the value's
		// dynamic type, not the interface — []any{1} gives %!v(int=1)
		if s.et != nil && s.et.Name != "any" {
			if _, isIface := s.et.TypeExpr().(*ast.InterfaceType); !isIface {
				name = stdlib.TypedefSpelling(s.et)
			}
		}
		if name == "" {
			name = scriptScalarName(x)
		}
		return badVerb(verb, name, (&fmtValue{c: s.c, nilSyntax: s.nilSyntax, x: x, depth: s.depth + 1}).render('v', nested(f)))
	}
	if i, ok := x.(int64); ok {
		// script ints store int64 but spell int — including inside
		// composites, where %#v elements stay decimal (the 0x-hex int
		// spelling is a top-level-only form).
		if suppressMethods(f) {
			// inside a bad-verb marker the flags fall to the integer
			// itself — %+ signs and %0 pads apply like fmtInteger,
			// %# stays inert (p.fmt.sharp, not sharpV).
			return fmt.Sprintf(formatOfNoHash(f, 'd'), int(i))
		}
		if verb == 'v' && f.Flag('#') {
			return fmt.Sprintf(formatOfNoHash(f, verb), int(i))
		}
		return fmt.Sprintf(formatOf(f, verb), int(i))
	}
	if suppressMethods(f) {
		// same inert-# rule for the other scalar leaves.
		return fmt.Sprintf(formatOfNoHash(f, verb), x)
	}
	return fmt.Sprintf(formatOf(f, verb), x)
}

// nilTyp renders a typed nil like Go's fmt: a nil slice or map prints its
// empty composite ([] or map[]) under every verb — a nil []byte renders
// like the empty string — while a nil pointer, chan or func prints
// <nil>/0x0/0 like the nil address it is. %#v uses the Go-syntax
// conversion form T(nil): []string(nil), map[string]int(nil), (*int)(nil).
func (s *fmtValue) nilTyp(verb rune, f fmt.State, td *runtime.TypeDef) string {
	if verb == 'v' && f.Flag('#') {
		return nilGoSyntax(td)
	}
	if verb == 'v' && s.nilSyntax && td != nil &&
		(td.Kind == runtime.KindSlice || td.Kind == runtime.KindMap) {
		return nilGoSyntax(td)
	}
	if td == nil {
		return "<nil>"
	}
	switch td.Kind {
	case runtime.KindSlice:
		if stdlib.ByteSliceTyp(td) {
			switch verb {
			case 's', 'x', 'X':
				return ""
			case 'q':
				return strconv.Quote("")
			}
		}
		if verb == 'p' {
			return "0x0"
		}
		return "[]"
	case runtime.KindMap:
		if verb == 'p' {
			return "0x0"
		}
		return "map[]"
	case runtime.KindPointer, runtime.KindChan, runtime.KindFunc:
		switch verb {
		case 'v':
			return "<nil>"
		case 'p':
			return "0x0"
		case 'd', 'o', 'b', 'x', 'X', 'U', 'c':
			// a nil address formats as 0 under the numeric verbs
			return fmt.Sprintf(formatOf(f, verb), 0)
		}
		return badVerb(verb, stdlib.TypedefSpelling(td), "<nil>")
	}
	return "<nil>"
}

// scanFn adapts fmt's Scan family: the out-params arrive as script refs,
// which host fmt cannot store through, so each is mirrored into a host
// variable of the pointee's type, the real fmt call scans into those, and
// each result is written back through the ref. heads counts the leading
// non-pointer args (a format string or reader); headFn converts any
// prefix of them it needs to (nil: none) and scanFn goNatives the rest,
// so a converter like scanReader never has to know the arg count.
func scanFn(name string, heads int, headFn func(runtime.MemberCaller, []runtime.Value) ([]any, error), target any, call func(head, ptrs []any) (int, error)) *runtime.BuiltinFunc {
	return &runtime.BuiltinFunc{Name: name, Target: target, Fn: func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) < heads {
			return nil, fmt.Errorf("%s needs at least %d args, got %d", name, heads, len(args))
		}
		var head []any
		if headFn != nil {
			var err error
			if head, err = headFn(v, args[:heads]); err != nil {
				return nil, err
			}
		}
		for i := len(head); i < heads; i++ {
			head = append(head, hostbridge.GoNative(args[i]))
		}
		ptrs := make([]any, len(args)-heads)
		tds := make([]*runtime.TypeDef, len(args)-heads)
		for i, ref := range args[heads:] {
			cur, ok := runtime.Deref(ref)
			if !ok {
				return stdlib.RetErr(0, fmt.Errorf("%s: argument %d is not a pointer", name, i+heads+1))
			}
			// the host mirror erases the pointee's declared tag —
			// `var m MyInt; Sscan("5", &m)` — so capture it for the
			// write-back below.
			tds[i] = runtime.TagOf(cur)
			t := reflect.TypeOf(hostbridge.GoNative(cur))
			if t == nil {
				return stdlib.RetErr(0, fmt.Errorf("%s: cannot infer scan target type: argument %d", name, i+heads+1))
			}
			rv := reflect.New(t)
			// Seed with the current pointee: gc leaves args the scan
			// never reached (or failed to store) untouched.
			rv.Elem().Set(reflect.ValueOf(hostbridge.GoNative(cur)))
			ptrs[i] = rv.Interface()
		}
		n, err := call(head, ptrs)
		// gc stores whatever it managed to scan even on error, so the
		// write-back runs unconditionally.
		for i, ref := range args[heads:] {
			out := stdlib.ScriptVal(reflect.ValueOf(ptrs[i]).Elem().Interface())
			if tds[i] != nil {
				out = runtime.Tag(tds[i], out)
			}
			runtime.SetRef(ref, out)
		}
		return stdlib.RetErr(n, err)
	}}
}

// scanReader converts an Fscan family's leading reader arg, accepting
// both host io.Reader values and script-defined readers.
func scanReader(ad *stdio.Adapters, v runtime.MemberCaller, args []runtime.Value) ([]any, error) {
	r, err := ad.AsReaderVM(v, args[0])
	if err != nil {
		return nil, err
	}
	return []any{r}, nil
}

// scriptTypeString spells a value's type the way Go's %T does —
// "[]int", "main.Point" — using the typedef, not the Go wrapper type.
func ScriptTypeString(x runtime.Value) string {
	switch t := x.(type) {
	case nil, runtime.Nil:
		// a nil interface has no dynamic type — %T spells <nil>.
		return "<nil>"
	case *runtime.UConst:
		return t.DefaultName()
	case *runtime.Named:
		if stdlib.NilIfaceTyp(t) != nil {
			return "<nil>" // a nil interface has no dynamic type
		}
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.Struct:
		return stdlib.TypedefSpelling(t.Def)
	case *runtime.Slice:
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.Map:
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.Chan:
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.TypedNil:
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.IfaceNil:
		if stdlib.NilIfaceTyp(t) != nil {
			return "<nil>" // a nil interface has no dynamic type
		}
		return stdlib.TypedefSpelling(t.Typ)
	case *runtime.PanicNilError:
		return "*runtime.PanicNilError"
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef:
		if dv, ok := runtime.Deref(t); ok {
			// the pointee slot's stamped declared type names the
			// pointee's static type — `&i` on an interface-typed var
			// spells *main.I, not *main.S of its stored value. A ref
			// operand reads it through the ref itself: a FieldRef's
			// declared field type, an IndexRef's element type.
			if td := stdlib.PointeeDeclTyp(t); td != nil {
				return "*" + stdlib.TypedefSpelling(td)
			}
			if c := stdlib.AsCell(dv); c != nil && c.Typ != nil {
				return "*" + stdlib.TypedefSpelling(c.Typ)
			}
			// *interface{} / *error keeps the pointee's declared name —
			// the pointer itself is the dynamic type, not <nil>.
			if td := stdlib.NilIfaceTyp(dv); td != nil {
				return "*" + stdlib.TypedefSpelling(td)
			}
			return "*" + ScriptTypeString(dv)
		}
		return "unsafe.Pointer"
	case *runtime.GoValue:
		if se, ok := t.V.(*hostbridge.ScriptError); ok {
			return ScriptTypeString(se.V)
		}
		if stdjson.IsEncoder(t.V) {
			return "*json.Encoder"
		}
		switch t.V.(type) {
		case stdruntime.CleanupHandle:
			return "runtime.Cleanup"
		case *minireflect.RValue:
			return "reflect.Value"
		case *minireflect.RType:
			return "*reflect.rtype"
		case *minireflect.MapIter:
			return "*reflect.MapIter"
		case *minireflect.StructField:
			return "reflect.StructField"
		case *minireflect.Method:
			return "reflect.Method"
		case runtime.PlainError:
			// the host type is exported; gc's payload is runtime.plainError.
			return "runtime.plainError"
		}
		if at, ok := t.V.(stdlib.APITypeBox); ok {
			// a facade spells its API type, not the box's name.
			return at.APIType().String()
		}
		if rt := stdtime.ProxyHostTypeOf(t.V); rt != nil {
			// a proxy box spells its API type, not the box's name.
			return rt.String()
		}
		return fmt.Sprintf("%T", t.V)
	case int64:
		return "int"
	case float64:
		return "float64"
	case string:
		return "string"
	case bool:
		return "bool"
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return funcSigSpelling(t)
	default:
		return fmt.Sprintf("%T", x)
	}
}

// UnwrapFmtArg peels the fmt argument box off a `&x` reference — used as
// the io Adapters' UnwrapArg by every installer that hands raw args to
// host calls.
func UnwrapFmtArg(v any) any {
	if fv, ok := v.(*fmtValue); ok {
		return fv.x
	}
	return v
}

// NewFmtValue boxes a script value for host fmt.Format — the repl hooks
// hand script values to Go formatters through it.
func NewFmtValue(c runtime.MemberCaller, x runtime.Value) fmt.Formatter {
	return &fmtValue{c: c, x: x, nilSyntax: true}
}

// fmtsortKeyElems returns the elements two same-shape composite keys
// compare by — a slice's Elems (an array is a Slice carrying an
// array typedef) or a struct's Fields. ok is false for any other
// pair or for differing kinds.
func fmtsortKeyElems(a, b runtime.Value) ([]runtime.Value, []runtime.Value, bool) {
	switch av := a.(type) {
	case *runtime.Slice:
		bv, ok := b.(*runtime.Slice)
		if !ok {
			return nil, nil, false
		}
		return av.Elems, bv.Elems, true
	case *runtime.Struct:
		bv, ok := b.(*runtime.Struct)
		if !ok {
			return nil, nil, false
		}
		return av.Fields, bv.Fields, true
	}
	return nil, nil, false
}

// mapKeyRank ranks a map key by its concrete type like go's
// internal/fmtsort orders interface keys — the type first, then the
// value. Go orders types by the rtype's address, which happens to be a
// fixed layout for the predeclared types (string < int8 < ... < bool)
// and allocation luck for everything else; the predeclared ranks pin
// that layout and other types fall into deterministic groups ordered
// by their typedef spell.
func mapKeyRank(v runtime.Value) (int, string) {
	var spell string
	switch x := v.(type) {
	case nil, runtime.Nil, *runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return 0, ""
	case int64:
		spell = "int"
	case float64:
		spell = "float64"
	case string:
		spell = "string"
	case bool:
		spell = "bool"
	case complex128:
		spell = "complex128"
	case *runtime.UConst:
		// an untyped constant's dynamic type is its default type.
		switch x.V.Kind() {
		case constant.Int:
			spell = "int"
		case constant.Float:
			spell = "float64"
		case constant.String:
			spell = "string"
		case constant.Bool:
			spell = "bool"
		case constant.Complex:
			spell = "complex128"
		default:
			return 60, ""
		}
	case *runtime.Named:
		if x.Typ == nil {
			return 60, ""
		}
		// the tag of a host-typed named — "time.Duration" — must
		// match its GoValue and unboxed forms' spelling below;
		// host types rank after the predeclared set like go's
		// rtype layout does.
		if rt := runtime.HostReflectType(x.Typ); rt != nil {
			if _, basic := fmtKeyBasicRank[rt.String()]; !basic {
				return 50, rt.String()
			}
			spell = rt.String()
		} else {
			spell = stdlib.TypedefSpelling(x.Typ)
		}
	case *runtime.Struct:
		if x.Def != nil && stdlib.IsArrayTyp(x.Def) {
			return 10, stdlib.TypedefSpelling(x.Def)
		}
		if x.Def != nil {
			return 12, stdlib.TypedefSpelling(x.Def)
		}
		return 12, ""
	case *runtime.Chan:
		if x.Typ != nil {
			return 11, stdlib.TypedefSpelling(x.Typ)
		}
		return 11, ""
	case *runtime.GoValue:
		if rt := reflect.TypeOf(x.V); rt != nil {
			if _, basic := fmtKeyBasicRank[rt.String()]; !basic {
				return 50, rt.String()
			}
			spell = rt.String()
		} else {
			return 0, ""
		}
	case *runtime.Cell, *runtime.FieldRef, *runtime.DerefRef, *runtime.IndexRef:
		// every pointer representation is a reflect.Ptr to gc's
		// fmtsort — same type rank regardless of how the pointer
		// was taken (&x vs &s[i]); order falls to the rendered
		// address, which sorts like the numeric one.
		return 47, ""
	default:
		// a raw host named scalar (a bound const's payload) spells
		// like its boxed Named/GoValue forms.
		if _, rt, ok := runtime.HostNamedPayload(v); ok {
			return 50, rt.String()
		}
		return 60, fmt.Sprintf("%T", v)
	}
	if r, ok := fmtKeyBasicRank[spell]; ok {
		return r, ""
	}
	// Named and host types group before the predeclared set, ordered
	// among themselves by spell — their go order is the rtype's
	// address, which no spelling can reproduce.
	return 20, spell
}

// fmtKeyBasicRank is the predeclared types' layout order in go's
// fmtsort output (string < int8 < ... < bool), as printed by go1.27.
var fmtKeyBasicRank = map[string]int{
	"string":     30,
	"int8":       31,
	"uint8":      32,
	"int16":      33,
	"uint16":     34,
	"int32":      35,
	"uint32":     36,
	"int64":      37,
	"uint64":     38,
	"int":        39,
	"uint":       40,
	"uintptr":    41,
	"complex64":  42,
	"complex128": 43,
	"float32":    44,
	"float64":    45,
	"bool":       46,
}
