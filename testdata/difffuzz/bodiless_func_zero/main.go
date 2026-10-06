package main

import (
	"fmt"
	"math/big"
)

func main() {
	fmt.Println(new(big.Int).Add(big.NewInt(1), big.NewInt(2)))
}
