APP := imchat
BIN := bin/imchat.exe
LDFLAGS := -s -w

.PHONY: build run release tidy lint test verify notes-check e2e load clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/imchat

run: build
	./$(BIN)

release:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/imchat-linux-amd64 ./cmd/imchat
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/imchat-linux-arm64 ./cmd/imchat
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/imchat-windows-amd64.exe ./cmd/imchat

tidy:
	go mod tidy

lint:
	go vet ./...

test:
	go test ./...

verify:
	go test ./internal/... -v

notes-check:
	go run ./cmd/notescheck .

e2e: build
	@echo "start the server first: ./$(BIN) -addr 127.0.0.1:18081 -db ./data/e2e.db"
	@node scripts/e2e.mjs

load:
	go build -o bin/loadtest.exe ./cmd/loadtest
	./bin/loadtest.exe -base http://127.0.0.1:8080 -workers 16 -per 50

clean:
	rm -rf bin data web/dist
