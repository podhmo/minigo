package minigo

import (
	"errors"
	"fmt"
	"go/ast"
	"reflect"
	"sync"
	"time"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/vm"
)

func (e *Engine) installTime(h *hostHelpers) {
	// monthTD backs both the time.Month conversion and the January..
	// December consts: bound consts carry the conversion's shape
	// (Named{time.Month, int64}) so time.January == time.Month(1) like
	// gc — a raw time.Month bind never adopted the typedef, so `m ==
	// time.Month(1)` and `m == 1` both diverged.
	monthTD := &runtime.TypeDef{Name: "time.Month", Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("int"), HostScalar: time.Month(0)}

	monthConst := func(m time.Month) runtime.Value {
		return &runtime.Named{Typ: monthTD, V: int64(m)}
	}

	// weekdayTD mirrors monthTD for time.Weekday and Sunday..Saturday.
	weekdayTD := &runtime.TypeDef{Name: "time.Weekday", Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("int"), HostScalar: time.Weekday(0)}

	weekdayConst := func(d time.Weekday) runtime.Value {
		return &runtime.Named{Typ: weekdayTD, V: int64(d)}
	}

	e.Bind("time", map[string]runtime.Value{
		// *time.ParseError: errors.As / type asserts on Parse's error.
		"ParseError": hostType("time.ParseError", func() any { return &time.ParseError{} }),
		"Sleep":      h.fn("time.Sleep", func(a []any) (any, error) { time.Sleep(durOf(a[0])); return nil, nil }),
		"After": h.fnvc("time.After", func(vc runtime.VMCaller, a []any) (any, error) {
			// a proxy feed like NewTimer's: once the fire is delivered the
			// channel is dead, and a second receive must count as asleep
			// for deadlock detection instead of hanging forever.
			f := newChanFeed(vc, time.After(durOf(a[0])), true)
			// gc hands out a <-chan: a script-side send on it must trap,
			// not silently succeed on the bidirectional proxy.
			return (<-chan time.Time)(f.C), nil
		}),
		"NewTimer": h.fnvc("time.NewTimer", func(vc runtime.VMCaller, a []any) (any, error) {
			t := time.NewTimer(durOf(a[0]))
			f := newChanFeed(vc, t.C, true)
			return &timerChanBox{t: t, C: f.C, feed: f}, nil
		}),
		"NewTicker": h.fnvc("time.NewTicker", func(vc runtime.VMCaller, a []any) (any, error) {
			t := time.NewTicker(durOf(a[0]))
			f := newChanFeed(vc, t.C, false)
			return &tickerChanBox{t: t, C: f.C, feed: f}, nil
		}),
		"AfterFunc": &runtime.BuiltinFunc{Name: "time.AfterFunc", Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
			if len(args) < 2 {
				return nil, errors.New("time.AfterFunc needs 2 args")
			}
			f := args[1]
			switch f.(type) {
			case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.Named,
				runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
				// a nil callback registers like Go — calling it fails at
				// fire time through the goroutine-failure path
			default:
				return nil, fmt.Errorf("time.AfterFunc: cannot use %T as func()", f)
			}
			// the pending timer keeps the process alive for deadlock
			// detection (a gc timer does the same); the wrapper releases
			// the note when the callback starts — after which the
			// spawned goroutine counts itself — or when Stop prevents
			// the callback from ever running.
			t := &afterFuncTimer{note: func() func() { return vm.NoteExternalWait(vc) }}
			t.hold()
			t.Timer = time.AfterFunc(durOf(goNative(args[0])), func() {
				t.drop()
				// the timer fires on a host goroutine — run the
				// callback like `go f()`: a panic inside fails the
				// process through the same path as a goroutine's.
				vc.Spawn(f, nil)
			})
			return &runtime.GoValue{V: t}, nil
		}},
		"Now":       h.fn("time.Now", func(a []any) (any, error) { return time.Now(), nil }, time.Now),
		"Time":      hostType("time.Time", func() any { return time.Time{} }),
		"Duration":  &runtime.TypeDef{Name: "time.Duration", Kind: runtime.KindNamedBasic, Anon: ast.NewIdent("int64"), HostScalar: time.Duration(0)},
		"Month":     monthTD,
		"January":   monthConst(time.January),
		"February":  monthConst(time.February),
		"March":     monthConst(time.March),
		"April":     monthConst(time.April),
		"May":       monthConst(time.May),
		"June":      monthConst(time.June),
		"July":      monthConst(time.July),
		"August":    monthConst(time.August),
		"September": monthConst(time.September),
		"October":   monthConst(time.October),
		"November":  monthConst(time.November),
		"December":  monthConst(time.December),
		"Weekday":   weekdayTD,
		"Sunday":    weekdayConst(time.Sunday),
		"Monday":    weekdayConst(time.Monday),
		"Tuesday":   weekdayConst(time.Tuesday),
		"Wednesday": weekdayConst(time.Wednesday),
		"Thursday":  weekdayConst(time.Thursday),
		"Friday":    weekdayConst(time.Friday),
		"Saturday":  weekdayConst(time.Saturday),
		"Location":  hostType("time.Location", func() any { return time.Local }),
		"UTC":       &runtime.GoValue{V: time.UTC},
		"Local":     &runtime.GoValue{V: time.Local},
		"Since": h.fn("time.Since", func(a []any) (any, error) {
			if t, ok := a[0].(time.Time); ok {
				return time.Since(t), nil
			}
			return nil, fmt.Errorf("time.Since: not a Time")
		}),
		"Parse": h.fn2("time.Parse", func(a []any) (any, error) {
			t, err := time.Parse(str(a[0]), str(a[1]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(t), errVal(err)}}, nil
		}),
		"ParseInLocation": h.fn("time.ParseInLocation", func(a []any) (any, error) {
			if len(a) != 3 {
				return nil, errors.New("time.ParseInLocation needs 3 args")
			}
			loc, ok := a[2].(*time.Location)
			if !ok {
				return nil, fmt.Errorf("time.ParseInLocation: %T is not a *time.Location", a[2])
			}
			t, err := time.ParseInLocation(str(a[0]), str(a[1]), loc)
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(t), errVal(err)}}, nil
		}),
		"ParseDuration": h.fn1("time.ParseDuration", func(a []any) (any, error) {
			d, err := time.ParseDuration(str(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{scriptVal(d), errVal(err)}}, nil
		}),
		"Date": h.fn("time.Date", func(a []any) (any, error) {
			if len(a) != 8 {
				return nil, fmt.Errorf("time.Date needs 8 args, got %d", len(a))
			}
			loc, ok := a[7].(*time.Location)
			if !ok {
				return nil, fmt.Errorf("time.Date: %T is not a *time.Location", a[7])
			}
			return &runtime.GoValue{V: time.Date(int(int64Of(a[0])), time.Month(int64Of(a[1])),
				int(int64Of(a[2])), int(int64Of(a[3])), int(int64Of(a[4])), int(int64Of(a[5])), int(int64Of(a[6])), loc)}, nil
		}),
		"Unix": h.fn2("time.Unix", func(a []any) (any, error) {
			return &runtime.GoValue{V: time.Unix(int64Of(a[0]), int64Of(a[1]))}, nil
		}),
		"FixedZone": h.fn2("time.FixedZone", func(a []any) (any, error) {
			return &runtime.GoValue{V: time.FixedZone(str(a[0]), int(int64Of(a[1])))}, nil
		}),
		"Nanosecond":  time.Nanosecond,
		"Microsecond": time.Microsecond,
		"Second":      time.Second,
		"Minute":      time.Minute,
		"Hour":        time.Hour,
		"Millisecond": time.Millisecond,
		"Layout":      time.Layout,
		"ANSIC":       time.ANSIC,
		"UnixDate":    time.UnixDate,
		"RubyDate":    time.RubyDate,
		"RFC822":      time.RFC822,
		"RFC822Z":     time.RFC822Z,
		"RFC850":      time.RFC850,
		"RFC1123":     time.RFC1123,
		"RFC1123Z":    time.RFC1123Z,
		"RFC3339":     time.RFC3339,
		"RFC3339Nano": time.RFC3339Nano,
		"Kitchen":     time.Kitchen,
		"Stamp":       time.Stamp,
		"StampMilli":  time.StampMilli,
		"StampMicro":  time.StampMicro,
		"StampNano":   time.StampNano,
		"DateTime":    time.DateTime,
		"DateOnly":    time.DateOnly,
		"TimeOnly":    time.TimeOnly,
	})
}

