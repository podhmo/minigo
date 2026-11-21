package main

import (
	"fmt"
	"io"
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
	// an interface-typed field keeps its declared type — the stored
	// facade's API type must not leak through FieldByName.Type.
	cmd.Stdout = os.Stdout
	fv := reflect.ValueOf(cmd).Elem().FieldByName("Stdout")
	fmt.Println(fv.Type() == reflect.TypeOf((*io.Writer)(nil)).Elem())
}
