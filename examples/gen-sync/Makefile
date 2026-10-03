.PHONY: test demo clean

test:
	go test -v ./...

# Run the sync twice to show idempotence; `git diff app/` shows the writes.
demo:
	go run ./
	go run ./
	git --no-pager diff ../gen-sync/app/

clean:
	git checkout -- app/
