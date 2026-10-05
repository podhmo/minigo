package main

import (
	"strconv"
	"strings"
)

// The text domain targets minigo's main use: text processing and LL-style
// scripting. Instead of hand-coded operators it is driven by a signature
// table — adding surface means adding a row.

var (
	xStr   = &Typ{Name: "string", VarKey: "s", Kind: "string", Values: []string{`""`, `"a,b,,c"`, `"  Hello, World  "`, `"héllo wörld"`, `"日本語テキスト"`, `"key=value; k2=v2"`, `"42"`, `"-3.5e2"`, `"line1\nline2\n"`, `"\t tab\tsep "`, `"GoGoGo"`, `"true"`}}
	xInt   = &Typ{Name: "int", VarKey: "n", Kind: "int", Values: []string{"0", "1", "2", "3", "-1", "5", "10"}}
	xBool  = &Typ{Name: "bool", VarKey: "b", Kind: "bool", Values: []string{"true", "false"}}
	xRune  = &Typ{Name: "rune", VarKey: "r", Kind: "rune", Values: []string{"'a'", "'Z'", "'é'", "'日'", "' '", "','", "'7'", "'\\n'"}}
	xByte  = &Typ{Name: "byte", VarKey: "c", Kind: "byte", Values: []string{"'a'", "'0'", "' '", "0xff"}}
	xFloat = &Typ{Name: "float64", VarKey: "f", Kind: "float", Values: []string{"0.0", "1.5", "-2.25", "100.0", "0.1", "1e6"}}
	xSS    = &Typ{Name: "[]string", VarKey: "ss", Kind: "slice", Values: []string{"nil", "[]string{}", `[]string{"b", "a", "c"}`, `[]string{"x", "", "y", "x"}`, `[]string{"Hello", "héllo", "HELLO"}`}}
	xBS    = &Typ{Name: "[]byte", VarKey: "bs", Kind: "slice", Values: []string{"nil", `[]byte("abc")`, `[]byte("héllo")`, `[]byte{0, 'A', 0xff}`}}
	xRS    = &Typ{Name: "[]rune", VarKey: "rs", Kind: "slice", Values: []string{"nil", `[]rune("héllo")`, `[]rune{'日', '本'}`}}
	xMap   = &Typ{Name: "map[string]int", VarKey: "m", Kind: "map", Values: []string{"nil", "map[string]int{}", `map[string]int{"a": 1, "b": 2, "c": 3}`, `map[string]int{"x": -1, "": 0}`}}

	textTypes = []*Typ{xStr, xInt, xBool, xRune, xByte, xFloat, xSS, xBS, xRS, xMap}
)

// Sig is a typed template. Placeholders $1..$9 are replaced by argument
// expressions, so templates can freely contain fmt verbs.
type Sig struct {
	Name string
	Ret  *Typ
	Args []*Typ
	Tmpl string
}

func (s *Sig) render(kids []*Node) string {
	out := s.Tmpl
	for i := len(kids); i >= 1; i-- { // $10 before $1 (not that we have any)
		out = strings.ReplaceAll(out, "$"+strconv.Itoa(i), kids[i-1].Expr())
	}
	return out
}

func sig(name string, ret *Typ, tmpl string, args ...*Typ) *Sig {
	return &Sig{Name: name, Ret: ret, Args: args, Tmpl: tmpl}
}

