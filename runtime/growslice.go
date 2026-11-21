package runtime

// gc's growslice capacity arithmetic, ported so script slices grow the
// way a compiled program's do: nextslicecap picks the element count,
// roundupsize snaps its byte footprint to the allocator's size classes
// (gc/mksizeclasses.go), and the answer is the class size in elements.
//
// The element size is the SCRIPT type's amd64 size — a []byte element
// is 1 byte — so the rounding lands on gc's cap values instead of the
// host []Value's 16-byte classes.
//
// go1.26's compiler additionally hands a fixed 32-byte stack buffer to
// a small non-escaping append of individual elements
// (ssagen getBackingStoreInfoForAppend): an empty base grown by a
// non-spread append whose new length fits in K=32/elemSize elements
// comes out with exactly K capacity. Spread appends always take the
// plain growslice path.
const growSliceStackBuf = 32

// sizeClassToSize mirrors gc.SizeClassToSize (class 0 is unused).
var sizeClassToSize = [68]uint16{
	0,
	8, 16, 24, 32, 48, 64, 80, 96, 112, 128, 144, 160, 176, 192, 208,
	224, 240, 256, 288, 320, 352, 384, 416, 448, 480, 512, 576, 640,
	704, 768, 896, 1024, 1152, 1280, 1408, 1536, 1792, 2048, 2304,
	2688, 3072, 3200, 3456, 4096, 4864, 5376, 6144, 6528, 6784, 6912,
	8192, 9472, 9728, 10240, 10880, 12288, 13568, 14336, 16384, 18432,
	19072, 20480, 21760, 24576, 27264, 28672, 32768,
}

// roundupSize mirrors runtime roundupsize for small and large sizes:
// small sizes map to a size class; a scan (pointerful) object above
// MinSizeForMallocHeader (512 bytes) carries an 8-byte malloc header.
// Above MaxSmallSize the allocation rounds to the 8KiB page.
func roundupSize(size uintptr, noscan bool) uintptr {
	const (
		smallSizeMax        = 1024
		smallSizeDiv        = 8
		largeSizeDiv        = 128
		mallocHeaderSize    = 8
		minSizeForMallocHdr = 512 // goarch.PtrSize * goarch.PtrBits
		maxSmallSize        = 32768
		pageSize            = 8192
	)
	req := size
	if req <= maxSmallSize-mallocHeaderSize {
		if !noscan && req > minSizeForMallocHdr {
			req += mallocHeaderSize
		}
		var cls int
		if req <= smallSizeMax-smallSizeDiv {
			cls = int(sizeClassIndex8(divRoundUp(int(req), smallSizeDiv)))
		} else {
			cls = int(sizeClassIndex128(divRoundUp(int(req)-smallSizeMax, largeSizeDiv)))
		}
		return uintptr(sizeClassToSize[cls]) - (req - size)
	}
	req += pageSize - 1
	if req < size {
		return size
	}
	return req &^ (pageSize - 1)
}

func divRoundUp(n, d int) int { return (n + d - 1) / d }

// sizeClassIndex8/128 mirror gc.SizeToSizeClass8/SizeToSizeClass128:
// the size class of a request given its index in the granularity table.
func sizeClassIndex8(i int) int {
	if i < 0 {
		i = 0
	}
	if i > 128 {
		i = 128
	}
	return sizeToSizeClass8[i]
}

func sizeClassIndex128(i int) int {
	if i < 0 {
		i = 0
	}
	if i > 248 {
		i = 248
	}
	return sizeToSizeClass128[i]
}

var sizeToSizeClass8 = [129]int{
	0, 1, 2, 3, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12,
	12, 13, 13, 14, 14, 15, 15, 16, 16, 17, 17, 18, 18, 19, 19, 19,
	19, 20, 20, 20, 20, 21, 21, 21, 21, 22, 22, 22, 22, 23, 23, 23,
	23, 24, 24, 24, 24, 25, 25, 25, 25, 26, 26, 26, 26, 27, 27, 27,
	27, 27, 27, 27, 27, 28, 28, 28, 28, 28, 28, 28, 28, 29, 29, 29,
	29, 29, 29, 29, 29, 30, 30, 30, 30, 30, 30, 30, 30, 31, 31, 31,
	31, 31, 31, 31, 31, 31, 31, 31, 31, 31, 31, 31, 31, 32, 32, 32,
	32, 32, 32, 32, 32, 32, 32, 32, 32, 32, 32, 32, 32,
}

var sizeToSizeClass128 = [249]int{
	32, 33, 34, 35, 36, 37, 37, 38, 38, 39, 39, 40, 40, 40, 41, 41, 41, 42, 43, 43,
	44, 44, 44, 44, 44, 45, 45, 45, 45, 45, 45, 46, 46, 46, 46, 47, 47, 47, 47, 47,
	47, 48, 48, 48, 49, 49, 50, 51, 51, 51, 51, 51, 51, 51, 51, 51, 51, 52, 52, 52,
	52, 52, 52, 52, 52, 52, 52, 53, 53, 54, 54, 54, 54, 55, 55, 55, 55, 55, 56, 56,
	56, 56, 56, 56, 56, 56, 56, 56, 56, 57, 57, 57, 57, 57, 57, 57, 57, 57, 57, 58,
	58, 58, 58, 58, 58, 59, 59, 59, 59, 59, 59, 59, 59, 59, 59, 59, 59, 59, 59, 59,
	59, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 60, 61, 61, 61,
	61, 61, 62, 62, 62, 62, 62, 62, 62, 62, 62, 62, 62, 63, 63, 63, 63, 63, 63, 63,
	63, 63, 63, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64,
	64, 64, 64, 64, 64, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65, 65,
	65, 65, 65, 65, 65, 65, 66, 66, 66, 66, 66, 66, 66, 66, 66, 66, 66, 67, 67, 67,
	67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67, 67,
	67, 67, 67, 67, 67, 67, 67, 67, 67,
}

// nextSliceCap mirrors runtime nextslicecap: doubles below the 256
// threshold, then grows by the smooth ~1.25x formula.
func nextSliceCap(newLen, oldCap int) int {
	newcap := oldCap
	doublecap := newcap + newcap
	if newLen > doublecap {
		return newLen
	}
	const threshold = 256
	if oldCap < threshold {
		return doublecap
	}
	for {
		newcap += (newcap + 3*threshold) >> 2
		if uint(newcap) >= uint(newLen) {
			break
		}
	}
	if newcap <= 0 {
		return newLen
	}
	return newcap
}

// GrowSliceCap reports the capacity gc's append would produce growing
// an oldCap-element slice to newLen elements of elemSize bytes each.
// noscan mirrors !et.Pointers(); spread selects the plain growslice
// path a `xs...` call always takes.
func GrowSliceCap(newLen, oldLen, oldCap int, elemSize uintptr, noscan, spread bool) int {
	if newLen <= oldCap {
		return oldCap
	}
	if !spread && oldLen == 0 && elemSize > 0 && elemSize <= growSliceStackBuf {
		if k := int(growSliceStackBuf / elemSize); newLen <= k {
			return k
		}
	}
	if elemSize == 0 {
		return nextSliceCap(newLen, oldCap)
	}
	newcap := nextSliceCap(newLen, oldCap)
	return int(roundupSize(uintptr(newcap)*elemSize, noscan) / elemSize)
}
