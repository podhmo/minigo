package vm

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"go/token"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/podhmo/minigo/runtime"
)

// Profiler samples script call stacks: a clock goroutine advances a tick
// every period, and each VM checks the tick at its safepoints (function
// entry and loop back-edges). A VM that sees the tick moved records its
// current script stack, weighted by the ticks elapsed since its last
// check, so time spent inside a long host call lands on the script frame
// that made it. Time a goroutine spends parked (channel ops, select,
// host-call joins) is excluded — the VM resyncs its tick on unpark.
//
// The result is a pprof profile of script functions and lines; read it
// with `go tool pprof`.
type Profiler struct {
	period time.Duration
	tick   atomic.Uint64
	start  time.Time
	stop   chan struct{}
	done   chan struct{}

	mu      sync.Mutex
	samples map[string]*profSample
	// fnIDs gives each sampled function a small id for sample keys; it
	// lives and dies with the profiler.
	fnIDs map[*runtime.Function]uint64
	end   time.Time
}

type profSample struct {
	stack []profFrame // innermost first
	n     int64
}

type profFrame struct {
	fn  *runtime.Function
	pos token.Pos
}

// NewProfiler starts a profiler sampling every period (10ms if <= 0).
// Hand it to the VMs through Hooks.Prof, and call Stop when the run ends.
func NewProfiler(period time.Duration) *Profiler {
	if period <= 0 {
		period = 10 * time.Millisecond
	}
	p := &Profiler{
		period:  period,
		start:   time.Now(),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		samples: map[string]*profSample{},
		fnIDs:   map[*runtime.Function]uint64{},
	}
	p.tick.Store(1) // 0 marks a VM that has not checked in yet
	go p.clock()
	return p
}

func (p *Profiler) clock() {
	defer close(p.done)
	t := time.NewTicker(p.period)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.tick.Add(1)
		}
	}
}

// Stop ends sampling; it is idempotent.
func (p *Profiler) Stop() {
	p.mu.Lock()
	if p.end.IsZero() {
		p.end = time.Now()
		close(p.stop)
	}
	p.mu.Unlock()
	<-p.done
}

// profCheck is the safepoint hook: cheap when the tick has not moved.
func (v *VM) profCheck() {
	p := v.H.Prof
	t := p.tick.Load()
	last := v.profTick
	if t == last {
		return
	}
	v.profTick = t
	if last == 0 {
		return // first check-in: no interval to attribute yet
	}
	p.record(v.frames, int64(t-last))
}

// profResync drops the ticks a goroutine spent blocked in a parked op.
// Every channel op parks, even one that completes at once (a receive on
// a closed channel), and the op's own work is real run time — so only an
// op that spanned more than one tick counts as blocked. A real wait
// loses at most one tick of run time to this; a quick op loses none.
func (v *VM) profResync() {
	p := v.H.Prof
	if p == nil {
		return
	}
	if t := p.tick.Load(); t-v.profTick > 1 {
		v.profTick = t
	}
}

func (p *Profiler) record(frames []*frame, n int64) {
	stack := make([]profFrame, 0, len(frames))
	for i := len(frames) - 1; i >= 0; i-- {
		f := frames[i]
		if f.sentinel || f.fn == nil {
			continue
		}
		pf := profFrame{fn: f.fn, pos: f.pos()}
		if !pf.pos.IsValid() && f.fn.Decl != nil {
			pf.pos = f.fn.Decl.Pos() // sampled at entry, before any instruction
		}
		stack = append(stack, pf)
	}
	if len(stack) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := make([]byte, 0, 16*len(stack))
	for _, f := range stack {
		id, ok := p.fnIDs[f.fn]
		if !ok {
			id = uint64(len(p.fnIDs) + 1)
			p.fnIDs[f.fn] = id
		}
		key = binary.LittleEndian.AppendUint64(key, id)
		key = binary.LittleEndian.AppendUint64(key, uint64(f.pos))
	}
	if s := p.samples[string(key)]; s != nil {
		s.n += n
	} else {
		p.samples[string(key)] = &profSample{stack: stack, n: n}
	}
}

// WriteTo writes the samples as a gzipped pprof profile with sample
// types samples/count and time/nanoseconds. Call it after Stop.
func (p *Profiler) WriteTo(w io.Writer) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	end := p.end
	if end.IsZero() {
		end = time.Now()
	}
	b := newProfBuilder()
	b.valueType(1, "samples", "count")
	b.valueType(1, "time", "nanoseconds")
	for _, s := range p.samples {
		locs := make([]uint64, len(s.stack))
		for i, f := range s.stack {
			locs[i] = b.location(f)
		}
		b.sample(locs, s.n, s.n*p.period.Nanoseconds())
	}
	b.flushTables()
	b.int(9, p.start.UnixNano())
	b.int(10, end.Sub(p.start).Nanoseconds())
	b.valueType(11, "wall", "nanoseconds")
	b.int(12, p.period.Nanoseconds())
	b.strings()

	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(b.buf.Bytes()); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return out.WriteTo(w)
}

