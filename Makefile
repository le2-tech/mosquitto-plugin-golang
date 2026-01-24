#SHELL := /bin/bash
BINARY_DIR := build
AUTH_SO := $(BINARY_DIR)/auth-plugin
QUEUE_SO := $(BINARY_DIR)/queue-plugin
BCRYPT := $(BINARY_DIR)/bcryptgen
DOCKER_IMAGE := ghcr.io/le2-tech/mosquitto

GOFLAGS :=
CGO_ENABLED := 1

.PHONY: all build build-queue bcryptgen clean docker-build docker-run mod

all: build bcryptgen

mod:
	go mod tidy

build-dev: clean mod
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -gcflags "all=-N -l" -ldflags "" -o $(AUTH_SO) ./authplugin

build: clean mod
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(AUTH_SO) ./authplugin

build-queue: mod
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(QUEUE_SO) ./queueplugin

bcryptgen:
	mkdir -p $(BINARY_DIR)
	go build -o $(BINARY_DIR)/bcryptgen ./cmd/bcryptgen

clean:
	rm -rf $(BINARY_DIR)

local-run: build build-queue
	PG_DSN=postgres://iot:ZDZrMegCF0i-saVU@127.0.0.1:5433/iot?sslmode=disable QUEUE_DSN=amqp://rabbitmq_user:passwd@127.0.0.1:7772/ mosquitto -c ./mosquitto.conf -v

# Build a runnable Mosquitto image with the plugin baked in
docker-build-dev:
	docker build . -f Dockerfile --build-arg APP_ENV=dev -t $(DOCKER_IMAGE)

docker-bash:
	docker run --rm -it $(DOCKER_IMAGE) bash

# Quick run; assumes a postgres reachable per mosquitto.conf DSN
docker-run:
	docker run --rm -it \
	  --network host \
	  -v $(PWD)/mosquitto.conf:/mosquitto/config/mosquitto.conf:ro \
	  $(DOCKER_IMAGE) mosquitto -c /mosquitto/config/mosquitto.conf
