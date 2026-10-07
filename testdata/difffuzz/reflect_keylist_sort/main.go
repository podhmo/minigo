package main

import (
	"fmt"
	"reflect"
	"sort"
)

type keyList []reflect.Value

func (l keyList) Len() int           { return len(l) }
func (l keyList) Swap(i, j int)      { l[i], l[j] = l[j], l[i] }
func (l keyList) Less(i, j int) bool { return l[i].String() < l[j].String() }

func main() {
	m := map[string]int{"b": 2, "a": 1, "c": 3}
	keys := keyList(reflect.ValueOf(m).MapKeys())
	sort.Sort(keys)
	for _, k := range keys {
		fmt.Println(k.String(), reflect.ValueOf(m).MapIndex(k).Int())
	}
	vs := []reflect.Value{reflect.ValueOf(1)}
	kl := keyList(vs)
	fmt.Println(len(kl), kl[0].Int())
}
