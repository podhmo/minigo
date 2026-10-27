package main

import (
	"fmt"
	"io/fs"
	"log/slog"
	"reflect"
	"time"
)

// reflect over a bound host scalar: a time.Duration conversion, typed
// constant, var or host-produced value (time.Second, 2*time.Second)
// reads its Int, Type, Kind, IsZero and DeepEqual like gc, next to
// time.Month, time.Weekday, fs.FileMode and slog.Level.

const c = time.Second
const cd time.Duration = 5

type S struct {
	D time.Duration
	M time.Month
}

func main() {
	var d time.Duration = 5
	var m time.Month = 3
	for _, v := range []any{time.Duration(5), d, c, cd, 2 * time.Second, time.March, m, time.Friday, fs.FileMode(0644), slog.LevelWarn, slog.Level(4)} {
		rv := reflect.ValueOf(v)
		fmt.Print(rv.Kind(), " ", rv.Type(), " ")
		if rv.CanInt() {
			fmt.Print(rv.Int(), " ", rv.Convert(reflect.TypeOf(int64(0))).Interface())
		} else {
			fmt.Print(rv.Uint(), " ", rv.Convert(reflect.TypeOf(uint64(0))).Interface())
		}
		fmt.Println(" ", rv.Interface(), rv.String(), rv.IsZero())
	}
	fmt.Println(reflect.ValueOf(time.Duration(5)).Int(), reflect.ValueOf(c).Int(), reflect.ValueOf(d).Int(), reflect.ValueOf(time.March).Int())
	fmt.Println(reflect.TypeOf(c) == reflect.TypeOf(d), reflect.TypeOf(time.Second) == reflect.TypeOf(time.Duration(0)))
	fmt.Println(reflect.TypeOf(time.March) == reflect.TypeOf(time.Month(1)), reflect.TypeOf(c).Kind(), reflect.TypeOf(c).String())
	fmt.Println(reflect.ValueOf(c).Interface() == any(time.Second), reflect.ValueOf(c).Interface().(time.Duration).Seconds())
	fmt.Println(reflect.ValueOf(c).Convert(reflect.TypeOf(int64(0))).Int(), reflect.ValueOf(int64(7)).Convert(reflect.TypeOf(c)).Interface())
	s := S{D: time.Duration(5), M: time.May}
	rv := reflect.ValueOf(&s).Elem()
	fmt.Println(rv.Field(0).Int(), rv.Field(1).Int(), rv.Field(0).Interface(), rv.Field(0).Type())
	rv.Field(0).SetInt(42)
	rv.Field(1).Set(reflect.ValueOf(time.June))
	fmt.Println(s.D, s.M)
	rv.Field(0).Set(reflect.ValueOf(c))
	fmt.Println(s.D)
	pd := &d
	reflect.ValueOf(pd).Elem().SetInt(11)
	fmt.Println(d, reflect.ValueOf(pd).Elem().Int())
	fmt.Printf("%v %v\n", reflect.ValueOf(c), reflect.ValueOf(time.Duration(5)))
	fmt.Println(reflect.ValueOf(c).IsZero(), reflect.ValueOf(time.Duration(0)).IsZero(), reflect.ValueOf(time.Month(0)).IsZero())
	fmt.Println(reflect.DeepEqual(c, time.Duration(1e9)), reflect.DeepEqual(time.Duration(1e9), c), reflect.DeepEqual(cd, time.Duration(5)))
	fmt.Println(reflect.DeepEqual(c, time.Duration(2)), reflect.DeepEqual(time.March, time.Month(3)), reflect.DeepEqual(time.March, 3))
	fmt.Println(reflect.DeepEqual([]time.Duration{c, cd}, []time.Duration{time.Duration(1e9), 5}), reflect.DeepEqual(S{D: c}, S{D: 1e9}))
}
