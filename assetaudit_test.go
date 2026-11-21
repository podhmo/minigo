package minigo

import (
	"context"
	"testing"
)

// TestAssetAudit pins the M3-2 opt-in audit: enabled, it fingerprints a
// package's AST + index at build and refuses a Call after the assets
// are mutated; disabled (the default), no fingerprint is recorded so
// the normal path pays no hashing cost.
func TestAssetAudit(t *testing.T) {
	ctx := context.Background()
	dir := writePinPkg(t)

	t.Run("DetectsMutation", func(t *testing.T) {
		e := NewEngine(dir, WithAssetAudit())
		p, err := e.Package(ctx, dir)
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		// mutate the shared asset after build — e.g. what a leaking
		// const-folder or a sibling session could do
		p.Index.Funcs["Inc"].Func.Name.Name = "XInc"
		if _, err := e.Call(ctx, p, "Version"); err == nil {
			t.Error("Call after AST mutation succeeded; want an asset-audit error")
		}
	})

	t.Run("OffByDefault", func(t *testing.T) {
		e := NewEngine(dir)
		p, err := e.Package(ctx, dir)
		if err != nil {
			t.Fatalf("Package: %v", err)
		}
		if p.AssetHash != "" {
			t.Errorf("audit off: AssetHash = %q, want empty — the default path must not pay the hash cost", p.AssetHash)
		}
	})
}
