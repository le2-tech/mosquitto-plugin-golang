#SHELL := /bin/bash
BINARY_DIR := plugins
AUTH_SO := $(BINARY_DIR)/auth-plugin
QUEUE_SO := $(BINARY_DIR)/queue-plugin
CONN_SO := $(BINARY_DIR)/conn-plugin
BCRYPT := $(BINARY_DIR)/bcryptgen
DOCKER_IMAGE := ghcr.io/le2-tech/mosquitto

GOFLAGS :=
CGO_ENABLED := 1

.PHONY: all build-auth build-queue bcryptgen clean docker-build docker-run mod

mod:
	go mod tidy

build-auth-dev: mod
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -gcflags "all=-N -l" -ldflags "" -o $(AUTH_SO) ./authplugin

build-auth:
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(AUTH_SO) ./authplugin

build-queue:
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(QUEUE_SO) ./queueplugin

build-conn:
	mkdir -p $(BINARY_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -buildmode=c-shared -trimpath -ldflags="-s -w" -o $(CONN_SO) ./connplugin

bcryptgen:
	mkdir -p $(BINARY_DIR)
	go build -o $(BINARY_DIR)/bcryptgen ./cmd/bcryptgen

clean:
	rm -rf $(BINARY_DIR)

local-run: mod clean build-auth build-queue build-conn
	PG_DSN=postgres://iot:ZDZrMegCF0i-saVU@127.0.0.1:5433/iot?sslmode=disable QUEUE_DSN=amqp://rabbitmq_user:passwd@127.0.0.1:7772/ mosquitto -c ./mosquitto.conf -v

# Build a runnable Mosquitto image with the plugin baked in
docker-build-dev:
	docker build . -f Dockerfile --build-arg APP_ENV=dev -t $(DOCKER_IMAGE)

# Quick run; assumes a postgres reachable per mosquitto.conf DSN
docker-run:
	docker run --rm -it \
	  --network host \
	  -w /mosquitto \
	  -v $(PWD)/mosquitto.conf:/mosquitto/config/mosquitto.conf \
	  $(DOCKER_IMAGE) mosquitto -c ./config/mosquitto.conf

docker-bash:
	docker run --rm -it $(DOCKER_IMAGE) bash
