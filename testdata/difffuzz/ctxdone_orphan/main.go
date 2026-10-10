package main

import (
	"context"
	"fmt"
)

func main() {
	ctx, _ := context.WithCancel(context.Background())
	fmt.Println("parked")
	<-ctx.Done() // the only CancelFunc is discarded — gc fatals here
}
