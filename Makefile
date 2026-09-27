GO       ?= $(shell test -x $(HOME)/sdk/go1.27.1/bin/go && echo $(HOME)/sdk/go1.27.1/bin/go || echo go)
BIN      := bin
SERVER   := $(BIN)/hollowmere
CLIENT   := $(BIN)/tapcli
WORLD    ?= data/world
HTTP     ?= 127.0.0.1:8080
TCP      ?= 127.0.0.1:4243
ADDR     ?= 127.0.0.1:4243
NAME     ?=
IMAGE    ?= hollowmere:dev

SOURCES  := $(shell find cmd internal -type f \( -name '*.go' -o -name '*.html' -o -name '*.css' -o -name '*.js' \)) go.mod go.sum

.PHONY: all install build run run-client lint test check-world demo demo-serve docker docker-run clean fclean re

all: build

## install: fetch dependencies
install:
	@$(GO) version
	$(GO) mod download
	$(GO) mod verify

## build: compile the server and the CLI client into ./bin
build: $(SERVER) $(CLIENT)

$(SERVER): $(SOURCES)
	$(GO) build -o $@ ./cmd/hollowmere

$(CLIENT): $(SOURCES)
	$(GO) build -o $@ ./cmd/tapcli

## run: start the server (web client on $(HTTP), TCP on $(TCP))
run: $(SERVER)
	./$(SERVER) -http $(HTTP) -tcp $(TCP) -world $(WORLD) -log-level debug

## run-client: start the CLI client
run-client: $(CLIENT)
	./$(CLIENT) -addr $(ADDR) $(if $(NAME),-name $(NAME))

## lint: formatting, vet and world validation
lint: check-world
	@unformatted="$$($(GO) fmt ./... )"; \
	if [ -n "$$unformatted" ]; then echo "gofmt rewrote:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./...
	@if command -v node >/dev/null 2>&1; then node --check internal/web/app.js; fi

check-world: $(SERVER)
	./$(SERVER) -check -world $(WORLD) -log-level error >/dev/null

## test: unit and integration tests with the race detector
test:
	$(GO) test -race -count=1 ./...

## demo: build the static WebAssembly demo into ./site (GitHub Pages)
demo:
	GO=$(GO) web-demo/build.sh site

## demo-serve: build the demo and serve it on http://127.0.0.1:8000
demo-serve: demo
	cd site && python3 -m http.server 8000 --bind 127.0.0.1

## docker: build the production image
docker:
	docker build -t $(IMAGE) .

## docker-run: run the production image locally
docker-run: docker
	docker run --rm -p 8080:8080 -p 4243:4243 $(IMAGE)

clean:
	$(GO) clean
	rm -f server.log

fclean: clean
	rm -rf $(BIN) site

re: fclean all
