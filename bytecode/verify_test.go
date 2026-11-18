package bytecode

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func chunk(code ...Instruction) *Chunk {
	return &Chunk{Code: code}
}

func TestVerifyLinear(t *testing.T) {
	// const, const, binary, return — peak depth 2.
	c := chunk(
		Instruction{Op: OpConst},
		Instruction{Op: OpConst},
		Instruction{Op: OpBinary},
		Instruction{Op: OpReturn, A: 1},
	)
	got, err := Verify(c)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if diff := cmp.Diff(2, got); diff != "" {
		t.Fatalf("MaxStack mismatch (-want +got):\n%s", diff)
	}
}

func TestVerifyBranchMerge(t *testing.T) {
	// 0: const x
	// 1: JumpFalse 4   (pops x)
	// 2: const         (depth 1)
	// 3: Jump 5
	// 4: const         (depth 1)
	// 5: return 1      (both paths arrive with depth 1)
	c := chunk(
		Instruction{Op: OpConst},
		Instruction{Op: OpJumpFalse, A: 4},
		Instruction{Op: OpConst},
		Instruction{Op: OpJump, A: 5},
		Instruction{Op: OpConst},
		Instruction{Op: OpReturn, A: 1},
	)
	if _, err := Verify(c); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyJumpToEnd(t *testing.T) {
	// a jump to len(Code) is a legal termination, not an error.
	c := chunk(
		Instruction{Op: OpJump, A: 2},
		Instruction{Op: OpNop},
	)
	if _, err := Verify(c); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyReject(t *testing.T) {
	for _, tt := range []struct {
		name string
		code []Instruction
		want string // substring of the error
	}{
		{
			name: "underflow",
			code: []Instruction{Instruction{Op: OpPop}},
			want: "stack underflow",
		},
		{
			name: "jump out of range",
			code: []Instruction{Instruction{Op: OpJump, A: 9}},
			want: "jump target 9 out of range",
		},
		{
			name: "negative jump",
			code: []Instruction{Instruction{Op: OpJump, A: -1}},
			want: "out of range",
		},
		{
			name: "merge depth mismatch",
			code: []Instruction{
				// 0: const       (depth 1)
				// 1: JumpFalse 4 (depth 0)
				// 2: const       (depth 1)
				// 3: Jump 4      — reaches 4 with depth 1; the
				//   fallthrough from 1 reaches it with depth 0.
				Instruction{Op: OpConst},
				Instruction{Op: OpJumpFalse, A: 4},
				Instruction{Op: OpConst},
				Instruction{Op: OpJump, A: 4},
				Instruction{Op: OpReturn, A: 1},
			},
			want: "merge depth",
		},
		{
			name: "return underflow",
			code: []Instruction{Instruction{Op: OpReturn, A: 3}},
			want: "stack underflow",
		},
		{
			name: "select table past end",
			code: []Instruction{
				Instruction{Op: OpConst}, Instruction{Op: OpConst},
				Instruction{Op: OpSelArm, B: 1},
				Instruction{Op: OpSelWait, A: 1, B: 0},
				// no OpJump table follows — slot 0 reads past end.
			},
			want: "jump table slot",
		},
		{
			name: "dup on empty stack",
			code: []Instruction{Instruction{Op: OpDup}},
			// net +1 yet reads the top slot — underflow by inputs
			want: "stack underflow",
		},
		{
			name: "call without callee",
			code: []Instruction{Instruction{Op: OpCall}},
			want: "stack underflow",
		},
		{
			name: "underflow loop terminates",
			code: []Instruction{
				Instruction{Op: OpPop},
				// OpJump 1 is never reached: the starving OpPop
				// propagates no negative depth, so no self-loop.
				Instruction{Op: OpJump, A: 1},
			},
			want: "stack underflow",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Verify(&Chunk{Code: tt.code})
			if err == nil {
				t.Fatalf("Verify succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestVerifyKindsReject(t *testing.T) {
	for _, tt := range []struct {
		name string
		code []Instruction
		want string
	}{
		{
			name: "merge kind mismatch",
			code: []Instruction{
				// ip4 arrives [Type], ip3 jumps to ip5 with [Scalar]:
				// both concrete, different — a merge conflict.
				Instruction{Op: OpConst},           // 0 [Any]
				Instruction{Op: OpJumpFalse, A: 4}, // 1 -> 4 on false
				Instruction{Op: OpNil},             // 2 [Scalar]
				Instruction{Op: OpJump, A: 5},      // 3 -> 5 [Scalar]
				Instruction{Op: OpDeferredTyp},     // 4 [Type]
				Instruction{Op: OpReturn, A: 1},    // 5 merge
			},
			want: "merge slot 0 kind type and scalar disagree",
		},
		{
			name: "call on a typedef tier",
			code: []Instruction{
				// OpCall A=1 wants [callable, arg, staticTd] —
				// a TypeDef where the callee should be conflicts.
				Instruction{Op: OpDeferredTyp},
				Instruction{Op: OpLocal},
				Instruction{Op: OpDeferredTyp},
				Instruction{Op: OpCall, A: 1},
			},
			want: "holds type, want callable",
		},
		{
			name: "coerce without typedef",
			code: []Instruction{
				Instruction{Op: OpNil},    // [Scalar]
				Instruction{Op: OpCoerce}, // wants Type on top
			},
			want: "holds scalar, want type",
		},
		{
			name: "unpack a scalar",
			code: []Instruction{
				Instruction{Op: OpNil},
				Instruction{Op: OpUnpack, A: 2},
			},
			want: "holds scalar, want tuple",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := VerifyKinds(&Chunk{Code: tt.code})
			if err == nil {
				t.Fatalf("VerifyKinds succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestVerifyKindsPositive(t *testing.T) {
	// a call with its full static typedef tier verifies:
	// [callable, arg, staticTd] -> [result]
	c := chunk(
		Instruction{Op: OpMakeClosure},
		Instruction{Op: OpLocal},
		Instruction{Op: OpDeferredTyp},
		Instruction{Op: OpCall, A: 1},
		Instruction{Op: OpReturn, A: 1},
	)
	if _, err := VerifyKinds(c); err != nil {
		t.Fatalf("VerifyKinds: %v", err)
	}
}

func TestVerifyTypGateIsNotAJump(t *testing.T) {
	// OpTypGate's A/B index CallSites, not code offsets — a gate
	// pointing at sites 5/6 in a 1-instruction chunk must not read
	// as an out-of-range branch.
	c := chunk(Instruction{Op: OpTypGate, A: 5, B: 6})
	if _, err := Verify(c); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifySelWaitDepths(t *testing.T) {
	// 0: const (chan)      1
	// 1: const (chan)      2
	// 2: const (value)     3
	// 3: SelArm send       2 (pop chan+value, push arm)
	// 4: SelArm recv       2 (pop chan, push arm)
	// 5: SelWait A=2 B=1   base=0: send-arm slot at 0, recv at 1,
	//                        default at 0
	// 6: Jump 9            send case: entered at depth 0
	// 7: Jump 10           recv case: entered at depth 1
	// 8: Jump 11           default: entered at depth 0
	// 9: Return 0          send: nothing received
	// 10: Return 1         recv: one value received
	// 11: Return 0         default
	c := chunk(
		Instruction{Op: OpConst}, Instruction{Op: OpConst}, Instruction{Op: OpConst},
		Instruction{Op: OpSelArm, B: 1},
		Instruction{Op: OpSelArm},
		Instruction{Op: OpSelWait, A: 2, B: 1},
		Instruction{Op: OpJump, A: 9},
		Instruction{Op: OpJump, A: 10},
		Instruction{Op: OpJump, A: 11},
		Instruction{Op: OpReturn, A: 0},
		Instruction{Op: OpReturn, A: 1},
		Instruction{Op: OpReturn, A: 0},
	)
	if _, err := Verify(c); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// the same select with the arms in the other source order still
	// verifies: the send/recv-ness is read from each OpSelArm, not
	// guessed from the table position.
	swapped := chunk(
		Instruction{Op: OpConst}, Instruction{Op: OpConst}, Instruction{Op: OpConst},
		Instruction{Op: OpSelArm},       // recv first
		Instruction{Op: OpSelArm, B: 1}, // send second
		Instruction{Op: OpSelWait, A: 2, B: 1},
		Instruction{Op: OpJump, A: 10}, // recv arm -> depth-1 body
		Instruction{Op: OpJump, A: 9},  // send arm -> depth-0 body
		Instruction{Op: OpJump, A: 11},
		Instruction{Op: OpReturn, A: 0},
		Instruction{Op: OpReturn, A: 1},
		Instruction{Op: OpReturn, A: 0},
	)
	if _, err := Verify(swapped); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}
