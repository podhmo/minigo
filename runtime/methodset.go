package runtime

// PromotedMember locates a promoted (embedded) member inside a struct
// value: Path is the embedded-field index path (reflect's Field.Index
// convention) to the slot whose type carries the member, Depth is the
// promotion depth in embed hops (1 = declared on a directly embedded
// type), and Via selects the binding —
//   - "method": a declared function (Funcs has it when signature-bearing);
//     the slot's value is the receiver
//   - "iface": an interface-typed slot — dispatch dynamically on the
//     stored value
//   - "host": a host-typed slot — reselect the member on the unwrapped box
type PromotedMember struct {
	Path  []int
	Depth int
	Via   string
	// Method reports whether the member is callable — a method or an
	// interface requirement. A promoted FIELD (host structs expose them)
	// wins the selector at its depth like any member but never belongs
	// to the method set, so a winning field keeps the name out of Names.
	Method bool
}

// MethodSet is the result of computing a value or typedef's method
// set once: every consumer — the callable-name check, the signature
// pass, the selector binding, the unsure report — reads the same
// walk's output instead of walking again.
type MethodSet struct {
	// Names is the callable method names: declared plus promoted,
	// with ambiguous promotions already dropped.
	Names map[string]bool
	// Funcs maps each signature-bearing member to its declared
	// Function shell; host reflect methods carry no signature, so a
	// pure host method set keeps Names without Funcs.
	Funcs map[string]*Function
	// Depths maps every member to its smallest promotion depth in
	// embed hops — 0 for members the type declares itself.
	Depths map[string]int
	// Members locates promoted members (depth ≥ 1) by name so selector
	// access can bind them without a second walk. Declared members are
	// not listed — they live on the typedef itself.
	Members map[string]*PromotedMember
	// Ambiguous records members contested at their shallowest depth
	// (name → that depth): absent from Names/Funcs by Go's rule, but a
	// selector must diagnose "ambiguous" rather than "no member".
	Ambiguous map[string]int
	// Unsure reports incomplete resolution — an embedded type failed
	// to resolve, so promoted members may be missing. Interface
	// satisfaction treats an unsure set as optimistic: a missing
	// requirement may live on an unresolved embed.
	Unsure bool
}
