package dep2

// dep2 has no Word at all — a use site importing it as `dep` makes
// `dep.Word` a missing member.
type Other int8
