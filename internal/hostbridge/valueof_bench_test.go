package hostbridge

import (
	"reflect"
	"testing"
)

func BenchmarkSizedScalarResult(b *testing.B) {
	rv := reflect.ValueOf(int8(3))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = GoValueOf(rv)
	}
}
