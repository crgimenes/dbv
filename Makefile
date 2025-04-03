BINARY_NAME=dbv

export CGO_ENABLED=0
GIT_TAG := $(shell git describe --tags --always)
BUILD_FLAGS := -trimpath -ldflags "-X 'main.GitTag=$(GIT_TAG)' -s -w -extldflags '-static -w'"

.PHONY: all build build-cross clean

all: build

build:
	go build $(BUILD_FLAGS) -o $(BINARY_NAME) .

clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-*