// chanFeed proxies a timer/ticker channel so a parked receiver's wake
// state can follow the channel's liveness: armed while a future send
// exists (the timer pending, the ticker ticking), dead once none can
// arrive (Stop won, or a one-shot fire was already delivered). The
// proxy registers its hchan as managed — chanOf then counts a receiver
// parked on a dead feed as asleep, the way gc treats a channel no timer
// can feed, instead of leaving the park wakeable forever.
type chanFeed struct {
	vc   runtime.CallerRef
	C    chan time.Time   // the script-visible channel (proxied)
	src  <-chan time.Time // the real timer channel the pump forwards
	ptr  uintptr          // managed-channel id (0 = unmanaged, no proc)
	once bool             // one-shot feed: die after the first value
	op   sync.Mutex       // serializes Stop/Reset (never taken by a pump)
	mu   sync.Mutex
	cur  *feedPump // the live pump (nil once retired)
}

// newChanFeed starts a pump forwarding src into a buffered proxy channel
// and registers the proxy's liveness with the caller's process.
func newChanFeed(vc runtime.CallerRef, src <-chan time.Time, once bool) *chanFeed {
	f := &chanFeed{
		vc:   vc,
		C:    make(chan time.Time, 1),
		src:  src,
		once: once,
	}
	f.ptr = vm.RegisterWakeChan(vc, reflect.ValueOf(f.C))
	f.start()
	return f
}

