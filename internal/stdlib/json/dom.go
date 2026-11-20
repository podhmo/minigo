package stdjson

import (
	"bytes"
	"encoding/json"
)

// jsonDom is one decoded JSON value plus the input byte span it was
// read from. Objects keep their members in input order (jsonMember
// pairs) so duplicate keys each decode in turn like gc, and a nested
// UnmarshalJSON receives the ORIGINAL literal — whitespace, number
// spelling, duplicate keys — instead of a re-encoded DOM, matching
// gc's indirect() which hands the method the input span.
type jsonDom struct {
	raw []byte
	val any // nil (null), bool, string, json.Number, []jsonMember, []*jsonDom
}

// jsonMember is one `"k": v` pair of a DOM object, in input order.
type jsonMember struct {
	key  string
	node *jsonDom
}

// domVal unwraps a node's decoded value; a nil node (a synthesized
// "no value" position) unwraps to nil like the null literal.
func domVal(n *jsonDom) any {
	if n == nil {
		return nil
	}
	return n.val
}

// jsonDomParse reads data into the span-carrying DOM. The outer
// Unmarshal-into-RawMessage is gc's checkValid pass: a syntax error
// reports before any UnmarshalJSON dispatch.
func jsonDomParse(data []byte) (*jsonDom, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return jsonDomNode(raw)
}

// jsonDomNode parses one raw value into a node. Members and elements
// are decoded as RawMessage so each child keeps its own span; the
// token stream reads numbers as json.Number so scalar conversion sees
// the literal itself.
func jsonDomNode(raw []byte) (*jsonDom, error) {
	n := &jsonDom{raw: raw}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if dm, ok := t.(json.Delim); ok {
		if dm == '{' {
			var ms []jsonMember
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				var r json.RawMessage
				if err := d.Decode(&r); err != nil {
					return nil, err
				}
				cn, err := jsonDomNode(r)
				if err != nil {
					return nil, err
				}
				ms = append(ms, jsonMember{key: kt.(string), node: cn})
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			n.val = ms
			return n, nil
		}
		var a []*jsonDom
		for d.More() {
			var r json.RawMessage
			if err := d.Decode(&r); err != nil {
				return nil, err
			}
			cn, err := jsonDomNode(r)
			if err != nil {
				return nil, err
			}
			a = append(a, cn)
		}
		if _, err := d.Token(); err != nil {
			return nil, err
		}
		n.val = a
		return n, nil
	}
	// scalars arrive as bool/string/json.Number; the null literal
	// arrives as nil.
	n.val = t
	return n, nil
}

// jsonDomDeep unwraps the DOM into the plain tree `any` targets keep:
// members collapse last-wins into a map like gc's untyped decode, and
// json.Number stays only when the caller asked for UseNumber.
func jsonDomDeep(n *jsonDom, keepNum bool) any {
	if n == nil {
		return nil
	}
	switch x := n.val.(type) {
	case []jsonMember:
		out := make(map[any]any, len(x))
		for _, m := range x {
			out[m.key] = jsonDomDeep(m.node, keepNum)
		}
		return out
	case []*jsonDom:
		out := make([]any, len(x))
		for i, c := range x {
			out[i] = jsonDomDeep(c, keepNum)
		}
		return out
	case json.Number:
		if keepNum {
			return x
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	default:
		return x
	}
}
