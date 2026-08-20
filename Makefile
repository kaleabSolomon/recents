.PHONY: fmt test lint build bins ci perf install uninstall

PREFIX ?= /usr/local
UNIT_DIR ?= /etc/systemd/system
# The user whose opens are tracked. `sudo make install` picks up the invoking
# user automatically; override with `sudo make install RECENTS_USER=name`.
RECENTS_USER ?= $(or $(SUDO_USER),$(USER))

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

lint:
	go vet ./...

build:
	go build ./...

bins:
	mkdir -p bin
	go build -o bin/recents ./cmd/recents
	go build -o bin/recentsd ./cmd/recentsd

ci: fmt lint test build

perf:
	go test -bench . -benchmem ./...

# Linux only. Run as: sudo make install
install: bins
	install -m 0755 bin/recents $(PREFIX)/bin/recents
	install -m 0755 bin/recentsd $(PREFIX)/bin/recentsd
	sed -e 's|@USER@|$(RECENTS_USER)|g' -e 's|@BINDIR@|$(PREFIX)/bin|g' \
		packaging/recentsd.service.in > $(UNIT_DIR)/recentsd.service
	systemctl daemon-reload
	systemctl enable --now recentsd.service
	@echo "recentsd installed and running; tracking opens for user '$(RECENTS_USER)'"
	@echo "check it with: systemctl status recentsd"

# Linux only. Run as: sudo make uninstall
uninstall:
	-systemctl disable --now recentsd.service
	rm -f $(UNIT_DIR)/recentsd.service $(PREFIX)/bin/recents $(PREFIX)/bin/recentsd
	systemctl daemon-reload
