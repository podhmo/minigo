package runtime

// MethodSet is the result of computing a value or typedef's method
// set once: every consumer — the callable-name check, the signature
// pass, the unsure report — reads the same walk's output instead of
// walking again.
type MethodSet struct {
	// Names is the callable method names: declared plus promoted,
	// with ambiguous promotions already dropped.
	Names map[string]bool
	// Funcs maps each signature-bearing member to its declared
	// Function shell; host reflect methods carry no signature, so a
	// pure host method set keeps Names without Funcs.
	Funcs map[string]*Function
	// Unsure reports incomplete resolution — an embedded type failed
	// to resolve, so promoted members may be missing. Interface
	// satisfaction treats an unsure set as optimistic: a missing
	// requirement may live on an unresolved embed.
	Unsure bool
}
