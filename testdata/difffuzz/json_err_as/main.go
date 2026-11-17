package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

type T struct{}

func (T) MarshalJSON() ([]byte, error) { return nil, errors.New("boom") }

type U struct{}

func (U) MarshalText() ([]byte, error) { return nil, errors.New("texterr") }

func main() {
	_, err := json.Marshal(make(chan int))
	var ute *json.UnsupportedTypeError
	fmt.Println(errors.As(err, &ute), ute != nil)
	if u, ok := err.(*json.UnsupportedTypeError); ok {
		fmt.Println("assert ok", u != nil)
	}
	_, err2 := json.Marshal(T{})
	fmt.Println(err2)
	var me *json.MarshalerError
	fmt.Println(errors.As(err2, &me), me != nil)
	if m, ok := err2.(*json.MarshalerError); ok {
		fmt.Println("assert2", m.Unwrap())
	}
	_, err3 := json.Marshal(U{})
	fmt.Println(err3)
	var me3 *json.MarshalerError
	fmt.Println(errors.As(err3, &me3), me3 != nil)
}
