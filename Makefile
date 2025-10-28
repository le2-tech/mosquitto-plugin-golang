#SHELL := /bin/bash
BINARY_DIR := build
SO := $(BINARY_DIR)/
BCRYPT := $(BINARY_DIR)/bcryptgen
DOCKER_IMAGE := coolcry/mosquitto:latest

GOFLAGS :=
CGO_ENABLED := 1

.PHONY: all build bcryptgen clean docker-build docker-run mod

all: build bcryptgen

mod:
	go mod tidy

build: clean mod
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -gcflags "all=-N -l" -ldflags "" -o $(SO) .

build-prod: clean mod
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(SO) .

bcryptgen:
	mkdir -p $(BINARY_DIR)
	go build -o $(BINARY_DIR)/bcryptgen ./cmd/bcryptgen

clean:
	rm -rf $(BINARY_DIR)

local-run: build
	mosquitto -c ./mosquitto.conf -v

# Build a runnable Mosquitto image with the plugin baked in
docker-build:
	docker build -f Dockerfile -t coolcry/mosquitto:latest .

docker-bash:
	docker run --rm -it $(DOCKER_IMAGE) bash

# Quick run; assumes a postgres reachable per mosquitto.conf DSN
docker-run:
	docker run --rm -it \
	  --network host \
	  -v $(PWD)/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro \
	  $(DOCKER_IMAGE) mosquitto -c /mosquitto/config/mosquitto.conf

