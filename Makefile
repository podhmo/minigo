test:
	go test -v ./...
.PHONY: test

plugin:
	go build -buildmode=plugin -o ./plugin/strings.so ./plugin/strings
.PHONY: plugin