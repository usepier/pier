# Keep the repository-level developer commands stable while the Go project
# lives alongside the app in cli/.
.PHONY: build supervisors install test clean ios-testflight macos-release notary-setup

build supervisors install test clean:
	$(MAKE) -C cli $@

ios-testflight macos-release notary-setup:
	$(MAKE) -C app $@
