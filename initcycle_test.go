package minigo_test

import (
	"context"
	"strings"
	"testing"

	"github.com/podhmo/minigo"
)

func TestInitCycleUnboundMemberDoesNotDeadlock(t *testing.T) {
	e := minigo.NewEngine("./testdata/initcycle")
	_, err := e.Run(context.Background(), "./testdata/initcycle/cmd", "main")
	if err == nil || !strings.Contains(err.Error(), "initialization cycle: a.Y is not bound yet") {
		t.Fatalf("cyclic read of an unbound member must fail loudly, got %v", err)
	}
}
