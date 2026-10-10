package main

// unsafe.Offsetof was a stub: the selector operand arrives as the
// field's value, so the field's identity — what the offset measures —
// was lost ("selector results are not values"). The compiler now
// rewrites unsafe.Offsetof(s.f) to pass the base and the field name,
// and the intrinsic measures the offset over the base's declared
// field order (aligned field sizes, matching unsafeSizeOf's
// approximation; host-boxed structs answer via reflect). Found via
// tmpltests TestParseZipFS (hash/crc32's amd64 init reads
// unsafe.Offsetof(cpu.X86.HasAVX512VPCLMULQDQ)).

import (
	"fmt"
	"unsafe"
)

type flags struct {
	a bool
	b bool
	x int64
	c bool
}

func main() {
	f := flags{}
	fmt.Println(unsafe.Offsetof(f.a), unsafe.Offsetof(f.b), unsafe.Offsetof(f.x), unsafe.Offsetof(f.c))
}
