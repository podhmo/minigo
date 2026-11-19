package bytecode

import (
	"fmt"
	"sort"
	"strings"
)

// Verify checks a chunk's instructions and control flow: every jump
// target lands in range, the operand stack never underflows, and all
// paths reaching an instruction agree on the entry stack depth. It
// returns the peak operand depth — the chunk's MaxStack — alongside a
// nil error. Anything reachable by the VM's dispatch rules is walked;
// instructions no edge reaches are ignored (the compiler may leave
// dead code).
//
// A jump target equal to len(Code) is legal: the VM's dispatch loop
// ends when ip passes the last instruction, so it is a plain
// termination, not an out-of-range edge.
func Verify(ch *Chunk) (int, error) {
	code := ch.Code
	n := len(code)
	// entry depth per instruction; -1 = not yet reached.
	depths := make([]int, n)
	for i := range depths {
		depths[i] = -1
	}
	var problems []string
	maxStack := 0
	type item struct {
		ip, depth int
	}
	work := []item{{0, 0}}
	for len(work) > 0 {
		it := work[len(work)-1]
		work = work[:len(work)-1]
		if it.ip == n {
			continue // ran off the end — the loop exits, nothing to check
		}
		if depths[it.ip] >= 0 {
			if depths[it.ip] != it.depth {
				problems = append(problems,
					fmt.Sprintf("ip %d: merge depth %d and %d disagree", it.ip, depths[it.ip], it.depth))
			}
			continue
		}
		depths[it.ip] = it.depth
		ins := code[it.ip]
		if need := needed(ins); it.depth < need {
			problems = append(problems,
				fmt.Sprintf("ip %d (%s): stack underflow (needs %d, depth %d)", it.ip, ins.Op, need, it.depth))
			continue // a starving instruction's successors are unreachable
		}
		post, succs := effect(ch, it.ip, ins, it.depth, &problems)
		if post < 0 {
			problems = append(problems,
				fmt.Sprintf("ip %d (%s): stack underflow (depth %d)", it.ip, ins.Op, it.depth))
			continue // do not propagate a negative depth — it reads as unvisited
		}
		if post > maxStack {
			maxStack = post
		}
		for _, e := range succs {
			if e.to < 0 || e.to > n {
				problems = append(problems,
					fmt.Sprintf("ip %d (%s): jump target %d out of range", it.ip, ins.Op, e.to))
				continue
			}
			work = append(work, item{e.to, e.depth})
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return 0, &VerifyError{Problems: problems}
	}
	return maxStack, nil
}

// VerifyError reports every problem [Verify] found in a chunk, one
// string per problem, sorted for stable output.
type VerifyError struct {
	Problems []string
}

func (e *VerifyError) Error() string {
	return "bytecode.Verify: " + strings.Join(e.Problems, "; ")
}

// succ is one outgoing control-flow edge of an instruction: to is the
// next instruction index and depth the stack depth on entry — fold ops
// reshape the stack only on the taken edge, so the depth travels with
// the edge, not the instruction.
type succ struct {
	to    int
	depth int
}

// effect answers the stack depth after executing ins at ip (post) and
// its outgoing edges, given the instruction's entry depth.
func effect(ch *Chunk, ip int, ins Instruction, d int, problems *[]string) (int, []succ) {
	next := ip + 1
	one := func(post int) (int, []succ) {
		return post, []succ{{next, post}}
	}
	jump := func(to, post int) (int, []succ) {
		return post, []succ{{to, post}}
	}
	pair := func(post int) (int, []succ) {
		return post, []succ{{next, post}, {int(ins.A), post}}
	}
	switch ins.Op {
	case OpNop:
		return one(d)
	case OpConst, OpNil, OpLocal, OpLocalTyp, OpLocalRef,
		OpUpval, OpUpvalTyp, OpUpvalRef,
		OpGlobal, OpGlobalTyp, OpGlobalRef,
		OpEvalAST, OpMakeClosure, OpSpecialCall,
		OpDeferredTyp, OpDeferredValueTyp:
		return one(d + 1)
	case OpDup:
		return one(d + 1)
	case OpDup2:
		return one(d + 2)
	case OpSwap, OpRot3, OpRenewVar, OpIter,
		OpDeref, OpNilPtrCheck, OpDerefRef,
		OpSelect, OpSelectCall, OpFieldRef, OpBox,
		OpUnary, OpRecv, OpRecvOK,
		OpElemType, OpElemTypeOrNil, OpLocalType:
		return one(d)
	case OpPop, OpNewLocal, OpSetLocal, OpSetUpval,
		OpNewGlobal, OpSetGlobal,
		OpBinary, OpSetField, OpIndex, OpIndexRef,
		OpCoerce, OpCoerceTop, OpCoerceGlobal, OpFoldArrayLen:
		return one(d - 1)
	case OpAssert:
		if ins.B == 1 {
			return one(d - 2) // a static-typedef operand sits between
		}
		return one(d - 1)
	case OpAssertOK:
		return one(d - 1) // [typedef, value] -> (value, ok)
	case OpIndexOK:
		return one(d - 1) // [base, index] -> (value, ok)
	case OpSlice:
		if ins.B == 1 {
			return one(d - 3) // three-index: max pops too
		}
		return one(d - 2)
	case OpSetIndex:
		return one(d - 3) // value + index + base
	case OpSetInd:
		return one(d - 2) // value + cell
	case OpSend:
		return one(d - 2)
	case OpSelArm:
		if ins.B == 1 {
			return one(d - 1) // send: pop chan+value, push the arm
		}
		return one(d) // recv: pop chan, push the arm
	case OpCall:
		// [fn, arg1..argA] -> result: the arg statics live on the
		// call site's ArgHint record (P4-4a), not the operand stack.
		return one(d - int(ins.A))
	case OpDefer, OpGo:
		return one(d - 2*int(ins.A) - 1)
	case OpPack:
		return one(d - int(ins.A) + 1)
	case OpUnpack:
		return one(d - 1 + int(ins.A))
	case OpMakeComposite:
		total := int(ins.A)
		if ins.B == 1 {
			total *= 2 // kv pairs: two stack slots per element
		}
		return one(d - total)
	case OpSetRefs:
		return one(d - 2*int(ins.A))
	case OpCoerceN:
		return one(d - int(ins.A))
	case OpInstantiate:
		return one(d - int(ins.A))
	case OpJump:
		return jump(int(ins.A), d)
	case OpJumpFalse, OpJumpTrue, OpJumpIfTypedef:
		return pair(d - 1)
	case OpRangeNext:
		// exit edge keeps the depth; the body edge pushes C&3 values.
		return d, []succ{{next, d + int(ins.C)&3}, {int(ins.A), d}}
	case OpLenIdxFold, OpLenCallFold:
		// a hit replaces [callee, operand] with the folded result and
		// jumps; a miss falls through unchanged.
		return d, []succ{{next, d}, {int(ins.A), d - 1}}
	case OpLenDerefFold:
		miss := d
		if ins.B == 0 {
			miss = d - 1 // a miss pops the typedef-tier operand
		}
		return d, []succ{{next, miss}, {int(ins.A), d - 1}}
	case OpLitKey:
		// map/array answer falls through to the key instructions;
		// otherwise the field name is pushed and control jumps to A.
		return d, []succ{{next, d}, {int(ins.A), d + 1}}
	case OpConstExpr:
		return pair(d)
	case OpTypGate:
		// A and B index CallSites, not code — the gate only traps or
		// falls through, it never jumps and touches no stack slot.
		return one(d)
	case OpSelWait:
		return selWaitSuccs(ch, ip, ins, d, problems)
	case OpPanic:
		return d - 1, nil
	case OpTrap:
		return d, nil
	case OpReturn:
		return d - int(ins.A), nil
	default:
		*problems = append(*problems, fmt.Sprintf("ip %d: unknown opcode %d", ip, ins.Op))
		return d, []succ{{next, d}}
	}
}

// needed answers how many operand slots ins must find on entry — the
// stack effect alone cannot express it (OpDup nets +1 yet reads one,
// OpCall pops 2*A+1 while netting -2*A). An entry depth below this is
// an underflow even when the net change would stay non-negative.
func needed(ins Instruction) int {
	switch ins.Op {
	case OpConst, OpNil, OpNop, OpJump, OpTrap,
		OpLocal, OpLocalTyp, OpLocalRef,
		OpUpval, OpUpvalTyp, OpUpvalRef,
		OpGlobal, OpGlobalTyp, OpGlobalRef,
		OpEvalAST, OpMakeClosure, OpSpecialCall,
		OpDeferredTyp, OpDeferredValueTyp, OpConstExpr, OpTypGate,
		OpRangeNext, OpLocalType:
		return 0 // locals/upvals/globals and const-driven ops read no operand
	case OpDup2, OpSwap, OpBinary, OpSetField, OpIndex,
		OpIndexOK, OpAssertOK, OpSetInd, OpSend,
		OpCoerceTop, OpFoldArrayLen,
		OpLenIdxFold, OpLenCallFold:
		return 2
	case OpRot3, OpSetIndex:
		return 3
	case OpIndexRef:
		return 2 // base + key
	case OpSlice:
		if ins.B == 1 {
			return 4 // three-index: base + lo + hi + max
		}
		return 3
	case OpAssert:
		if ins.B == 1 {
			return 2
		}
		return 1
	case OpSelArm:
		if ins.B == 1 {
			return 2 // send arm: chan + value
		}
		return 1
	case OpCall:
		return int(ins.A) + 1
	case OpDefer, OpGo:
		return 2*int(ins.A) + 1
	case OpPack:
		return int(ins.A)
	case OpMakeComposite:
		total := int(ins.A)
		if ins.B == 1 {
			total *= 2
		}
		return total
	case OpSetRefs:
		return 2 * int(ins.A)
	case OpCoerceN:
		return int(ins.A) + 1 // the typedef tier plus each operand
	case OpInstantiate:
		return int(ins.A) + 1 // type args + base
	case OpSelWait:
		return int(ins.A) // the n pending arms
	case OpReturn:
		return int(ins.A)
	default:
		// the pop-on-read ops and single-operand consumers: OpPop,
		// OpDup, OpLocal, OpUpval, OpGlobal, OpMakeClosure,
		// OpSpecialCall, OpNewLocal, OpSetLocal, OpSetUpval,
		// OpNewGlobal, OpSetGlobal, OpCoerce, OpCoerceTop,
		// OpCoerceGlobal, OpFoldArrayLen, OpDeref, OpNilPtrCheck,
		// OpDerefRef, OpSelect, OpSelectCall, OpFieldRef, OpBox,
		// OpUnary, OpRecv, OpRecvOK, OpElemType, OpElemTypeOrNil,
		// OpLocalType, OpRenewVar, OpIter, OpUnpack, OpJumpFalse,
		// OpJumpTrue, OpJumpIfTypedef, OpRangeNext, OpLenDerefFold,
		// OpLitKey, OpPanic.
		return 1
	}
}

// selWaitSuccs builds OpSelWait's edges: the jump table of OpJump
// instructions follows it, one per case plus the default slot. Arm i's
// recv-ness is read back from the OpSelArm instruction that built it —
// the compiler emits the arms immediately above the wait in case order.
func selWaitSuccs(ch *Chunk, ip int, ins Instruction, d int, problems *[]string) (int, []succ) {
	code := ch.Code
	n := int(ins.A)
	// collect the last n OpSelArm instructions above the wait.
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
	// sends were collected newest-first — reverse to case order.
	for i, j := 0, len(sends)-1; i < j; i, j = i+1, j-1 {
		sends[i], sends[j] = sends[j], sends[i]
	}
	base := d - n // the n arms pop off
	table := ip + 1
	slots := n
	if ins.B == 1 {
		slots++ // trailing default OpJump
	}
	succs := make([]succ, 0, slots)
	for i := 0; i < slots; i++ {
		to := table + i
		if to >= len(code) {
			*problems = append(*problems,
				fmt.Sprintf("ip %d (OpSelWait): jump table slot %d past end of code", ip, i))
			continue
		}
		pushed := 0
		if i < n && !sends[i] {
			pushed = 1 // a recv arm pushes its received value (or Tuple)
		}
		succs = append(succs, succ{to, base + pushed})
	}
	return base, succs
}
