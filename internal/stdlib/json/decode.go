package stdjson

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// jsonDecoder backs the bound encoding/json.Decoder: the host decoder
// walks the buffered input while the script-arg Decode shapes one value
// through jsonInject, like json.Unmarshal's two-arg form. Decoding into
// a RawMessage captures the value's own byte span, which the DOM keeps
// per node so a nested UnmarshalJSON sees the original literal — and a
// Token+Decode mix lands on the host decoder's own mid-stream rules.
// The other members forward.
type jsonDecoder struct {
	dec *json.Decoder
	// num/strict mirror the UseNumber and DisallowUnknownFields
	// flags: they only steer how jsonInject shapes the DOM.
	num    bool
	strict bool
}

// ScriptMethod routes Decode around the reflect call so the target's
// script pointer stays raw (runtime.ScriptMethoder).
func (d *jsonDecoder) ScriptMethod(name string) (func(runtime.VMCaller, []runtime.Value) (runtime.Value, error), bool) {
	if name != "Decode" {
		return nil, false
	}
	return func(v runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("json.Decoder.Decode needs 1 arg, got %d", len(args))
		}
		if _, ok := runtime.Deref(args[0]); !ok {
			atd := v.TypeOf(args[0])
			if atd == nil {
				return stdlib.ErrVal(fmt.Errorf("json: Decode(nil)")), nil
			}
			return stdlib.ErrVal(fmt.Errorf("json: Decode(non-pointer %s)", runtime.DisplayName(atd))), nil
		}
		var raw json.RawMessage
		if err := d.dec.Decode(&raw); err != nil {
			return stdlib.ErrVal(err), nil
		}
		// the DOM is built with UseNumber semantics so jsonScalar
		// parses each number literal itself — the caller's UseNumber
		// only decides whether `any` targets keep json.Number.
		node, err := jsonDomParse(raw)
		if err != nil {
			return stdlib.ErrVal(err), nil
		}
		return jsonInject(v, raw, node, args[0], jsonDecFlags{Number: d.num, Strict: d.strict})
	}, true
}

// Decode serves host callers holding the boxed decoder with a native
// target; script calls take the raw-arg path (ScriptMethod).
func (d *jsonDecoder) Decode(v any) error {
	return d.dec.Decode(v)
}

func (d *jsonDecoder) UseNumber()                 { d.dec.UseNumber(); d.num = true }
func (d *jsonDecoder) DisallowUnknownFields()     { d.dec.DisallowUnknownFields(); d.strict = true }
func (d *jsonDecoder) More() bool                 { return d.dec.More() }
func (d *jsonDecoder) Buffered() io.Reader        { return d.dec.Buffered() }
func (d *jsonDecoder) InputOffset() int64         { return d.dec.InputOffset() }
func (d *jsonDecoder) Token() (json.Token, error) { return d.dec.Token() }
