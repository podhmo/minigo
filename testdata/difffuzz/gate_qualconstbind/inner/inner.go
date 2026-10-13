package inner

// C is an untyped rune constant in a source-indexed package — a var
// bound from it is rune-typed, so `y == i` mismatches like gc.
const C = 'a'
