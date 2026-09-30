.PHONY: test clean demo install

test:
	go test -v ./...

# Install a standalone binary into $(go env GOPATH)/bin so `task-run`
# runs from anywhere with zero rebuild — no per-invocation `go run`.
install:
	go install

demo:
	go run ./ -f testdata/Taskfile.go -l
	go run ./ -f testdata/Taskfile.go Default

# Same demo via the installed binary — Taskfile-relative paths work from
# any cwd, so run it from /tmp to prove the point.
demo-installed: install
	cd /tmp && $(shell go env GOPATH)/bin/task-run -f $(CURDIR)/testdata/Taskfile.go -l
	cd /tmp && $(shell go env GOPATH)/bin/task-run -f $(CURDIR)/testdata/Taskfile.go Default

clean:
	rm -f testdata/app.out