// timerChanBox is the time.Timer NewTimer hands to the script: only the
// Timer surface (C/Stop/Reset) while the feed tracks liveness. The
// underlying timer stays unexported so the real channel cannot leak past
// the proxy.
type timerChanBox struct {
	t    *time.Timer
	C    <-chan time.Time
	feed *chanFeed
}

// tickerChanBox is the time.Ticker NewTicker hands to the script; the
// feed stays armed until Stop, since a ticker can always send again.
type tickerChanBox struct {
	t    *time.Ticker
	C    <-chan time.Time
	feed *chanFeed
}

// proxyHostTypes maps each liveness-tracking proxy box to the API type
// it stands in for: the script must see *time.Timer/*time.Ticker (in %T
// and reflect), never the box's own name.
var proxyHostTypes = map[reflect.Type]reflect.Type{
	reflect.TypeOf((*timerChanBox)(nil)):   reflect.TypeOf((*time.Timer)(nil)),
	reflect.TypeOf((*tickerChanBox)(nil)):  reflect.TypeOf((*time.Ticker)(nil)),
	reflect.TypeOf((*afterFuncTimer)(nil)): reflect.TypeOf((*time.Timer)(nil)),
}

// proxyHostType resolves the public spelling of a host type — the
// TypeAlias hook for the reflect facade.
func proxyHostType(rt reflect.Type) reflect.Type {
	if rt == nil {
		return nil
	}
	if a, ok := proxyHostTypes[rt]; ok {
		return a
	}
	return rt
}

// proxyUnwrapHost is the facade's UnwrapHost hook — the inverse of
// proxyHostType: a reflect call site demanding the public type a box
// stands in for (Method(i).Func.Call's receiver, Set's target) gets the
// wrapped real timer/ticker. The call then runs the real timer's
// method, bypassing the box's feed bookkeeping — script-side calls
// through the box stay feed-aware; the reflect path trades the
// bookkeeping for gc-faithful observable behavior.
func proxyUnwrapHost(v any, t reflect.Type) (reflect.Value, bool) {
	var inner any
	switch b := v.(type) {
	case *timerChanBox:
		inner = b.t
	case *tickerChanBox:
		inner = b.t
	case *afterFuncTimer:
		inner = b.Timer
	default:
		return reflect.Value{}, false
	}
	rv := reflect.ValueOf(inner)
	if !rv.IsValid() || !rv.Type().AssignableTo(t) {
		return reflect.Value{}, false
	}
	return rv, true
}

// proxyHostTypeOf is proxyHostType over a value — the fmt path's %T
// lookup.
func proxyHostTypeOf(v any) reflect.Type {
	if v == nil {
		return nil
	}
	return proxyHostTypes[reflect.TypeOf(v)]
}

// afterFuncTimer is the time.Timer AfterFunc hands to the script: a
// *time.Timer wrapper that owns the pending-callback note registered
// for deadlock detection. The note releases when the callback fires or
// when Stop wins; Reset re-arms it, like gc rescheduling the callback.
// hold/drop keep exactly one note per pending fire.
type afterFuncTimer struct {
	*time.Timer
	mu      sync.Mutex
	note    func() func() // registers a fresh external-wait note
	release func()        // releases the held note (nil when none)
}

// pump forwards each src value into the proxy until p.done closes; a
// one-shot feed dies after its single delivery. A pump is retired only
// before the real timer is stopped or reset, so a fire it holds at that
// point predates the Stop/Reset and is dropped, as go1.23 discards it.
func (f *chanFeed) pump(p *feedPump) {
	defer close(p.exited)
	for {
		select {
		case v := <-f.src:
			if !f.once {
				// a repeating feed drops a tick nobody has collected
				// yet, like gc's ticker — the 1-buffer keeps only the
				// latest.
				select {
				case f.C <- v:
				default:
				}
				continue
			}
			select {
			case f.C <- v:
			case <-p.done:
				p.dropped = true
				return
			}
			f.dieIf(p)
			return
		case <-p.done:
			return
		}
	}
}

