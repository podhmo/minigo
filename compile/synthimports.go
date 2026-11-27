package compile

import (
	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/internal/spike"
)

// keepSynthImports pins this compilation's minted import refs to the
// finished chunk. The process-wide lookup registry holds them weakly so
// dropped sessions can collect; the chunk is the strong owner for as
// long as its code can still resolve one of the minted qualifiers.
func (c *compiler) keepSynthImports() {
	if len(c.synthImports) != 0 {
		c.ch.Keeps = append(c.ch.Keeps, c.synthImports)
	}
}

// finishChunk completes a compiled chunk's shape metadata and owns its
// minted imports before publication. Keeping this in one place also makes
// every compilation path apply the same classification hook.
func (c *compiler) finishChunk() *bytecode.Chunk {
	c.ch.NLocals = c.fs.nlocals
	c.ch.Upvals = c.fs.upvals
	c.ch.Boxed = c.fs.boxedSlice()
	c.keepSynthImports()
	spike.NoteChunkClassified(c.ch.Boxed)
	return c.ch
}
