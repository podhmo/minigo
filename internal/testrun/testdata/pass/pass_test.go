package pass

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(1, 2); got != 3 {
		t.Errorf("Add(1,2) = %d, want 3", got)
	}
}

func TestLog(t *testing.T) {
	t.Log("greeting:", greeting)
}

func TestSkip(t *testing.T) {
	t.Skip("not today")
}

func TestSub(t *testing.T) {
	t.Run("alpha", func(t *testing.T) {
		t.Log("inside alpha")
	})
	t.Run("beta", func(t *testing.T) {
		if Add(2, 2) != 4 {
			t.Error("2+2 != 4")
		}
	})
}

func TestCleanup(t *testing.T) {
	t.Cleanup(func() { t.Log("cleanup ran") })
	t.Log("body ran")
}
