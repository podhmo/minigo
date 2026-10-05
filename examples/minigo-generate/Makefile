.PHONY: test demo clean

test:
	go test -v ./...

# Run the generator twice with echo on: the second run regenerates the
# same bytes. Generated files are untracked — `make clean` removes them.
demo:
	go run ./ -x ./app
	go run ./ -x ./app

clean:
	rm -f app/*_string.go
