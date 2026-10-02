.PHONY: test clean e2e update-golden check-toolchains

test:
	go test -v ./...

# Verifies the define package's build-tag split on both sides: go1.27.0
# selects the generic methods in methods_go127.go, go1.26.0 the `any`
# variants in methods_pre127.go. `go vet -tags codegen` additionally
# type-checks the DSL file's calls against whichever variant was selected.
# Both toolchains are pinned explicitly (GOTOOLCHAIN values need a patch
# version) so the check does not depend on the ambient `go version`;
# each is auto-downloaded on first run.
check-toolchains:
	GOTOOLCHAIN=go1.27.0 go build ./define
	GOTOOLCHAIN=go1.27.0 go vet -tags codegen ./e2e_test
	GOTOOLCHAIN=go1.26.0 go build ./define
	GOTOOLCHAIN=go1.26.0 go vet -tags codegen ./e2e_test

# The 'e2e' and 'test' targets are separate because in CI,
# the generation step (e2e) and the testing step (test) are run independently.
# The 'update-golden' target is for local development to update testdata.
update-golden:
	go test -v . -update

clean:
	rm -rf e2e_test

e2e:
	go run . -file ./e2e_test/mappings.go -output ./e2e_test/generated.go --tags e2e
	go test -v -tags=e2e ./e2e_test/...
