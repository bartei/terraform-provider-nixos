HOSTNAME    = registry.terraform.io
NAMESPACE   = bartei
NAME        = nixos
VERSION     = 0.1.0
OS_ARCH     = $(shell go env GOOS)_$(shell go env GOARCH)
INSTALL_DIR = ~/.terraform.d/plugins/$(HOSTNAME)/$(NAMESPACE)/$(NAME)/$(VERSION)/$(OS_ARCH)

.PHONY: build install clean dev

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

clean:
	rm -f terraform-provider-nixos