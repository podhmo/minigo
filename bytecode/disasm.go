package bytecode

import (
	"fmt"
	"strings"
)

// opNames indexes every [Op] by value; keep it in the same order as the
// Op constants so Disasm prints stable names.
var opNames = [...]string{
	OpNop:              "OpNop",
	OpConst:            "OpConst",
	OpNil:              "OpNil",
	OpDup:              "OpDup",
	OpSwap:             "OpSwap",
	OpRot3:             "OpRot3",
	OpPop:              "OpPop",
	OpNewLocal:         "OpNewLocal",
	OpRenewVar:         "OpRenewVar",
	OpLocal:            "OpLocal",
	OpLocalTyp:         "OpLocalTyp",
	OpSetLocal:         "OpSetLocal",
	OpLocalRef:         "OpLocalRef",
	OpUpval:            "OpUpval",
	OpUpvalTyp:         "OpUpvalTyp",
	OpSetUpval:         "OpSetUpval",
	OpUpvalRef:         "OpUpvalRef",
	OpGlobal:           "OpGlobal",
	OpGlobalTyp:        "OpGlobalTyp",
	OpNewGlobal:        "OpNewGlobal",
	OpSetGlobal:        "OpSetGlobal",
	OpGlobalRef:        "OpGlobalRef",
	OpSelect:           "OpSelect",
	OpSetField:         "OpSetField",
	OpIndex:            "OpIndex",
	OpIndexOK:          "OpIndexOK",
	OpSetIndex:         "OpSetIndex",
	OpSlice:            "OpSlice",
	OpDeref:            "OpDeref",
	OpSetInd:           "OpSetInd",
	OpSetRefs:          "OpSetRefs",
	OpBox:              "OpBox",
	OpCall:             "OpCall",
	OpDefer:            "OpDefer",
	OpGo:               "OpGo",
	OpPack:             "OpPack",
	OpUnpack:           "OpUnpack",
	OpMakeComposite:    "OpMakeComposite",
	OpMakeClosure:      "OpMakeClosure",
	OpEvalAST:          "OpEvalAST",
	OpBinary:           "OpBinary",
	OpUnary:            "OpUnary",
	OpJump:             "OpJump",
	OpLenIdxFold:       "OpLenIdxFold",
	OpLenDerefFold:     "OpLenDerefFold",
	OpJumpFalse:        "OpJumpFalse",
	OpJumpTrue:         "OpJumpTrue",
	OpIter:             "OpIter",
	OpRangeNext:        "OpRangeNext",
	OpSend:             "OpSend",
	OpRecv:             "OpRecv",
	OpRecvOK:           "OpRecvOK",
	OpSelArm:           "OpSelArm",
	OpSelWait:          "OpSelWait",
	OpPanic:            "OpPanic",
	OpTrap:             "OpTrap",
	OpReturn:           "OpReturn",
	OpDup2:             "OpDup2",
	OpFieldRef:         "OpFieldRef",
	OpIndexRef:         "OpIndexRef",
	OpNilPtrCheck:      "OpNilPtrCheck",
	OpDerefRef:         "OpDerefRef",
	OpAssert:           "OpAssert",
	OpAssertOK:         "OpAssertOK",
	OpInstantiate:      "OpInstantiate",
	OpElemType:         "OpElemType",
	OpElemTypeOrNil:    "OpElemTypeOrNil",
	OpCoerce:           "OpCoerce",
	OpCoerceTop:        "OpCoerceTop",
	OpCoerceN:          "OpCoerceN",
	OpCoerceGlobal:     "OpCoerceGlobal",
	OpSpecialCall:      "OpSpecialCall",
	OpFoldArrayLen:     "OpFoldArrayLen",
	OpLocalType:        "OpLocalType",
	OpSelectCall:       "OpSelectCall",
	OpLenCallFold:      "OpLenCallFold",
	OpDeferredTyp:      "OpDeferredTyp",
	OpDeferredValueTyp: "OpDeferredValueTyp",
	OpJumpIfTypedef:    "OpJumpIfTypedef",
	OpLitKey:           "OpLitKey",
	OpConstExpr:        "OpConstExpr",
	OpTypGate:          "OpTypGate",
}

// String renders the op's name for disassembly output.
func (o Op) String() string {
	if int(o) < len(opNames) && opNames[o] != "" {
		return opNames[o]
	}
	return fmt.Sprintf("Op(%d)", int(o))
}

// operandKind tags how one operand field of an instruction prints.
type operandKind int