// start (re)arms the feed with a fresh pump: the managed channel reports
// alive again. Callers hold f.op (or own f exclusively).
func (f *chanFeed) start() {
	p := &feedPump{done: make(chan struct{}), exited: make(chan struct{})}
	f.mu.Lock()
	f.cur = p
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, false)
	}
	f.mu.Unlock()
	go f.pump(p)
}

// dieIf marks the feed dead after a one-shot delivery — but only while
// p is still the live pump; a Stop/Reset already owns the lifecycle.
func (f *chanFeed) dieIf(p *feedPump) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cur != p {
		return
	}
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, true)
	}
}

// retire stops the live pump and waits for it to exit, so no value read
// from src before the coming Stop/Reset can reach C afterwards. It
// reports whether the pump held an undelivered fire. Callers hold f.op;
// f.mu is released while waiting so the pump's dieIf cannot deadlock.
func (f *chanFeed) retire() bool {
	f.mu.Lock()
	p := f.cur
	f.cur = nil
	f.mu.Unlock()
	if p == nil {
		return false
	}
	close(p.done)
	<-p.exited
	return p.dropped
}

// drain discards a fire left in the proxy buffer, reporting whether one
// was there: go1.23 makes no pre-Stop/Reset value receivable afterwards.
func (f *chanFeed) drain() bool {
	select {
	case <-f.C:
		return true
	default:
		return false
	}
}

// stop retires the feed for good around the real stop: parked receivers
// join the asleep count. It reports whether a fire was still pending
// (the real timer's own stop result, a dropped fire, or a buffered one).
func (f *chanFeed) stop(realStop func() bool) bool {
	f.op.Lock()
	defer f.op.Unlock()
	dropped := f.retire()
	stopped := realStop()
	drained := f.drain()
	if f.ptr != 0 {
		vm.SetWakeChanDead(f.vc, f.ptr, true)
	}
	return stopped || dropped || drained
}

// reset retires the old pump, resets the real timer, discards any stale
// fire, and re-arms with a fresh pump. It reports whether a fire was
// still pending, like stop.
func (f *chanFeed) reset(realReset func() bool) bool {
	f.op.Lock()
	defer f.op.Unlock()
	dropped := f.retire()
	active := realReset()
	drained := f.drain()
	f.start()
	return active || dropped || drained
}

// Stop cancels the timer like time.Timer.Stop: the feed dies and parked
// receivers count asleep. As in go1.23, a fire not yet received counts
// as stopped (true) and is discarded.
func (b *timerChanBox) Stop() bool {
	return b.feed.stop(b.t.Stop)
}

// Reset reschedules the timer like time.Timer.Reset: a dead feed
// re-arms and a stale fire is discarded.
func (b *timerChanBox) Reset(d time.Duration) bool {
	return b.feed.reset(func() bool { return b.t.Reset(d) })
}

// Stop halts the ticker like time.Ticker.Stop: no more ticks can arrive,
// so the feed dies and parked receivers count asleep.
func (b *tickerChanBox) Stop() {
	b.feed.stop(func() bool { b.t.Stop(); return false })
}

// Reset restarts the ticker like time.Ticker.Reset: a stopped feed
// re-arms so parked receivers wake again.
//
// The interval is validated before the feed retires its pump: a
// recovered panic must leave the ticker running, as in gc.
func (b *tickerChanBox) Reset(d time.Duration) {
	if d <= 0 {
		panic("non-positive interval for Ticker.Reset")
	}
	b.feed.reset(func() bool { b.t.Reset(d); return false })
}

// hold takes a new note if none is held. Call with no lock held.
func (t *afterFuncTimer) hold() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.release == nil {
		t.release = t.note()
	}
}

// drop releases the held note, if any. The release funcs are
// once-guarded, but clearing the field also stops a later Stop from
// double-counting a callback that already fired.
func (t *afterFuncTimer) drop() {
	t.mu.Lock()
	r := t.release
	t.release = nil
	t.mu.Unlock()
	if r != nil {
		r()
	}
}

// Stop cancels the timer like time.Timer.Stop: a true return means the
// callback will never run, so its wait note is released.
func (t *afterFuncTimer) Stop() bool {
	if !t.Timer.Stop() {
		return false
	}
	t.drop()
	return true
}

// Reset reschedules the timer like time.Timer.Reset: a callback the
// timer may now fire must hold a wait note again.
func (t *afterFuncTimer) Reset(d time.Duration) bool {
	active := t.Timer.Reset(d)
	t.hold()
	return active
}
