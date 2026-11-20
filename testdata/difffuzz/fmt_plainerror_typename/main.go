package main
import "fmt"
func main() {
	var m map[string]int
	defer func() {
		fmt.Printf("%T\n", recover())
	}()
	m["a"] = 1
}
