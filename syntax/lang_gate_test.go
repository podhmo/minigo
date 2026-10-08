package syntax

import (
	"go/version"
	"os"
	"regexp"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestLatestGate keeps CheckLang's early return honest: latestGate must
// be the newest version any fail call in lang.go gates on.
func TestLatestGate(t *testing.T) {
	src, err := os.ReadFile("lang.go")
	if err != nil {
		t.Fatal(err)
	}
	newest := ""
	for _, m := range regexp.MustCompile(`c\.fail\([^\n]*?"(go1\.\d+)"`).FindAllSubmatch(src, -1) {
		if v := string(m[1]); newest == "" || version.Compare(v, newest) > 0 {
			newest = v
		}
	}
	if diff := cmp.Diff(latestGate, newest); diff != "" {
		t.Errorf("latestGate is stale (-const +newest gate):\n%s", diff)
	}
}
