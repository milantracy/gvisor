# Phase 0 Hypervisor.framework probes. macOS on Apple Silicon only.
#
#   make            build and ad-hoc sign every probe
#   make run-NAME   build, sign and run one probe
#   make run        run every probe
#
# Ad-hoc signing with the com.apple.security.hypervisor entitlement is
# enough; no developer account is needed.

CC      ?= clang
CFLAGS  ?= -O2 -g -Wall -Wextra -Werror -std=c11
CFLAGS  += -arch arm64
LDFLAGS += -arch arm64 -framework Hypervisor

PROBES  := vcpu_run
OUT     := out
COMMON  := $(OUT)/probe.o $(OUT)/vectors.o

all: $(addprefix $(OUT)/,$(PROBES))

$(OUT):
	mkdir -p $@

$(OUT)/%.o: %.c probe.h | $(OUT)
	$(CC) $(CFLAGS) -c -o $@ $<

$(OUT)/%.o: %.S | $(OUT)
	$(CC) -arch arm64 -c -o $@ $<

$(OUT)/vcpu_run: $(OUT)/vcpu_run.o $(OUT)/vcpu_run_guest.o $(COMMON) hvf.entitlements
	$(CC) $(LDFLAGS) -o $@ $(filter %.o,$^)
	codesign -f -s - --entitlements hvf.entitlements $@

run-%: $(OUT)/%
	./$<

run: $(addprefix run-,$(PROBES))

clean:
	rm -rf $(OUT)

.PHONY: all run clean
.PRECIOUS: $(OUT)/%.o
