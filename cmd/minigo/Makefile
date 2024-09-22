MINIGO ?= /tmp/gobin/minigo

default:\
 hello\
 another-entrypoint\
 use-stdlib

hello: $(MINIGO)
	$(MINIGO) -- ../../internal/interpreter/testdata/hello.go
.PHONY: hello

another-entrypoint: $(MINIGO)
	$(MINIGO) -entrypoint Foo ../../internal/interpreter/testdata/another-entrypoint.go
	$(MINIGO) -entrypoint Bar ../../internal/interpreter/testdata/another-entrypoint.go
.PHONY: another-entrypoint

use-stdlib: $(MINIGO)
	$(MINIGO) ../../internal/interpreter/testdata/use-stdlib.go
.PHONY: use-stdlib

$(MINIGO): main.go
	mkdir -p $(dir $(MINIGO))
	go build -o $(MINIGO) .
