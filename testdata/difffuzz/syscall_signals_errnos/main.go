package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

var sigStuckProcess = syscall.SIGQUIT

func main() {
	fmt.Println(sigStuckProcess, syscall.SIGINT, syscall.SIGTERM)
	_, err := os.Open("/nonexistent/minigo")
	fmt.Println(errors.Is(err, syscall.ENOENT), syscall.ENOENT)
	fmt.Println(syscall.Getpid() > 0)
	fmt.Println(syscall.EINVAL, syscall.Errno(2), syscall.SIGUSR1)
	fmt.Println(errors.Is(err, syscall.Errno(2)))
}
