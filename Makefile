# Every target dispatches to scripts/make.sh, which holds the logic; on
# Windows without GNU make use .\make.ps1 <target> (same script via Git Bash).
TARGETS := check lint test ui-test build run-daemon cover integration escape-check smoke fixtures images e2e desktop

.PHONY: $(TARGETS)
.DEFAULT_GOAL := check

$(TARGETS):
	@bash scripts/make.sh $@
