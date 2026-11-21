package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println(os.IsPathSeparator('/'), os.IsPathSeparator('x'), os.IsPathSeparator(os.PathSeparator))
}
