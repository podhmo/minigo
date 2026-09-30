.PHONY: all test format clean lint go-mod-tidy-all

all:
	go build ./...

format:
	go tool goimports -w $(shell find . -name '*.go')

lint:
	go tool staticcheck ./...

go-mod-tidy-all:
	for i in `find . -name go.mod | grep -v testdata | xargs dirname`; do pushd $$i; go mod tidy; popd; done

test:
	go test ./...
	go -C ./examples/task-run test ./...
	go -C ./examples/convert-define test ./...

clean:
	go clean -cache -testcache # General Go clean
	# Example-specific cleaning should be done within their respective Makefiles
	# or by explicitly calling make -C examples/<example_dir> clean
	@echo "Root clean done. For example-specific cleaning, cd into example dir and run make clean."
