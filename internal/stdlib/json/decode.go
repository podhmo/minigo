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
// through jsonInject, like json.Unmarshal's two-arg form. The literal
// span InputOffset brackets feeds both the top-level UnmarshalJSON
// probe and error-position collation. The other members forward.
type jsonDecoder struct {
	dec *json.Decoder
	buf []byte
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
		start := d.dec.InputOffset()
		var dec any
		if err := d.dec.Decode(&dec); err != nil {
			return stdlib.ErrVal(err), nil
		}
		return jsonInject(v, d.buf[start:d.dec.InputOffset()], dec, args[0])
	}, true
}

// Decode serves host callers holding the boxed decoder with a native
// target; script calls take the raw-arg path (ScriptMethod).
func (d *jsonDecoder) Decode(v any) error {
	return d.dec.Decode(v)
}

func (d *jsonDecoder) UseNumber()                 { d.dec.UseNumber() }
func (d *jsonDecoder) DisallowUnknownFields()     { d.dec.DisallowUnknownFields() }
func (d *jsonDecoder) More() bool                 { return d.dec.More() }
func (d *jsonDecoder) Buffered() io.Reader        { return d.dec.Buffered() }
func (d *jsonDecoder) InputOffset() int64         { return d.dec.InputOffset() }
func (d *jsonDecoder) Token() (json.Token, error) { return d.dec.Token() }
