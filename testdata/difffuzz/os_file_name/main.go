package main

import (
	"fmt"
	"os"
)

func main() {
	d, _ := os.MkdirTemp("", "osptest")
	defer os.RemoveAll(d)
	os.Chdir(d)
	os.Mkdir("sub", 0o755)
	sub, err := os.MkdirTemp("sub", "x")
	fmt.Println(len(sub) > 4 && sub[:4] == "sub/", err)
	f, _ := os.Create("f.txt")
	fmt.Println(f.Name())
	f.WriteString("hi")
	f.Close()
	f2, _ := os.Open("f.txt")
	fmt.Println(f2.Name())
	f2.Close()
	_, rerr := f2.Read(make([]byte, 1))
	fmt.Println(rerr)
	ct, _ := os.CreateTemp("sub", "y")
	fmt.Println(len(ct.Name()) > 4 && ct.Name()[:4] == "sub/")
	ct.Close()
	var w any = os.Stdout
	_, isFile := w.(*os.File)
	fmt.Println(isFile)
}