const (
	opdSkip  operandKind = iota // never printed
	opdNum                      // raw number
	opdConst                    // Consts index, printed k<N>
	opdSlot                     // local slot, printed slot<N>
	opdUpval                    // upvalue index, printed uv<N>
	opdSite                     // Sites index, printed site<N>
	opdCSite                    // CallSites index, printed csite<N>
	opdJump                     // branch target, printed -><ip>
	opdBinOp                    // BinOp, printed by name
	opdUnOp                     // UnOp, printed by name
	opdFlags                    // flag bits, printed as a hex mask only when nonzero
)

// opOperands lists, per op, how A B C print. Ops absent from the table
// print A and B as numbers when nonzero (and C likewise).
var opOperands = map[Op][3]operandKind{
	OpConst:            {opdConst, opdSkip, opdSkip},
	OpNewLocal:         {opdSlot, opdNum, opdSkip},
	OpRenewVar:         {opdSlot, opdSkip, opdSkip},
	OpLocal:            {opdSlot, opdSkip, opdSkip},
	OpLocalTyp:         {opdSlot, opdSkip, opdSkip},
	OpSetLocal:         {opdSlot, opdSkip, opdSkip},
	OpLocalRef:         {opdSlot, opdSkip, opdSkip},
	OpUpval:            {opdUpval, opdSkip, opdSkip},
	OpUpvalTyp:         {opdUpval, opdSkip, opdSkip},
	OpSetUpval:         {opdUpval, opdSkip, opdSkip},
	OpUpvalRef:         {opdUpval, opdSkip, opdSkip},
	OpGlobal:           {opdConst, opdSkip, opdSite},
	OpGlobalTyp:        {opdConst, opdSkip, opdSkip},
	OpNewGlobal:        {opdConst, opdNum, opdSkip},
	OpSetGlobal:        {opdConst, opdSkip, opdSkip},
	OpGlobalRef:        {opdConst, opdSkip, opdSkip},
	OpSelect:           {opdConst, opdSkip, opdSkip},
	OpSetField:         {opdConst, opdSkip, opdSkip},
	OpSelectCall:       {opdConst, opdSkip, opdSkip},
	OpFieldRef:         {opdConst, opdFlags, opdSkip},
	OpIndex:            {opdNum, opdNum, opdSkip},
	OpIndexOK:          {opdSkip, opdSkip, opdSkip},
	OpSlice:            {opdSkip, opdNum, opdSkip},
	OpSetRefs:          {opdNum, opdNum, opdSkip},
	OpCall:             {opdNum, opdFlags, opdCSite},
	OpDefer:            {opdNum, opdFlags, opdCSite},
	OpGo:               {opdNum, opdFlags, opdCSite},
	OpPack:             {opdNum, opdSkip, opdSkip},
	OpUnpack:           {opdNum, opdSkip, opdSkip},
	OpMakeComposite:    {opdNum, opdFlags, opdSkip},
	OpMakeClosure:      {opdConst, opdSkip, opdSkip},
	OpEvalAST:          {opdConst, opdSkip, opdSkip},
	OpBinary:           {opdBinOp, opdFlags, opdSkip},
	OpUnary:            {opdUnOp, opdSkip, opdSkip},
	OpJump:             {opdJump, opdSkip, opdSkip},
	OpLenIdxFold:       {opdJump, opdSkip, opdSkip},
	OpJumpFalse:        {opdJump, opdSkip, opdSkip},
	OpJumpTrue:         {opdJump, opdSkip, opdSkip},
	OpRangeNext:        {opdJump, opdSlot, opdNum},
	OpSelArm:           {opdNum, opdFlags, opdSkip},
	OpSelWait:          {opdNum, opdFlags, opdSkip},
	OpTrap:             {opdConst, opdSkip, opdSkip},
	OpReturn:           {opdNum, opdSkip, opdSkip},
	OpIndexRef:         {opdNum, opdFlags, opdSkip},
	OpAssert:           {opdNum, opdFlags, opdSkip},
	OpAssertOK:         {opdNum, opdFlags, opdSkip},
	OpInstantiate:      {opdNum, opdSkip, opdSkip},
	OpCoerce:           {opdSlot, opdSkip, opdSkip},
	OpCoerceN:          {opdNum, opdSkip, opdSkip},
	OpCoerceGlobal:     {opdConst, opdSkip, opdSkip},
	OpSpecialCall:      {opdSkip, opdSkip, opdCSite},
	OpFoldArrayLen:     {opdConst, opdSkip, opdSkip},
	OpLenCallFold:      {opdJump, opdNum, opdSkip},
	OpDeferredTyp:      {opdCSite, opdSkip, opdSkip},
	OpDeferredValueTyp: {opdCSite, opdSkip, opdSkip},
	OpJumpIfTypedef:    {opdJump, opdSkip, opdSkip},
	OpLitKey:           {opdJump, opdCSite, opdConst},
	OpConstExpr:        {opdJump, opdCSite},
	OpTypGate:          {opdCSite, opdCSite, opdConst},
}

