BINARY  := podcast-agent
CMD     := ./cmd/$(BINARY)
BIN_DIR := bin
IMAGE   := $(BINARY)
TAG     ?= latest

.PHONY: all build test run docker clean

all: build

build:
	go build -o $(BIN_DIR)/$(BINARY) $(CMD)

test:
	go test ./...

run:
	go run $(CMD)

docker:
	docker build -t $(IMAGE):$(TAG) .

clean:
	rm -rf $(BIN_DIR)
