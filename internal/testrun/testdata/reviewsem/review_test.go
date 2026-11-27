package reviewsem

import "testing"

func TestErrorThenSkip(t *testing.T) {
	t.Error("failure is recorded")
	t.Skip("skip does not erase the failure")
}

func TestCleanupError(t *testing.T) {
	t.Cleanup(func() {
		t.Error("cleanup failure is recorded")
	})
}

func TestSkippedSubtestResult(t *testing.T) {
	if !t.Run("skip", func(st *testing.T) {
		st.SkipNow()
	}) {
		t.Error("t.Run returned false for a skipped subtest")
	}
}

func TestZones(t *testing.T) {
	t.Run("America/New_York", func(st *testing.T) {
		st.Log("zone ran")
	})
}

func TestDeferFatalDrainsDefers(t *testing.T) {
	defer t.Log("outer defer ran")
	defer func() {
		t.Fatal("fatal in defer")
	}()
}

func TestSubCleanupError(t *testing.T) {
	t.Run("child", func(st *testing.T) {
		st.Cleanup(func() {
			st.Error("sub cleanup failure is recorded")
		})
	})
}

func TestCleanupPanicDrains(t *testing.T) {
	t.Cleanup(func() {
		t.Log("outer cleanup ran")
	})
	t.Cleanup(func() {
		panic("cleanup boom")
	})
	t.Cleanup(func() {
		t.Log("inner cleanup ran")
	})
}

func TestDeferSkipDoesNotHidePanic(t *testing.T) {
	defer func() {
		panic("panic after skip must win")
	}()
	defer t.SkipNow()
}

func TestCleanupSkipDoesNotHidePanic(t *testing.T) {
	t.Cleanup(func() {
		panic("panic after cleanup skip must win")
	})
	t.Cleanup(func() {
		t.SkipNow()
	})
}

func helperRecoverThenSkip(t *testing.T) {
	defer t.Log("helper cleanup")
	defer func() {
		recover()
		t.SkipNow()
	}()
	panic("inner")
}

func TestRestoredOuterPanicDoesNotCancelSkip(t *testing.T) {
	defer func() {
		helperRecoverThenSkip(t)
		t.Error("unexpected continuation")
	}()
	panic("outer")
}
