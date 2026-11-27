package ffastsub

import "testing"

func TestNested(t *testing.T) {
	t.Run("first", func(st *testing.T) {
		st.Error("first fails")
	})
	t.Run("second", func(st *testing.T) {
		st.Log("second must not run under -failfast")
	})
}
