.PHONY: help render native-html preview list export test

PREVIEW_ADDR ?= 127.0.0.1:6006
PKG ?= ./app
STORY ?= StoryUser
OUT ?= /tmp/minigo-storybook-out

help:
	@printf '%s\n' \
		'Direct minigo use (sufficient for inspecting one HTML page):' \
		'make render      Run one Story directly with minigo (STORY=StoryUser)' \
		'  Save HTML: make render STORY=StoryUser > /tmp/story-user.html' \
		'' \
		'Optional helper (discovery, snapshots, or repeated browser reloads):' \
		'make preview     Optional browser helper (PREVIEW_ADDR=127.0.0.1:6006)' \
		'make list        List stories (PKG=./app)' \
		'make export      Export stories and a browser index (OUT=/tmp/minigo-storybook-out)' \
		'  export saves a snapshot; preview re-renders on each request.' \
		'' \
		'Comparison and verification (not required for minigo previewing):' \
		'make native-html Save the same Stories with Go tests (OUT=/tmp/minigo-storybook-out)' \
		'make test        Check HTTP behavior, direct CLI output, and native output'

# Direct minigo use: save stdout as HTML and open it in a browser.
# Keep stdout suitable for redirection to an HTML file. Resolve PKG before -C.
render:
	@go -C ../../cmd/minigo run ./ run --tags story "$(abspath $(PKG))" "$(STORY)"

# Optional helper: discovery, saved snapshots, and a server for editing loops.
preview:
	go -C ./cmd/storybook run ./ -http "$(PREVIEW_ADDR)" "$(abspath $(PKG))"

list:
	go -C ./cmd/storybook run ./ -list "$(abspath $(PKG))"

export:
	go -C ./cmd/storybook run ./ -o "$(abspath $(OUT))" "$(abspath $(PKG))"

# Comparison and verification: neither target is needed for direct minigo use.
native-html:
	go test -tags story ./app -run '^TestStories$$' -count=1 -args -html-dir "$(abspath $(OUT))"

test:
	go test ./...
	go test -tags story ./...