// Disasm renders a chunk's code to a stable, line-oriented text form for
// golden tests. Each instruction is one line: pc, op name, operands —
// consts print as k<N>, local slots as slot<N>, upvalues as uv<N>,
// OpGlobal sites as site<N>, and branch targets as -><ip>. A "consts:"
// section then lists each constant: scalars print their value, other
// values print their dynamic type (bytecode cannot see the concrete
// types that live in runtime).
func Disasm(c *Chunk) string {
	var b strings.Builder
	disasmInto(&b, c, "")
	return b.String()
}

// DisasmNested is [Disasm] plus a hook: when a constant holds a nested
// function (whose concrete type lives outside bytecode), nested may
// return its chunk, which is then rendered inline after the parent as
// "nested k<N>: chunk <name>". Pass nil to behave like Disasm.
func DisasmNested(c *Chunk, nested func(cst any) *Chunk) string {
	var b strings.Builder
	disasmInto(&b, c, "")
	if nested == nil {
		return b.String()
	}
	for i, cst := range c.Consts {
		if nc := nested(cst); nc != nil {
			fmt.Fprintf(&b, "nested k%d:\n", i)
			disasmInto(&b, nc, "  ")
		}
	}
	return b.String()
}

func disasmInto(b *strings.Builder, c *Chunk, indent string) {
	fmt.Fprintf(b, "%schunk %s (params=%d locals=%d results=%d", indent, c.Name, c.NParams, c.NLocals, c.NResults)
	if c.IsVararg {
		b.WriteString(" vararg")
	}
	if len(c.Upvals) > 0 {
		fmt.Fprintf(b, " upvals=%d", len(c.Upvals))
	}
	b.WriteString(")\n")
	for ip, ins := range c.Code {
		fmt.Fprintf(b, "%s  %04d  %s", indent, ip, ins.Op)
		kinds, known := opOperands[ins.Op]
		ops := [3]int32{ins.A, ins.B, ins.C}
		for i, v := range ops {
			kind := opdNum
			if known {
				kind = kinds[i]
			}
			if v == 0 && kind == opdNum {
				continue
			}
			// A negative operand is meaningful only where the op table
			// declares a number field (OpReturn's -1 collects named
			// results); unused fields default to -1 elsewhere.
			if v < 0 && !(known && kind == opdNum) {
				continue
			}
			switch kind {
			case opdSkip:
				continue
			case opdNum:
				fmt.Fprintf(b, " %d", v)
			case opdConst:
				fmt.Fprintf(b, " k%d", v)
			case opdSlot:
				fmt.Fprintf(b, " slot%d", v)
			case opdUpval:
				fmt.Fprintf(b, " uv%d", v)
			case opdSite:
				fmt.Fprintf(b, " site%d", v)
			case opdCSite:
				fmt.Fprintf(b, " csite%d", v)
			case opdJump:
				fmt.Fprintf(b, " ->%d", v)
			case opdBinOp:
				fmt.Fprintf(b, " %s", BinOp(v))
			case opdUnOp:
				fmt.Fprintf(b, " %s", UnOp(v))
			case opdFlags:
				if v != 0 {
					fmt.Fprintf(b, " flags=%d", v)
				}
			}
		}
		b.WriteByte('\n')
	}
	if len(c.Consts) > 0 {
		fmt.Fprintf(b, "%sconsts:\n", indent)
		for i, cst := range c.Consts {
			fmt.Fprintf(b, "%s  k%d: %s\n", indent, i, formatConst(cst))
		}
	}
}

func formatConst(cst any) string {
	switch v := cst.(type) {
	case string:
		return fmt.Sprintf("%q (string)", v)
	case int64, int32, int, float64, float32, bool:
		return fmt.Sprintf("%v (%T)", v, v)
	case nil:
		return "nil"
	default:
		return fmt.Sprintf("<%T>", v)
	}
}
