APP := imchat
BIN := bin/imchat.exe
LDFLAGS := -s -w

.PHONY: build run tidy lint test verify clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/imchat

run: build
	./$(BIN)

tidy:
	go mod tidy

lint:
	go vet ./...

test:
	go test ./...

verify:
	go test ./internal/... -run TestReliability -v

clean:
	rm -rf bin data web/dist
