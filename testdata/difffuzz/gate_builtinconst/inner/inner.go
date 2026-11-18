package inner

// Consts built from real/imag/complex stay untyped under gc; a var or
// comparison that defers their member answer must keep that.
const R = real(1i)
const C = complex(1, 2)
const I = imag(2i)