// profBuilder hand-encodes the pprof protobuf (profile.proto): the
// format is small enough that a dependency is not worth it.
type profBuilder struct {
	buf     bytes.Buffer
	strs    []string
	strIdx  map[string]int64
	funcs   map[[2]string]uint64 // name, file -> id
	locs    map[[3]string]uint64 // name, file, line -> id
	funcBuf bytes.Buffer
	locBuf  bytes.Buffer
}

func newProfBuilder() *profBuilder {
	return &profBuilder{
		strs:   []string{""},
		strIdx: map[string]int64{"": 0},
		funcs:  map[[2]string]uint64{},
		locs:   map[[3]string]uint64{},
	}
}

func (b *profBuilder) str(s string) int64 {
	if i, ok := b.strIdx[s]; ok {
		return i
	}
	i := int64(len(b.strs))
	b.strs = append(b.strs, s)
	b.strIdx[s] = i
	return i
}

func (b *profBuilder) location(f profFrame) uint64 {
	name := profFuncName(f.fn)
	file, line := "", 0
	if f.fn.Pkg != nil && f.fn.Pkg.Fset != nil && f.pos.IsValid() {
		p := f.fn.Pkg.Fset.Position(f.pos)
		file, line = p.Filename, p.Line
	}
	lk := [3]string{name, file, strconv.Itoa(line)}
	if id, ok := b.locs[lk]; ok {
		return id
	}
	fk := [2]string{name, file}
	fid, ok := b.funcs[fk]
	if !ok {
		fid = uint64(len(b.funcs) + 1)
		b.funcs[fk] = fid
		var m bytes.Buffer
		protoUint(&m, 1, fid)
		protoUint(&m, 2, uint64(b.str(name)))
		protoUint(&m, 3, uint64(b.str(name)))
		protoUint(&m, 4, uint64(b.str(file)))
		protoBytes(&b.funcBuf, 5, m.Bytes())
	}
	id := uint64(len(b.locs) + 1)
	b.locs[lk] = id
	var ln bytes.Buffer
	protoUint(&ln, 1, fid)
	protoUint(&ln, 2, uint64(line))
	var m bytes.Buffer
	protoUint(&m, 1, id)
	protoBytes(&m, 4, ln.Bytes())
	protoBytes(&b.locBuf, 4, m.Bytes())
	return id
}

func (b *profBuilder) valueType(field int, typ, unit string) {
	var m bytes.Buffer
	protoUint(&m, 1, uint64(b.str(typ)))
	protoUint(&m, 2, uint64(b.str(unit)))
	protoBytes(&b.buf, field, m.Bytes())
}

func (b *profBuilder) sample(locs []uint64, vals ...int64) {
	var m, packed bytes.Buffer
	for _, l := range locs {
		packed.Write(binary.AppendUvarint(nil, l))
	}
	protoBytes(&m, 1, packed.Bytes())
	packed.Reset()
	for _, v := range vals {
		packed.Write(binary.AppendUvarint(nil, uint64(v)))
	}
	protoBytes(&m, 2, packed.Bytes())
	protoBytes(&b.buf, 2, m.Bytes())
}

func (b *profBuilder) flushTables() {
	b.buf.Write(b.locBuf.Bytes())
	b.buf.Write(b.funcBuf.Bytes())
}

func (b *profBuilder) int(field int, v int64) { protoUint(&b.buf, field, uint64(v)) }

func (b *profBuilder) strings() {
	for _, s := range b.strs {
		protoBytes(&b.buf, 6, []byte(s))
	}
}

func protoUint(w *bytes.Buffer, field int, v uint64) {
	w.Write(binary.AppendUvarint(nil, uint64(field)<<3))
	w.Write(binary.AppendUvarint(nil, v))
}

func protoBytes(w *bytes.Buffer, field int, data []byte) {
	w.Write(binary.AppendUvarint(nil, uint64(field)<<3|2))
	w.Write(binary.AppendUvarint(nil, uint64(len(data))))
	w.Write(data)
}

// profFuncName qualifies a script function the way Go's pprof names
// functions: `main.fib`, `example.com/p.(*T).M`, `main.distance.func1`.
func profFuncName(fn *runtime.Function) string {
	name := fn.Name
	if fn.Pkg != nil {
		// closures are named `<pkg>.<outer>.funcN`; requalify below
		name = strings.TrimPrefix(name, fn.Pkg.Name+".")
	}
	if fn.Recv != "" {
		recv := fn.Recv
		if fn.PtrRecv {
			recv = "(*" + recv + ")"
		}
		name = recv + "." + strings.TrimPrefix(name, fn.Recv+".")
	}
	if fn.Pkg == nil {
		return name
	}
	if fn.Pkg.Name == "main" {
		return "main." + name
	}
	if fn.Pkg.Path != "" {
		return fn.Pkg.Path + "." + name
	}
	return name
}
