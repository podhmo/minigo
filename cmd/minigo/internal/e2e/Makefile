MINIGO := ../../

default: hello another-entrypoint

hello:
	go run $(MINIGO) -- testdata/hello.go
.PHONY: hello

another-entrypoint:
	go run $(MINIGO) -entrypoint Foo testdata/another-entrypoint.go
	go run $(MINIGO) -entrypoint Bar testdata/another-entrypoint.go
.PHONY: another-entrypoint
