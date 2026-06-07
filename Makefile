.PHONY: help build test test-commit test-release-candidate profile-config-check contract-check clean docker arm64

help:
	@echo "Levara repository commands"
	@echo ""
	@echo "  make build                  Build Levara server and CLI"
	@echo "  make test                   Run Go tests in Levara/"
	@echo "  make test-commit            Run focused every-commit gate"
	@echo "  make test-release-candidate Run local release-candidate gate"
	@echo "  make profile-config-check   Validate runtime profile config paths"
	@echo "  make contract-check         Validate REST/gRPC/MCP contract artifacts"
	@echo "  make docker                 Build Levara Docker image"
	@echo "  make arm64                  Cross-compile ARM64 server"

build test test-commit test-release-candidate profile-config-check contract-check clean docker arm64:
	@$(MAKE) -C Levara $@
