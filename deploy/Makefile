# Compatibility entrypoint: all Go commands run in backend/, independent of cwd.
.DEFAULT_GOAL := build
BACKEND_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/../backend)

.PHONY: wire build build-embed test-unit test-integration test-e2e test-cover-integration clean-coverage clean

wire build build-embed test-unit test-e2e test-cover-integration clean-coverage clean:
	@$(MAKE) -C "$(BACKEND_DIR)" $@

# Preserve the legacy integration target's race/parallel coverage.
TEST_INTEGRATION_FLAGS ?= -count=1 -race -parallel=8
test-integration:
	@$(MAKE) -C "$(BACKEND_DIR)" $@ TEST_INTEGRATION_FLAGS="$(TEST_INTEGRATION_FLAGS)"
