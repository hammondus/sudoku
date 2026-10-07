# sudoku — Go server + vanilla-JS PWA in web/. Deploys as a container behind
# Nginx Proxy Manager, which terminates TLS; the container serves plain HTTP.

BINARY  := sudoku
SERVICE := sudoku

# Pure Go (modernc.org/sqlite), so CGO is off everywhere and every binary is
# static. -trimpath keeps local paths out of the binary; -s -w strips symbols.
GOFLAGS := -trimpath
LDFLAGS := -s -w

DEV_DB := /tmp/sudoku-dev.db

.PHONY: build test run release clean docker-build deploy logs invite

## build: compile the server for this machine (web/ embedded)
build:
	go build $(GOFLAGS) -o $(BINARY) .

## test: vet + tests
test:
	go vet ./...
	go test ./...

## run: dev server on :8080 — serves web/ from disk, codes logged not emailed
# -dev also drops the Secure flag on the session cookie, because localhost is
# plain HTTP. Invite yourself first: make invite EMAIL=you@example.com
run: build
	./$(BINARY) -dev -db $(DEV_DB)

## invite: add an account to the dev database (EMAIL=...)
invite: build
	./$(BINARY) -db $(DEV_DB) -invite "$(EMAIL)"

## release: stripped static binary for the deploy target, linux/arm64
# The image builds its own binary on the deploy host; this is for running the
# server without Docker, or for checking that the cross-compile is clean.
release:
	rm -rf dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 .

## clean: remove build output
clean:
	rm -rf $(BINARY) dist

## docker-build: check that the image builds
docker-build:
	docker compose build

## deploy: on the server — pull, rebuild, restart
deploy:
	git pull
	docker compose up -d --build

## logs: follow the server's logs
logs:
	docker compose logs -f $(SERVICE)
