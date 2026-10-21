package minigo_test

import (
	"context"
	"testing"

	"github.com/podhmo/minigo"
)

// BenchmarkGenericCall compares 100k calls of a plain function with the
// same call through a generic one (inferred, explicit, partially
// explicit, and a generic type's method).
func BenchmarkGenericCall(b *testing.B) {
	for _, fn := range []string{"RunPlain", "RunInferred", "RunExplicit", "RunPartial", "RunMethod"} {
		b.Run(fn, func(b *testing.B) {
			ctx := context.Background()
			e := minigo.NewEngine(".")
			if _, err := e.Run(ctx, "./testdata/genericbench", fn); err != nil {
				b.Fatalf("Run(%s): %v", fn, err)
			}
			b.ResetTimer()
			for b.Loop() {
				if _, err := e.Run(ctx, "./testdata/genericbench", fn); err != nil {
					b.Fatalf("Run(%s): %v", fn, err)
				}
			}
		})
	}
}
