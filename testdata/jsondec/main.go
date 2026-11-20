package main

import (
	"bytes"
	"encoding/json"
)

type Raw struct {
	Payload json.RawMessage `json:"payload"`
}

type T struct {
	X int `json:"x"`
}

func (t T) Tag() string { return "t" }

type TBis T

func (b TBis) Tag() string { return "bis" }

type Lit string

type F32 float32

func (l *Lit) UnmarshalJSON(b []byte) error { *l = Lit(string(b)); return nil }

func main() string {
	// bound NewDecoder streams one value off the reader.
	dec := json.NewDecoder(bytes.NewReader([]byte(`{"a":1} {"b":2}`)))
	var m1, m2 map[string]int
	if err := dec.Decode(&m1); err != nil {
		return "err1:" + err.Error()
	}
	if err := dec.Decode(&m2); err != nil {
		return "err2:" + err.Error()
	}
	// RawMessage keeps the literal bytes.
	var r Raw
	if err := json.Unmarshal([]byte(`{"payload":{"x":[1,2]}}`), &r); err != nil {
		return "err3:" + err.Error()
	}
	b, err := json.Marshal(r.Payload)
	if err != nil {
		return "err4:" + err.Error()
	}
	// DisallowUnknownFields rejects keys no field claims while the
	// known fields still land.
	strict := json.NewDecoder(bytes.NewReader([]byte(`{"a":1,"zzz":2,"b":"x"}`)))
	strict.DisallowUnknownFields()
	var s struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	serr := strict.Decode(&s)
	if serr == nil {
		return "err5:no error"
	}
	// int64 literals keep their low bits past 2^53.
	var big int64
	if err := json.Unmarshal([]byte(`9007199254740993`), &big); err != nil {
		return "err6:" + err.Error()
	}
	// a named-delegated decode keeps the declared type's methods.
	var tb TBis
	if err := json.Unmarshal([]byte(`{"x":1}`), &tb); err != nil {
		return "err7:" + err.Error()
	}
	// a nested UnmarshalJSON receives the member's original literal.
	var sp struct {
		K Lit `json:"k"`
	}
	if err := json.Unmarshal([]byte(`{"k":  1e2 }`), &sp); err != nil {
		return "err8:" + err.Error()
	}
	// DisallowUnknownFields checks every duplicate-key occurrence
	// while both occurrences still decode.
	strict2 := json.NewDecoder(bytes.NewReader([]byte(`{"o":{"bad":1},"o":{"a":7}}`)))
	strict2.DisallowUnknownFields()
	var s2 struct {
		O struct {
			A int `json:"a"`
		} `json:"o"`
	}
	serr2 := strict2.Decode(&s2)
	if serr2 == nil {
		return "err9:no error"
	}
	// a duplicate known key's later value wins.
	var dup struct {
		A int `json:"a"`
	}
	if err := json.Unmarshal([]byte(`{"a":1,"a":2}`), &dup); err != nil {
		return "err10:" + err.Error()
	}
	// a decoded uint64 keeps its unsigned tag through Marshal.
	var u64 struct {
		U uint64 `json:"u"`
	}
	if err := json.Unmarshal([]byte(`{"u":18446744073709551615}`), &u64); err != nil {
		return "err11:" + err.Error()
	}
	ub, err := json.Marshal(u64)
	if err != nil {
		return "err12:" + err.Error()
	}
	// a literal float64 cannot hold fails the decode but still stores
	// the strconv result, like gc's literalStore.
	var av any
	aerr := json.Unmarshal([]byte(`1e309`), &av)
	if aerr == nil {
		return "err13:no error"
	}
	avf, _ := av.(float64)
	inf := "nan"
	if avf > 1e308 {
		inf = "inf"
	}
	// a duplicate `any` field decodes through the declared type —
	// the later literal replaces the earlier tree.
	var dv struct {
		A any `json:"a"`
	}
	if err := json.Unmarshal([]byte(`{"a":{"x":1},"a":"s"}`), &dv); err != nil {
		return "err14:" + err.Error()
	}
	dvs, _ := dv.A.(string)
	// a named float32 field rejects a literal past MaxFloat32.
	var fv struct {
		F F32 `json:"f"`
	}
	ferr := json.Unmarshal([]byte(`{"f":1e300}`), &fv)
	if ferr == nil {
		return "err15:no error"
	}
	// two occurrences of one unknown key take positions in input
	// order — the middle type error must not jump the queue.
	strict3 := json.NewDecoder(bytes.NewReader([]byte(`{"o":{"bad":1,"a":"x"},"o":{"bad":1}}`)))
	strict3.DisallowUnknownFields()
	var s3 struct {
		O struct {
			A int `json:"a"`
		} `json:"o"`
	}
	serr3 := strict3.Decode(&s3)
	if serr3 == nil {
		return "err16:no error"
	}
	return string(rune('0'+m1["a"])) + string(rune('0'+m2["b"])) + "|" + string(b) +
		"|" + serr.Error() + "|" + itoa(big) + "|" + tb.Tag() +
		"|" + string(sp.K) + "|" + serr2.Error() + "|" + itoa(int64(dup.A)) + "|" + itoa(int64(s2.O.A)) +
		"|" + string(ub) + "|" + aerr.Error() + "|" + inf + "|" + dvs + "|" + ferr.Error() + "|" + serr3.Error()
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	var buf [24]byte
	p := len(buf)
	neg := i < 0
	for i != 0 {
		p--
		d := i % 10
		if neg {
			d = -d
		}
		buf[p] = byte('0' + d)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}
