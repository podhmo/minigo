.PHONY: all test format clean lint go-mod-tidy-all difffuzz difffuzz-corpus tmpltests

all:
	go build ./...

format:
	go tool goimports -w . # walks .go files recursively; `find` is not portable (Windows System32\find.exe shadows GNU find)

lint:
	go vet ./...
	go tool staticcheck ./...

go-mod-tidy-all:
	for i in `git ls-files | grep 'go\.mod$$' | grep -v testdata | xargs dirname`; do pushd $$i; go mod tidy; popd; done

test:
	go test ./...
	go -C ./examples/task-run test ./...
	go -C ./examples/convert-define test ./...
	go -C ./examples/gen-sync test ./...
	go -C ./examples/test-detect test ./...
	go -C ./examples/minigo-generate test ./...

# differential harness vs the go toolchain (see tools/difffuzz/README.md).
# override e.g. `make difffuzz DIFFFUZZ_ARGS="-domain num -seed 1"`
DIFFFUZZ_ARGS ?= -domain text -batches 16
difffuzz:
	go -C ./tools/difffuzz run ./ gen $(DIFFFUZZ_ARGS)

difffuzz-corpus:
	go -C ./tools/difffuzz run ./ corpus -goroot-tests $(DIFFFUZZ_CORPUS_ARGS)

# verbatim upstream tests under --src (see tools/tmpltests/README.md).
# override e.g. `make tmpltests TMPLTESTS_ARGS="-only TestExec"`
TMPLTESTS_ARGS ?=
tmpltests:
	go -C ./tools/tmpltests run ./ $(TMPLTESTS_ARGS)

clean:
	go clean -cache -testcache # General Go clean
	# Example-specific cleaning should be done within their respective Makefiles
	# or by explicitly calling make -C examples/<example_dir> clean
	@echo "Root clean done. For example-specific cleaning, cd into example dir and run make clean."
