package minigo_test

import (
	"context"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/podhmo/minigo"
)

var (
	concurrencyStress        = flag.Bool("concurrency-stress", false, "run the opt-in shared-engine concurrency stress suite")
	concurrencyStressCases   = flag.String("concurrency-stress-cases", "ArrayTypes,SyncReentry,FuncMap,CallbackRelease", "comma-separated stress fixture names")
	concurrencyStressRounds  = flag.Int("concurrency-stress-rounds", 12, "fresh engines per stress case")
	concurrencyStressWorkers = flag.Int("concurrency-stress-workers", 8, "simultaneous callers per engine")
	concurrencyStressCalls   = flag.Int("concurrency-stress-calls", 10, "calls per worker on each engine")
)

// This is an allowlist, not an invitation to parallelize difffuzz's corpus:
// fixtures have no mutable package globals, shared output, or external effects.
// CallbackRelease reuses #1061's existing fixture and deliberately does not
// join its empty callback: that is the process-release window being tested.
var concurrencyStressFixtures = []struct {
	name, ref, entry string
	want             any
}{
	{"ArrayTypes", "./testdata/concurrencystress", "ArrayTypes", int64(16)},
	{"SyncReentry", "./testdata/concurrencystress", "SyncReentry", int64(2)},
	{"FuncMap", "./testdata/concurrencystress", "FuncMap", "6"},
	{"CallbackRelease", "./testdata/procrace", "CancelThenReturn", "ok"},
}

type concurrencyStressConfig struct {
	rounds, workers, calls int
}

// TestConcurrencyStressSmoke keeps the shared-engine runner and its result
// oracles exercised by ordinary CI, including #1061's callback/release fixture.
func TestConcurrencyStressSmoke(t *testing.T) {
	runConcurrencyStress(t, "ArrayTypes,SyncReentry,FuncMap,CallbackRelease", concurrencyStressConfig{2, 2, 2})
}

// TestConcurrencyStress is bounded by Go's test-process timeout, not by an
// abandoned goroutine or a context deadline that cannot stop arbitrary host
// calls. See docs/concurrency-stress.md for commands and coverage limits.
func TestConcurrencyStress(t *testing.T) {
	if !*concurrencyStress {
		t.Skip("enable with -args -concurrency-stress; see docs/concurrency-stress.md")
	}
	runConcurrencyStress(t, *concurrencyStressCases, concurrencyStressConfig{
		rounds: *concurrencyStressRounds, workers: *concurrencyStressWorkers, calls: *concurrencyStressCalls,
	})
}

func runConcurrencyStress(t *testing.T, selection string, cfg concurrencyStressConfig) {
	t.Helper()
	if cfg.rounds <= 0 || cfg.workers <= 0 || cfg.calls <= 0 {
		t.Fatal("stress rounds, workers, and calls must all be positive")
	}
	// Reject the whole selection before starting work. A misspelling must not
	// quietly pass with zero exercised fixtures.
	selected := strings.Split(selection, ",")
	seen := make(map[string]bool)
	for _, name := range selected {
		found := false
		for _, fixture := range concurrencyStressFixtures {
			if fixture.name == name {
				found = true
				break
			}
		}
		if !found || seen[name] {
			t.Fatalf("unknown or duplicate stress case %q (choose ArrayTypes,SyncReentry,FuncMap,CallbackRelease)", name)
		}
		seen[name] = true
	}
	t.Logf("toolchain=%s platform=%s/%s GOMAXPROCS=%d rounds=%d workers=%d calls/worker=%d cases=%s",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), cfg.rounds, cfg.workers, cfg.calls, selection)
	for _, fixture := range concurrencyStressFixtures {
		if !seen[fixture.name] {
			continue
		}
		t.Run(fixture.name, func(t *testing.T) {
			for round := 0; round < cfg.rounds; round++ {
				// A new engine restores first-use compilation/materialization
				// races. Later calls exercise the same engine's warm caches.
				// Package (rather than Run) avoids main-package metadata writes.
				e := minigo.NewEngine(".", minigo.WithOutput(io.Discard))
				pkg, err := e.Package(context.Background(), fixture.ref)
				if err != nil {
					t.Fatal(err)
				}
				start := make(chan struct{})
				failures := make(chan error, cfg.workers)
				var wg sync.WaitGroup
				for worker := 0; worker < cfg.workers; worker++ {
					wg.Go(func() {
						<-start
						for call := 0; call < cfg.calls; call++ {
							got, err := e.Call(context.Background(), pkg, fixture.entry)
							if err != nil {
								failures <- fmt.Errorf("round=%d worker=%d call=%d: %w", round, worker, call, err)
								return
							}
							if diff := cmp.Diff(fixture.want, got); diff != "" {
								failures <- fmt.Errorf("round=%d worker=%d call=%d (-want +got):\n%s", round, worker, call, diff)
								return
							}
						}
					})
				}
				close(start)
				wg.Wait()
				close(failures)
				for err := range failures {
					t.Error(err)
				}
				if t.Failed() {
					t.FailNow()
				}
			}
			t.Logf("completed %d engines x %d workers x %d calls", cfg.rounds, cfg.workers, cfg.calls)
		})
	}
}
