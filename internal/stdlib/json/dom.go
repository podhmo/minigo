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
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	return jsonDomScan(d, raw)
}

// jsonDomScan consumes one value off the shared decoder and wraps it
// in a node whose raw is the value's own slice of buf — every node
// aliases the input buffer, so a deep document costs its input size,
// not a copy per level. Token/Decode mixing is the host decoder's
// business: members and elements read via the token stream, numbers
// arrive as json.Number so scalar conversion sees the literal itself.
func jsonDomScan(d *json.Decoder, buf []byte) (*jsonDom, error) {
	// the value's span starts at its first byte: after an object key
	// the offset sits on ':', after an array element it sits on ','
	// — skip the separator and any whitespace.
	start := d.InputOffset()
	for start < int64(len(buf)) && (buf[start] == ':' || buf[start] == ',' || buf[start] == ' ' || buf[start] == '\t' || buf[start] == '\n' || buf[start] == '\r') {
		start++
	}
	n := &jsonDom{}
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
				cn, err := jsonDomScan(d, buf)
				if err != nil {
					return nil, err
				}
				ms = append(ms, jsonMember{key: kt.(string), node: cn})
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			n.val = ms
		} else {
			var a []*jsonDom
			for d.More() {
				cn, err := jsonDomScan(d, buf)
				if err != nil {
					return nil, err
				}
				a = append(a, cn)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			n.val = a
		}
	} else {
		// scalars arrive as bool/string/json.Number; the null literal
		// arrives as nil.
		n.val = t
	}
	end := d.InputOffset()
	if start < end && end <= int64(len(buf)) {
		n.raw = buf[start:end]
	} else {
		n.raw = buf
	}
	return n, nil
}
