APP := imchat
BIN := bin/imchat.exe
LDFLAGS := -s -w

.PHONY: build run tidy lint test verify e2e clean

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
	go test ./internal/... -v

e2e: build
	@echo "start the server first: ./$(BIN) -addr 127.0.0.1:18081 -db ./data/e2e.db"
	@node scripts/e2e.mjs

clean:
	rm -rf bin data web/dist
