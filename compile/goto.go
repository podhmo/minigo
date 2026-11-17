// Package compile lowers Go functions, package initializers and
// expressions to [bytecode.Chunk] values for the minigo stack VM.
//
// Every Go program compiles. A construct outside the supported subset
// compiles to a [bytecode.OpTrap] that fails at run time, with its
// position, only if execution reaches it.
//
// Only locals and upvalues are resolved at compile time. Any other name
// compiles to [bytecode.OpGlobal] or [bytecode.OpSetGlobal] and is looked
// up at run time (file imports, then package globals, then builtins),
// which keeps imports lazy.
package compile

import (
	"fmt"
	"go/token"
)

// ctrlCtx is the patching context for breakable/continuable constructs
// (for, range, switch, select).
type ctrlCtx struct {
	isLoop     bool
	labels     []string // label names attached by enclosing LabeledStmt
	continueIP int      // -1 until post position is known
	continues  []int    // continue jumps to patch once continueIP is known
	breaks     []int    // instruction indices to patch to construct end
}

// labelInfo is a label statement's jump target (ip is its instruction
// position). `goto L` resolves against it; `break L`/`continue L` find the
// control construct it was attached to.
type labelInfo struct {
	name   string
	ip     int
	blocks map[int]bool         // blocks open at the label
	vars   map[string]token.Pos // variables in scope at the label
}

// pendingGoto is a goto emitted before its label was defined; resolved at
// the end of the function's compilation.
type pendingGoto struct {
	ins    int
	name   string
	pos    token.Pos
	blocks map[int]bool         // blocks open at the goto
	vars   map[string]token.Pos // variables in scope at the goto
}

// takeLabels hands any pending labels to a newly created ctrlCtx and
// returns their names.
func (c *compiler) takeLabels() []string {
	if len(c.pendingLabels) == 0 {
		return nil
	}
	names := make([]string, len(c.pendingLabels))
	for i, li := range c.pendingLabels {
		names[i] = li.name
	}
	c.pendingLabels = nil
	return names
}

// resolveGotos patches forward gotos to their labels; a goto with no
// matching label in the function becomes a run-time trap.
func (c *compiler) resolveGotos() {
	for _, pg := range c.pendingGotos {
		if li, ok := c.labels[pg.name]; ok {
			if why := gotoViolation(li, pg.blocks, pg.vars); why != "" {
				ti := c.trap(pg.pos, "%s", why)
				c.patchA(pg.ins, ti)
				continue
			}
			c.patchA(pg.ins, li.ip)
			continue
		}
		ti := c.trap(pg.pos, "goto %s: label not defined", pg.name)
		c.patchA(pg.ins, ti)
	}
	c.pendingGotos = nil
}

// gotoViolation reports why `goto L` is illegal given the scope snapshots
// taken at the goto and at the label. Go forbids jumping INTO a block
// (a label's open blocks must all be open at the goto) and jumping OVER a
// variable declaration (every var visible at the label must already be
// visible at the goto). Returns "" when the jump is legal.
func gotoViolation(li *labelInfo, gotoBlocks map[int]bool, gotoVars map[string]token.Pos) string {
	for id := range li.blocks {
		if !gotoBlocks[id] {
			return fmt.Sprintf("goto %s jumps into a block", li.name)
		}
	}
	for n, lp := range li.vars {
		// the declaration the name resolves to at the label must be the
		// same one visible at the goto — a different decl (or none) means
		// the jump skips that variable's declaration
		if gp, ok := gotoVars[n]; !ok || gp != lp {
			return fmt.Sprintf("goto %s jumps over declaration of %s", li.name, n)
		}
	}
	return ""
}
