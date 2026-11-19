package runtime

import (
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func newIntMap(n int) *Map {
	m := &Map{}
	for i := range n {
		m.Insert(int64(i), int64(i*10))
	}
	return m
}

// walkKeys walks m like maps.Keys does, calling body per produced key;
// body returns false to stop.
func walkKeys(m *Map, body func(k Value) bool) {
	w := m.BeginWalk()
	for i := range w.Len() {
		k, ok := w.Key(i)
		if !ok {
			continue
		}
		if !body(k) {
			return
		}
	}
}

// TestMapWalkConcurrentReaders starts read-only walks from many
// goroutines at once — legal in Go — and must not race (go test -race).
func TestMapWalkConcurrentReaders(t *testing.T) {
	m := newIntMap(64)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 2000 {
				walkKeys(m, func(Value) bool { return false })
				w := m.BeginWalk()
				for i := range w.Len() {
					w.At(i)
				}
			}
		})
	}
	wg.Wait()
}

func TestMapWalkMutation(t *testing.T) {
	type result struct {
		Seen []Value
		Left []Value
	}
	cases := []struct {
		name string
		run  func(m *Map) []Value
		want result
	}{
		{
			name: "delete current",
			run: func(m *Map) (seen []Value) {
				walkKeys(m, func(k Value) bool {
					seen = append(seen, k)
					m.Delete(k)
					return true
				})
				return seen
			},
			want: result{Seen: []Value{int64(0), int64(1), int64(2), int64(3), int64(4)}, Left: []Value{}},
		},
		{
			name: "delete next",
			run: func(m *Map) (seen []Value) {
				walkKeys(m, func(k Value) bool {
					seen = append(seen, k)
					m.Delete(k.(int64) + 1)
					return true
				})
				return seen
			},
			want: result{Seen: []Value{int64(0), int64(2), int64(4)}, Left: []Value{int64(0), int64(2), int64(4)}},
		},
		{
			name: "delete and reinsert",
			run: func(m *Map) (seen []Value) {
				walkKeys(m, func(k Value) bool {
					seen = append(seen, k)
					if k == int64(0) {
						m.Delete(int64(3))
						m.Insert(int64(3), int64(99))
					}
					return true
				})
				return seen
			},
			// the reinserted key is found at its old slot (go may or may
			// not produce it; either is allowed)
			// the reinserted key is not produced: its old slot is
			// tombstoned and its new slot is beyond the walk's length
			// (go may or may not produce it; either is allowed)
			want: result{Seen: []Value{int64(0), int64(1), int64(2), int64(4)}, Left: []Value{int64(0), int64(1), int64(2), int64(4), int64(3)}},
		},
		{
			name: "clear",
			run: func(m *Map) (seen []Value) {
				walkKeys(m, func(k Value) bool {
					seen = append(seen, k)
					m.Clear()
					return true
				})
				return seen
			},
			want: result{Seen: []Value{int64(0)}, Left: nil},
		},
		{
			name: "growth is not produced",
			run: func(m *Map) (seen []Value) {
				walkKeys(m, func(k Value) bool {
					seen = append(seen, k)
					m.Insert(k.(int64)+100, int64(0))
					return true
				})
				return seen
			},
			want: result{
				Seen: []Value{int64(0), int64(1), int64(2), int64(3), int64(4)},
				Left: []Value{int64(0), int64(1), int64(2), int64(3), int64(4), int64(100), int64(101), int64(102), int64(103), int64(104)},
			},
		},
		{
			name: "nested walks with inner deletes",
			run: func(m *Map) (seen []Value) {
				walkKeys(m, func(k Value) bool {
					seen = append(seen, k)
					walkKeys(m, func(j Value) bool {
						if j.(int64)%2 == 1 {
							m.Delete(j)
						}
						return true
					})
					return true
				})
				return seen
			},
			want: result{Seen: []Value{int64(0), int64(2), int64(4)}, Left: []Value{int64(0), int64(2), int64(4)}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newIntMap(5)
			left, _ := m.SnapshotKeys()
			_ = left
			seen := c.run(m)
			disp, _ := m.SnapshotKeys()
			got := result{Seen: seen, Left: disp}
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
			// Order keeps tombstoned slots until compaction: its live
			// count always equals Pairs, and Keys stays aligned.
			if len(m.Order) != len(m.Pairs)+m.dead || len(m.Keys) != len(m.Order) {
				t.Errorf("order/pairs out of sync: order=%d keys=%d pairs=%d dead=%d", len(m.Order), len(m.Keys), len(m.Pairs), m.dead)
			}
		})
	}
}

// TestMapWalkDeleteCopiesOnce: deletes tombstone in place — the order
// slice a walk holds is never reallocated or shifted under it.
func TestMapWalkDeleteCopiesOnce(t *testing.T) {
	m := newIntMap(100)
	w := m.BeginWalk()
	m.Delete(int64(10))
	first := &m.Order[0]
	m.Delete(int64(20))
	m.Delete(int64(30))
	if &m.Order[0] != first {
		t.Errorf("later deletes reallocated the order slice")
	}
	if diff := cmp.Diff(100, w.Len()); diff != "" {
		t.Errorf("walk length changed (-want +got):\n%s", diff)
	}
	if _, ok := w.Key(10); ok {
		t.Errorf("deleted key still reported present")
	}
	if k, ok := w.Key(11); !ok || k != int64(11) {
		t.Errorf("walk slot 11 = %v, %v; want 11, true", k, ok)
	}
}
