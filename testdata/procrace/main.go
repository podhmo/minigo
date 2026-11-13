package main

import "context"

// CancelThenReturn cancels a context with a pending context.AfterFunc
// and returns at once: the host runs the callback on its own goroutine,
// so its Call into this run's VM lands right as the run ends and
// releases its process.
func CancelThenReturn() string {
	ctx, cancel := context.WithCancel(context.Background())
	context.AfterFunc(ctx, func() {})
	cancel()
	return "ok"
}

func main() {}
