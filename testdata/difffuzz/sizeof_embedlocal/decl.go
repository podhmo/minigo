package main

type Inner struct{ Arr [4]int64 }

type Outer struct{ Inner }

var o Outer
