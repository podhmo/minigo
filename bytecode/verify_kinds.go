package bytecode

import (
	"fmt"
	"sort"
)

// SlotKind is what a slot on the operand stack carries. Only slots a
// real bug could misplace get a concrete kind — the rest stay
// [KindAny] and never conflict: a wrong Any-vs-concrete pairing is
// untraceable statically, but a Type-tier operand landing where a
// plain value is expected (or the reverse) is exactly the class of
// bug deferred operand types introduced.
type SlotKind byte

const (
	KindAny      SlotKind = iota // unconstrained / unknown
	KindScalar                   // int, string, bool, nil — a plain value
	KindType                     // a *runtime.TypeDef operand
	KindTuple                    // packed multi-value (OpPack, comma-ok)
	KindRef                      // cell/ref to a var, field or element
	KindCallable                 // function or method value
)

func (k SlotKind) String() string {
	switch k {
	case KindScalar:
		return "scalar"
	case KindType:
		return "type"
	case KindTuple:
		return "tuple"
	case KindRef:
		return "ref"
	case KindCallable:
		return "callable"
	}
	return "any"
}

// VerifyKinds walks a chunk like [Verify] but tracks the kind of every
// operand-stack slot: each instruction's inputs are checked against
// what the op actually consumes (the static typedef tier of OpCall,
// the TypeDef popped by coercions and asserts, the Tuple under
// OpUnpack), and every merge point must agree on kinds as well as
// depth. Returns the chunk's MaxStack on success.
//
// The kind lattice is deliberately shallow: Any never conflicts, and
// two different concrete kinds meeting at a merge or under an operand
// are reported — the compiler should never emit that.
func VerifyKinds(ch *Chunk) (int, error) {
	code := ch.Code
	n := len(code)
	// entry stack per instruction; visited marks reachability — an
	// empty stack is indistinguishable from a nil slice otherwise,
	// and a loop head re-entered with no operands would loop forever.
	stacks := make([][]SlotKind, n)
	visited := make([]bool, n)
	var problems []string
	maxStack := 0
	type item struct {
		ip    int
		stack []SlotKind
	}
	work := []item{{0, nil}}
	for len(work) > 0 {
		it := work[len(work)-1]
		work = work[:len(work)-1]
		if it.ip == n {
			continue // ran off the end — the loop exits, nothing to check
		}
		if visited[it.ip] {
			seen := stacks[it.ip]
			if len(seen) != len(it.stack) {
				problems = append(problems,
					fmt.Sprintf("ip %d: merge depth %d and %d disagree", it.ip, len(seen), len(it.stack)))
			} else {
				for i := range seen {
					if seen[i] != KindAny && it.stack[i] != KindAny && seen[i] != it.stack[i] {
						problems = append(problems,
							fmt.Sprintf("ip %d: merge slot %d kind %s and %s disagree",
								it.ip, i, seen[i], it.stack[i]))
					}
				}
			}
			continue
		}
		visited[it.ip] = true
		stacks[it.ip] = it.stack
		ins := code[it.ip]
		if need := needed(ins); len(it.stack) < need {
			problems = append(problems,
				fmt.Sprintf("ip %d (%s): stack underflow (needs %d, depth %d)",
					it.ip, ins.Op, need, len(it.stack)))
			continue // a starving instruction's successors are unreachable
		}
		edges := kindSuccs(ch, it.ip, ins, it.stack, &problems)
		for _, e := range edges {
			if e.to < 0 || e.to > n {
				problems = append(problems,
					fmt.Sprintf("ip %d (%s): jump target %d out of range", it.ip, ins.Op, e.to))
				continue
			}
			if len(e.stack) > maxStack {
				maxStack = len(e.stack)
			}
			work = append(work, item{e.to, e.stack})
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return 0, &VerifyError{Problems: problems}
	}
	return maxStack, nil
}

// ksucc is one outgoing edge carrying the whole operand stack.
type ksucc struct {
	to    int
	stack []SlotKind
}

// kindSuccs answers ins's outgoing edges with the stack each edge
// carries. Expectations are checked against the top slots of st:
// [KindAny] expectations match everything, a concrete expected kind
// conflicts only with a different concrete actual kind.
func kindSuccs(ch *Chunk, ip int, ins Instruction, st []SlotKind, problems *[]string) []ksucc {
	next := ip + 1
	one := func(post []SlotKind) []ksucc { return []ksucc{{next, post}} }
	pair := func(post []SlotKind) []ksucc { return []ksucc{{next, post}, {int(ins.A), post}} }

	// expect checks kinds of the last len(exp) slots, exp[0] = deepest.
	expect := func(exp ...SlotKind) {
		base := len(st) - len(exp)
		if base < 0 {
			return // the needs check already reported this
		}
		for i, want := range exp {
			if want == KindAny {
				continue
			}
			got := st[base+i]
			if got != KindAny && got != want {
				*problems = append(*problems,
					fmt.Sprintf("ip %d (%s): slot %d holds %s, want %s",
						ip, ins.Op, base+i, got, want))
			}
		}
	}
	// pop drops k top slots; push appends kinds.
	pop := func(k int) []SlotKind {
		if k >= len(st) {
			return nil
		}
		return append([]SlotKind(nil), st[:len(st)-k]...)
	}
	push := func(post []SlotKind, kinds ...SlotKind) []SlotKind {
		return append(post, kinds...)
	}

	switch ins.Op {
	case OpNop:
		return one(st)
	case OpJump:
		return []ksucc{{int(ins.A), st}}
	case OpConst, OpEvalAST, OpSpecialCall, OpNil:
		// a pushed constant's kind is whatever the const holds — even
		// nil doubles as an absent typedef tier (unresolved static
		// types), so nothing concrete can be claimed here.
		return one(push(st, KindAny))
	case OpLocal, OpUpval, OpGlobal:
		return one(push(st, KindAny))
	case OpLocalTyp, OpUpvalTyp, OpGlobalTyp,
		OpLocalType, OpElemType, OpElemTypeOrNil, OpDeferredTyp:
		return one(push(st, KindType))
	case OpDeferredValueTyp:
		return one(push(st, KindType))
	case OpLocalRef, OpUpvalRef, OpGlobalRef:
		return one(push(st, KindRef))
	case OpFieldRef:
		return one(push(pop(1), KindRef))
	case OpIndexRef:
		return one(push(pop(2), KindRef))
	case OpDerefRef:
		return one(push(pop(1), KindRef))
	case OpMakeClosure:
		return one(push(st, KindCallable))
	case OpDup:
		return one(push(st, st[len(st)-1]))
	case OpDup2:
		return one(push(st, st[len(st)-2], st[len(st)-1]))
	case OpSwap:
		post := pop(2)
		return one(push(post, st[len(st)-1], st[len(st)-2]))
	case OpRot3:
		post := pop(3)
		return one(push(post, st[len(st)-2], st[len(st)-1], st[len(st)-3]))
	case OpDeref:
		return one(push(pop(1), KindAny))
	case OpSelect, OpSelectCall, OpRenewVar, OpIter, OpRecv:
		return one(push(pop(1), KindAny))
	case OpRecvOK:
		return one(push(pop(1), KindTuple))
	case OpUnary:
		return one(push(pop(1), KindScalar))
	case OpNilPtrCheck, OpBox:
		return one(st) // in-place on the top slot
	case OpCoerceTop:
		expect(KindType)
		return one(pop(1))
	case OpPop, OpNewLocal, OpSetLocal, OpSetUpval,
		OpNewGlobal, OpSetGlobal:
		return one(pop(1))
	case OpCoerce, OpCoerceGlobal:
		expect(KindType)
		return one(pop(1))
	case OpFoldArrayLen:
		expect(KindType, KindScalar)
		return one(push(pop(2), KindType))
	case OpBinary, OpSetField, OpIndex, OpSend:
		return one(push(pop(2), KindAny))
	case OpIndexOK:
		return one(push(pop(2), KindTuple))
	case OpAssert:
		if ins.B == 1 {
			expect(KindAny, KindType, KindType)
			return one(push(pop(3), KindAny))
		}
		expect(KindAny, KindType)
		return one(push(pop(2), KindAny))
	case OpAssertOK:
		expect(KindAny, KindType)
		return one(push(pop(2), KindTuple))
	case OpSlice:
		if ins.B == 1 {
			return one(push(pop(4), KindAny))
		}
		return one(push(pop(3), KindAny))
	case OpSetIndex:
		return one(pop(3))
	case OpSetInd:
		return one(pop(2))
	case OpSelArm:
		if ins.B == 1 {
			return one(push(pop(2), KindAny))
		}
		return one(push(pop(1), KindAny))
	case OpCall:
		// [callee, arg1..argA, static1..staticA] -> result: the A
		// topmost slots are the static typedef tier.
		exp := make([]SlotKind, 0, 2*int(ins.A)+1)
		exp = append(exp, KindCallable)
		for i := 0; i < int(ins.A); i++ {
			exp = append(exp, KindAny)
		}
		for i := 0; i < int(ins.A); i++ {
			exp = append(exp, KindType)
		}
		expect(exp...)
		return one(push(pop(2*int(ins.A)+1), KindAny))
	case OpDefer, OpGo:
		return one(pop(2*int(ins.A) + 1))
	case OpPack:
		return one(push(pop(int(ins.A)), KindTuple))
	case OpUnpack:
		expect(KindTuple)
		post := pop(1)
		for i := 0; i < int(ins.A); i++ {
			post = push(post, KindAny)
		}
		return one(post)
	case OpMakeComposite:
		total := int(ins.A)
		if ins.B == 1 {
			total *= 2
		}
		return one(push(pop(total), KindAny))
	case OpSetRefs:
		return one(pop(2 * int(ins.A)))
	case OpCoerceN:
		exp := make([]SlotKind, 0, int(ins.A)+1)
		for i := 0; i < int(ins.A); i++ {
			exp = append(exp, KindType)
		}
		exp = append(exp, KindAny)
		expect(exp...)
		return one(push(pop(int(ins.A)+1), KindAny))
	case OpInstantiate:
		// base is a generic TypeDef or a function value — and the
		// result is the instantiated typedef or the instantiated
		// function (`Id[int]`), so both sides stay Any.
		return one(push(pop(int(ins.A)+1), KindAny))
	case OpJumpFalse, OpJumpTrue, OpJumpIfTypedef:
		return pair(pop(1))
	case OpRangeNext:
		// fallthrough edge pushes C&3 values; the exit edge keeps st.
		post := st
		for i := 0; i < int(ins.C)&3; i++ {
			post = push(post, KindAny)
		}
		return []ksucc{{next, post}, {int(ins.A), st}}
	case OpLenIdxFold, OpLenCallFold:
		// hit: [callee, operand] collapses into the folded scalar.
		return []ksucc{{next, st}, {int(ins.A), push(pop(2), KindScalar)}}
	case OpLenDerefFold:
		miss := st
		if ins.B == 0 {
			expect(KindType)
			miss = pop(1) // a miss drops the typedef-tier operand
		}
		return []ksucc{{next, miss}, {int(ins.A), push(pop(2), KindScalar)}}
	case OpLitKey:
		return []ksucc{{next, st}, {int(ins.A), push(st, KindScalar)}}
	case OpConstExpr:
		return pair(st)
	case OpTypGate:
		return one(st)
	case OpSelWait:
		return selWaitKindSuccs(ch, ip, ins, st, problems)
	case OpPanic:
		return nil
	case OpTrap:
		return nil
	case OpReturn:
		return nil
	default:
		*problems = append(*problems, fmt.Sprintf("ip %d: unknown opcode %d", ip, ins.Op))
		return one(st)
	}
}

// selWaitKindSuccs mirrors selWaitSuccs for kind tracking: the n arms
// pop off, each recv arm's edge carries one more (Any) value.
func selWaitKindSuccs(ch *Chunk, ip int, ins Instruction, st []SlotKind, problems *[]string) []ksucc {
	code := ch.Code
	n := int(ins.A)
	sends := make([]bool, 0, n)
	for i := ip - 1; i >= 0 && len(sends) < n; i-- {
		if code[i].Op == OpSelArm {
			sends = append(sends, code[i].B == 1)
		}
	}
	if len(sends) < n {
		*problems = append(*problems,
			fmt.Sprintf("ip %d (OpSelWait): only %d OpSelArm instructions precede %d cases",
				ip, len(sends), n))
		sends = append(sends, make([]bool, n-len(sends))...)
	}
	for i, j := 0, len(sends)-1; i < j; i, j = i+1, j-1 {
		sends[i], sends[j] = sends[j], sends[i]
	}
	base := st
	if len(st) >= n {
		base = append([]SlotKind(nil), st[:len(st)-n]...)
	}
	table := ip + 1
	slots := n
	if ins.B == 1 {
		slots++
	}
	succs := make([]ksucc, 0, slots)
	for i := 0; i < slots; i++ {
		to := table + i
		if to >= len(code) {
			*problems = append(*problems,
				fmt.Sprintf("ip %d (OpSelWait): jump table slot %d past end of code", ip, i))
			continue
		}
		s := base
		if i < n && !sends[i] {
			s = append(append([]SlotKind(nil), base...), KindAny)
		}
		succs = append(succs, ksucc{to, s})
	}
	return succs
}
