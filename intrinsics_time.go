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
	vc   runtime.VMCaller
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
func newChanFeed(vc runtime.VMCaller, src <-chan time.Time, once bool) *chanFeed {
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
