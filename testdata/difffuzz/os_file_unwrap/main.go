package main

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
)

func main() {
	f, _ := os.CreateTemp("", "x")
	defer os.Remove(f.Name())
	defer f.Close()
	cmd := exec.Command("true")
	cmd.ExtraFiles = []*os.File{f, os.Stdout}
	fmt.Println(len(cmd.ExtraFiles))
	fmt.Println(reflect.TypeOf(f) == reflect.TypeOf((*os.File)(nil)))
	fmt.Println(reflect.TypeOf(os.Stdout) == reflect.TypeOf((*os.File)(nil)))
}
