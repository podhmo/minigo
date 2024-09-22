MINIGO := ./

default:\
 hello\
 another-entrypoint\
 use-stdlib

hello:
	go run $(MINIGO) -- ./internal/e2e/testdata/hello.go
.PHONY: hello

another-entrypoint:
	go run $(MINIGO) -entrypoint Foo ./internal/e2e/testdata/another-entrypoint.go
	go run $(MINIGO) -entrypoint Bar ./internal/e2e/testdata/another-entrypoint.go
.PHONY: another-entrypoint

use-stdlib:
	go run $(MINIGO) ./internal/e2e/testdata/use-stdlib.go
.PHONY: use-stdlib
