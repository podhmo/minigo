package main

import (
	"fmt"
	"text/template/parse"
)

func main() { fmt.Println(parse.ParseComments | parse.SkipFuncCheck) }
