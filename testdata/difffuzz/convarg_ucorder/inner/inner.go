package inner

func Bump(p *int) int { *p++; return *p }

func Min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
