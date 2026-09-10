HOSTNAME    = registry.terraform.io
NAMESPACE   = bartei
NAME        = nixos
VERSION     = 0.1.0
OS_ARCH     = $(shell go env GOOS)_$(shell go env GOARCH)
INSTALL_DIR = ~/.terraform.d/plugins/$(HOSTNAME)/$(NAMESPACE)/$(NAME)/$(VERSION)/$(OS_ARCH)

.PHONY: build install clean dev test testacc testacc-vm-up testacc-vm-down testacc-debian-vm-up testacc-debian-vm-down

build:
	go build -o terraform-provider-nixos

# Install to local plugin cache (for use without dev_overrides)
install: build
	mkdir -p $(INSTALL_DIR)
	cp terraform-provider-nixos $(INSTALL_DIR)/terraform-provider-nixos_v$(VERSION)

# For local development: use with dev_overrides in ~/.terraformrc
dev: build
	@echo "Binary built. Add this to ~/.terraformrc:"
	@echo ""
	@echo 'provider_installation {'
	@echo '  dev_overrides {'
	@echo '    "bartei/nixos" = "$(CURDIR)"'
	@echo '  }'
	@echo '  direct {}'
	@echo '}'

# Unit tests (no acceptance tests).
test:
	go test ./...

# Acceptance tests: real terraform plan/apply against the QEMU VM in
# test/qemu/. Requires `make testacc-vm-up` to be running first. See
# README.md "Acceptance tests" for prerequisites.
testacc:
	@if [ ! -s test/qemu/.vm-host ]; then \
		echo "VM not running. Start it first: make testacc-vm-up"; exit 1; \
	fi
	TF_ACC=1 \
	NIXOS_TEST_HOST=$$(cat test/qemu/.vm-host) \
	NIXOS_TEST_KEY_PATH=$(CURDIR)/test/qemu/.keys/id_ed25519 \
	NIXOS_TEST_USER=root \
	SYSMGR_TEST_HOST=$$(cat test/debian/.vm-host 2>/dev/null) \
	go test -v -count=1 -timeout 30m ./internal/resource/... ./internal/sshclient/...

testacc-vm-up:
	cd test/qemu && ./build.sh && ./run.sh > .vm-host
	@echo "VM ready at $$(cat test/qemu/.vm-host)"

testacc-vm-down:
	cd test/qemu && ./stop.sh
	rm -f test/qemu/.vm-host

# Debian VM for the nixos_system_manager tests (non-NixOS target). Optional:
# the system-manager tests skip when SYSMGR_TEST_HOST is empty. Requires
# genisoimage (cdrkit) in addition to QEMU.
testacc-debian-vm-up:
	cd test/debian && ./run.sh > .vm-host
	@echo "Debian VM ready at $$(cat test/debian/.vm-host)"

testacc-debian-vm-down:
	cd test/debian && ./stop.sh
	rm -f test/debian/.vm-host

clean:
	rm -f terraform-provider-nixos