#SHELL := /bin/bash
BINARY_DIR := plugins
DOCKER_IMAGE := ghcr.io/le2-tech/mosquitto

GOFLAGS :=
CGO_ENABLED ?= 1

.PHONY: all build-auth build-queue bcryptgen clean docker-build docker-run mod

mod:
	go mod tidy

test:
	go test ./...
	go vet ./...

mkdir:
	mkdir -p $(BINARY_DIR)

build-auth-dev:
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -gcflags "all=-N -l" -ldflags "" -o $(BINARY_DIR)/auth-plugin ./plugin/authplugin

build-auth:
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(BINARY_DIR)/auth-plugin ./plugin/authplugin

build-queue:
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(BINARY_DIR)/queue-plugin ./plugin/queueplugin

build-conn:
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(BINARY_DIR)/conn-plugin ./plugin/connplugin

run-bcryptgen:
	go run ./cmd/bcryptgen --salt slat_foo123 --password public

clean:
	rm -rf $(BINARY_DIR)

local-run: mod clean mkdir build-auth build-queue build-conn
	mosquitto --version
	PG_DSN=postgres://iot:ZDZrMegCF0i-saVU@127.0.0.1:7733/iot?sslmode=disable QUEUE_DSN=amqp://rabbitmq_user:passwd@127.0.0.1:7772/ mosquitto -c ./mosquitto.conf -v

# Build a runnable Mosquitto image with the plugin baked in
docker-build-dev:
	docker build . -f Dockerfile --build-arg APP_ENV=dev -t $(DOCKER_IMAGE)

# Quick run; assumes a postgres reachable per mosquitto.conf DSN
docker-run-http:
	docker run --rm -it \
	  -p 1883:1883 -p 9001:9001 -p 9002:9002 \
	  -w /mosquitto \
	  -v $(PWD)/mosquitto.http.conf:/mosquitto/config/mosquitto.conf \
	  eclipse-mosquitto:latest mosquitto -c ./config/mosquitto.conf

docker-bash:
	docker run --rm -it $(DOCKER_IMAGE) bash

api-clients:
	curl http://localhost:9002/api/clients