var textSigs = []*Sig{
	// --- string -> string
	sig("+", xStr, "($1 + $2)", xStr, xStr),
	sig("ToUpper", xStr, "strings.ToUpper($1)", xStr),
	sig("ToLower", xStr, "strings.ToLower($1)", xStr),
	sig("TrimSpace", xStr, "strings.TrimSpace($1)", xStr),
	sig("Trim", xStr, "strings.Trim($1, $2)", xStr, xStr),
	sig("TrimLeft", xStr, "strings.TrimLeft($1, $2)", xStr, xStr),
	sig("TrimPrefix", xStr, "strings.TrimPrefix($1, $2)", xStr, xStr),
	sig("TrimSuffix", xStr, "strings.TrimSuffix($1, $2)", xStr, xStr),
	sig("TrimFunc", xStr, "strings.TrimFunc($1, unicode.IsSpace)", xStr),
	sig("TrimFuncLit", xStr, "strings.TrimFunc($1, func(r rune) bool { return r == $2 })", xStr, xRune),
	sig("Repeat", xStr, "strings.Repeat($1, $2)", xStr, xInt),
	sig("Replace", xStr, "strings.Replace($1, $2, $3, $4)", xStr, xStr, xStr, xInt),
	sig("ReplaceAll", xStr, "strings.ReplaceAll($1, $2, $3)", xStr, xStr, xStr),
	sig("NewReplacer", xStr, `strings.NewReplacer($2, "<", $3, ">").Replace($1)`, xStr, xStr, xStr),
	sig("Join", xStr, "strings.Join($1, $2)", xSS, xStr),
	sig("Title", xStr, "strings.ToTitle($1)", xStr),
	sig("Map", xStr, "strings.Map(func(r rune) rune { if r == $2 { return -1 }; return unicode.ToUpper(r) }, $1)", xStr, xRune),
	sig("slice", xStr, "$1[$2:]", xStr, xInt),
	sig("slice2", xStr, "$1[$2:$3]", xStr, xInt, xInt),
	sig("string(rune)", xStr, "string($1)", xRune),
	sig("string([]byte)", xStr, "string($1)", xBS),
	sig("string([]rune)", xStr, "string($1)", xRS),
	sig("Itoa", xStr, "strconv.Itoa($1)", xInt),
	sig("Quote", xStr, "strconv.Quote($1)", xStr),
	sig("FormatInt", xStr, "strconv.FormatInt(int64($1), 36)", xInt),
	sig("FormatFloat", xStr, "strconv.FormatFloat($1, 'f', 2, 64)", xFloat),
	sig("Atoi", xStr, "fmt.Sprint(strconv.Atoi($1))", xStr),
	sig("ParseFloat", xStr, "fmt.Sprint(strconv.ParseFloat($1, 64))", xStr),
	sig("ParseBool", xStr, "fmt.Sprint(strconv.ParseBool($1))", xStr),
	sig("Cut", xStr, "fmt.Sprint(strings.Cut($1, $2))", xStr, xStr),
	sig("CutPrefix", xStr, "fmt.Sprint(strings.CutPrefix($1, $2))", xStr, xStr),
	sig("Sprintf%q", xStr, `fmt.Sprintf("%q", $1)`, xStr),
	sig("Sprintf%5s", xStr, `fmt.Sprintf("[%5s|%-6s]", $1, $2)`, xStr, xStr),
	sig("Sprintf%x", xStr, `fmt.Sprintf("%x % X", $1, $1)`, xStr),
	sig("Sprintf%03d", xStr, `fmt.Sprintf("%03d|%-4d|%+d", $1, $1, $1)`, xInt),
	sig("Sprintf%.2f", xStr, `fmt.Sprintf("%.2f|%8.3f|%g|%e", $1, $1, $1, $1)`, xFloat),
	sig("Sprintf%c", xStr, `fmt.Sprintf("%c|%q|%U|%d", $1, $1, $1, $1)`, xRune),
	sig("Sprintf%vss", xStr, `fmt.Sprintf("%v|%q|%d", $1, $1, len($1))`, xSS),
	sig("Sprintf%#vss", xStr, `fmt.Sprintf("%#v", $1)`, xSS),
	sig("Sprintf%vmap", xStr, `fmt.Sprintf("%v|%d", $1, len($1))`, xMap),
	sig("Sprintf%t", xStr, `fmt.Sprintf("%t|%v", $1, $1)`, xBool),
	sig("Sprintf%s[]byte", xStr, `fmt.Sprintf("%s|%x|%v", $1, $1, $1)`, xBS),
	sig("Sprintf%*s", xStr, `fmt.Sprintf("%*s|", $2, $1)`, xStr, xInt),
	sig("Sprint2", xStr, "fmt.Sprint($1, $2)", xStr, xInt),
	sig("Sprintln", xStr, "fmt.Sprintln($1, $2, $3)", xStr, xInt, xBool),
	sig("Builder", xStr, "func() string { var b strings.Builder; for i, x := range $1 { if i > 0 { b.WriteByte(',') }; b.WriteString(x) }; fmt.Fprintf(&b, \"(%d)\", len($1)); return b.String() }()", xSS),
	sig("rangeString", xStr, `func() string { out := ""; for i, r := range $1 { out += fmt.Sprintf("%d:%c,", i, r) }; return out }()`, xStr),
	sig("byteLoop", xStr, `func() string { out := []byte{}; for i := 0; i < len($1); i++ { if $1[i] != ' ' { out = append(out, $1[i]) } }; return string(out) }()`, xStr),
	sig("switchString", xStr, `func() string { switch $1 { case "", "a": return "short"; case "42": return "num"; default: return "other:" + $1 } }()`, xStr),
	sig("ifElseChain", xStr, `func() string { if n := len($1); n == 0 { return "empty" } else if n > 5 { return "long" }; return "mid" }()`, xStr),
	sig("strings.Builder.Grow", xStr, `func() string { var b strings.Builder; b.Grow($2); b.WriteRune($3); b.WriteString($1); return b.String() + strconv.Itoa(b.Len()) }()`, xStr, xInt, xRune),

	// --- -> int
	sig("len", xInt, "len($1)", xStr),
	sig("lenSS", xInt, "len($1)", xSS),
	sig("lenMap", xInt, "len($1)", xMap),
	sig("Index", xInt, "strings.Index($1, $2)", xStr, xStr),
	sig("LastIndex", xInt, "strings.LastIndex($1, $2)", xStr, xStr),
	sig("IndexRune", xInt, "strings.IndexRune($1, $2)", xStr, xRune),
	sig("IndexByte", xInt, "strings.IndexByte($1, $2)", xStr, xByte),
	sig("Count", xInt, "strings.Count($1, $2)", xStr, xStr),
	sig("Compare", xInt, "strings.Compare($1, $2)", xStr, xStr),
	sig("RuneCount", xInt, "utf8.RuneCountInString($1)", xStr),
	sig("RuneLen", xInt, "utf8.RuneLen($1)", xRune),
	sig("mapIndex", xInt, "$1[$2]", xMap, xStr),
	sig("+int", xInt, "($1 + $2)", xInt, xInt),
	sig("int(byte)", xInt, "int($1[$2])", xStr, xInt),
	sig("slices.Index", xInt, "slices.Index($1, $2)", xSS, xStr),
	sig("sumMap", xInt, "func() int { t := 0; for _, v := range $1 { t += v }; return t }()", xMap),

	// --- -> bool
	sig("Contains", xBool, "strings.Contains($1, $2)", xStr, xStr),
	sig("ContainsAny", xBool, "strings.ContainsAny($1, $2)", xStr, xStr),
	sig("ContainsRune", xBool, "strings.ContainsRune($1, $2)", xStr, xRune),
	sig("HasPrefix", xBool, "strings.HasPrefix($1, $2)", xStr, xStr),
	sig("HasSuffix", xBool, "strings.HasSuffix($1, $2)", xStr, xStr),
	sig("EqualFold", xBool, "strings.EqualFold($1, $2)", xStr, xStr),
	sig("==", xBool, "($1 == $2)", xStr, xStr),
	sig("<", xBool, "($1 < $2)", xStr, xStr),
	sig("IsUpper", xBool, "unicode.IsUpper($1)", xRune),
	sig("IsLetter", xBool, "unicode.IsLetter($1)", xRune),
	sig("IsDigit", xBool, "unicode.IsDigit($1)", xRune),
	sig("slices.Contains", xBool, "slices.Contains($1, $2)", xSS, xStr),
	sig("commaOk", xBool, "func() bool { _, ok := $1[$2]; return ok }()", xMap, xStr),
	sig("ss==nil", xBool, "($1 == nil)", xSS),
	sig("m==nil", xBool, "($1 == nil)", xMap),
	sig("ValidString", xBool, "utf8.ValidString($1)", xStr),

	// --- -> rune / byte
	sig("runeAt", xRune, "[]rune($1)[$2]", xStr, xInt),
	sig("ToUpperRune", xRune, "unicode.ToUpper($1)", xRune),
	sig("DecodeRune", xRune, "func() rune { r, _ := utf8.DecodeRuneInString($1); return r }()", xStr),
	sig("byteAt", xByte, "$1[$2]", xStr, xInt),
	sig("byte+", xByte, "($1 + 1)", xByte),

	// --- -> float64
	sig("float(len)", xFloat, "float64(len($1)) / 3", xStr),
	sig("float(int)", xFloat, "float64($1) * 0.5", xInt),

	// --- -> []string
	sig("Split", xSS, "strings.Split($1, $2)", xStr, xStr),
	sig("SplitN", xSS, "strings.SplitN($1, $2, $3)", xStr, xStr, xInt),
	sig("SplitAfter", xSS, "strings.SplitAfter($1, $2)", xStr, xStr),
	sig("Fields", xSS, "strings.Fields($1)", xStr),
	sig("FieldsFunc", xSS, "strings.FieldsFunc($1, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) })", xStr),
	sig("append", xSS, "append($1, $2)", xSS, xStr),
	sig("appendSpread", xSS, "append($1, $2...)", xSS, xSS),
	sig("sliceSS", xSS, "$1[$2:]", xSS, xInt),
	sig("sorted", xSS, "func() []string { c := slices.Clone($1); sort.Strings(c); return c }()", xSS),
	sig("slices.Sorted", xSS, "func() []string { c := append([]string(nil), $1...); slices.Sort(c); return c }()", xSS),
	sig("sortSlice", xSS, "func() []string { c := append([]string{}, $1...); sort.Slice(c, func(i, j int) bool { return len(c[i]) < len(c[j]) }); return c }()", xSS),
	sig("mapKeys", xSS, "func() []string { ks := []string{}; for k := range $1 { ks = append(ks, k) }; sort.Strings(ks); return ks }()", xMap),
	sig("filter", xSS, `func() []string { var out []string; for _, x := range $1 { if x != "" { out = append(out, strings.TrimSpace(x)) } }; return out }()`, xSS),
	sig("dedupe", xSS, "func() []string { seen := map[string]bool{}; out := []string{}; for _, x := range $1 { if !seen[x] { seen[x] = true; out = append(out, x) } }; return out }()", xSS),
	sig("copyMut", xSS, `func() []string { c := make([]string, len($1)); copy(c, $1); if len(c) > 0 { c[0] = "!" }; return c }()`, xSS),
	sig("regexpAll", xSS, `regexp.MustCompile("[a-z]+").FindAllString($1, -1)`, xStr),

	// --- -> []byte / []rune
	sig("[]byte", xBS, "[]byte($1)", xStr),
	sig("bytes.ToUpper", xBS, "bytes.ToUpper($1)", xBS),
	sig("appendByte", xBS, "append($1, $2)", xBS, xByte),
	sig("[]rune", xRS, "[]rune($1)", xStr),
	sig("reverseRunes", xRS, "func() []rune { r := []rune($1); for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 { r[i], r[j] = r[j], r[i] }; return r }()", xStr),

	// --- -> map[string]int
	sig("wordCount", xMap, "func() map[string]int { m := map[string]int{}; for _, w := range $1 { m[w]++ }; return m }()", xSS),
	sig("parseKV", xMap, `func() map[string]int { m := map[string]int{}; for _, kv := range strings.Split($1, ";") { k, v, ok := strings.Cut(kv, "="); if !ok { continue }; m[strings.TrimSpace(k)] = len(v) }; return m }()`, xStr),
	sig("mapsClone", xMap, "func() map[string]int { c := maps.Clone($1); if c == nil { c = map[string]int{} }; c[$2]++; return c }()", xMap, xStr),
	sig("deleteKey", xMap, "func() map[string]int { c := map[string]int{}; for k, v := range $1 { c[k] = v }; delete(c, $2); return c }()", xMap, xStr),
}

var textImports = []string{"bytes", "fmt", "maps", "regexp", "slices", "sort", "strconv", "strings", "unicode", "unicode/utf8"}

func sigsReturning(sigs []*Sig, t *Typ) []*Sig {
	var out []*Sig
	for _, s := range sigs {
		if s.Ret == t {
			out = append(out, s)
		}
	}
	return out
}

func (g *Gen) textExpr(t *Typ, depth int) *Node {
	sigs := sigsReturning(g.D.Sigs, t)
	if depth <= 0 || len(sigs) == 0 || g.R.IntN(4) == 0 {
		return g.leaf(t)
	}
	s := sigs[g.R.IntN(len(sigs))]
	kids := make([]*Node, len(s.Args))
	for i, a := range s.Args {
		kids[i] = g.textExpr(a, depth-1)
	}
	return &Node{T: t, Op: "call", Sig: s, Kids: kids}
}
