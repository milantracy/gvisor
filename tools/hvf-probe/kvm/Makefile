# KVM counterpart of Phase 0 probe 1. Linux on arm64 only; needs read-write
# access to /dev/kvm (usually membership in the "kvm" group, no root).
#
#   make            build
#   make run        build and run (CPU=n pins the probe to CPU n)

CC      ?= cc
CFLAGS  ?= -O2 -g -Wall -Wextra -Werror -std=gnu11

all: kvm_vcpu_run

kvm_vcpu_run: kvm_vcpu_run.c kvm_guest.S
	$(CC) $(CFLAGS) -o $@ kvm_vcpu_run.c kvm_guest.S

run: kvm_vcpu_run
	./kvm_vcpu_run $(CPU)

clean:
	rm -f kvm_vcpu_run

.PHONY: all run clean
