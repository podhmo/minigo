.PHONY: help run preview list render export test

ADDR ?= 127.0.0.1:8080
PREVIEW_ADDR ?= 127.0.0.1:6006
PKG ?= ./app
STORY ?= StoryUser
OUT ?= /tmp/minigo-storybook-out

help:
	@printf '%s\n' \
		'make run      Start the ordinary Go server (ADDR=127.0.0.1:8080)' \
		'make preview  Start the minigo preview (PREVIEW_ADDR=127.0.0.1:6006)' \
		'make list     List stories (PKG=./app)' \
		'make render   Print one story as HTML (STORY=StoryUser)' \
		'make export   Export all stories (OUT=/tmp/minigo-storybook-out)' \
		'make test     Check application routes, minigo previews, and native stories'

run:
	go -C ./cmd/server run ./ -addr "$(ADDR)"

preview:
	# Resolve paths before go -C changes the working directory.
	go -C ./cmd/storybook run ./ -http "$(PREVIEW_ADDR)" "$(abspath $(PKG))"

list:
	go -C ./cmd/storybook run ./ -list "$(abspath $(PKG))"

render:
	go -C ./cmd/storybook run ./ "$(abspath $(PKG))" "$(STORY)"

export:
	go -C ./cmd/storybook run ./ -o "$(abspath $(OUT))" "$(abspath $(PKG))"

test:
	go test ./...
	go test -tags story ./...
