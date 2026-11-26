package compile

// keepSynthImports pins this compilation's minted import refs to the
// finished chunk. The process-wide lookup registry holds them weakly so
// dropped sessions can collect; the chunk is the strong owner for as
// long as its code can still resolve one of the minted qualifiers.
func (c *compiler) keepSynthImports() {
	if len(c.synthImports) != 0 {
		c.ch.Keeps = append(c.ch.Keeps, c.synthImports)
	}
}
