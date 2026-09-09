BINARY := notiongate
PKG := ./cmd/notiongate
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin

.PHONY: build run test vet fmt docker clean install uninstall completion

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BINARY) $(PKG)

install: build
	@echo "  Гейтик переезжает в $(BINDIR)/$(BINARY)..."
	install -Dm755 $(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)
	@echo "  готово: $(DESTDIR)$(BINDIR)/$(BINARY) — теперь где угодно: notiongate --help"

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/$(BINARY)
	@echo "  удалён"

completion:
	@echo "  bash:  notiongate completion bash > /etc/bash_completion.d/notiongate"
	@echo "  zsh:   notiongate completion zsh > /usr/share/zsh/site-functions/_notiongate"
	@echo "  fish:  notiongate completion fish > ~/.config/fish/completions/notiongate.fish"
	@./$(BINARY) completion bash 2>/dev/null | head -n 5 || true

run: build
	./$(BINARY) serve

test:
	go test ./... -race -count=1

vet:
	go vet ./...

fmt:
	gofmt -l -w .

docker:
	docker compose up -d --build

clean:
	rm -f $(BINARY)
	rm -rf data
