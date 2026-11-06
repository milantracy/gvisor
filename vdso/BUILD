# Description:
#   This VDSO is a shared library that provides the same interfaces as the
#   normal system VDSO (time, gettimeofday, clock_gettimeofday) but which uses
#   timekeeping parameters managed by the sandbox kernel.

load("@bazel_skylib//lib:selects.bzl", "selects")
load("@bazel_skylib//rules:common_settings.bzl", "bool_flag")
load("@rules_python//python:py_test.bzl", "py_test")
load("//tools:arch.bzl", "select_arch")
load("//tools:defs.bzl", "cc_flags_supplier", "cc_toolchain")

package(
    default_applicable_licenses = ["//:license"],
    licenses = ["notice"],
)

exports_files(["check_vdso.py"])

# Set on macOS hosts by .bazelrc. The host has no Linux cross toolchain, so the
# vDSO is built with clang and lld against @linux_sysroot.
bool_flag(
    name = "darwin_host",
    build_setting_default = False,
)

config_setting(
    name = "darwin_host_enabled",
    flag_values = {":darwin_host": "True"},
)

selects.config_setting_group(
    name = "darwin_host_amd64",
    match_all = [
        ":darwin_host_enabled",
        "//tools/bazeldefs:amd64",
    ],
)

selects.config_setting_group(
    name = "darwin_host_arm64",
    match_all = [
        ":darwin_host_enabled",
        "//tools/bazeldefs:arm64",
    ],
)

_DARWIN_CC = (
    "LLD=$$(PATH=$$PATH:/opt/homebrew/bin:/usr/local/bin command -v ld.lld) || " +
    "{ echo 'ld.lld not found; run: brew install lld' >&2; exit 1; }; " +
    "clang --ld-path=$$LLD " +
    "--sysroot=$$(dirname $(location @linux_sysroot//:sysroot/root)) "
)

genrule(
    name = "vdso",
    srcs = [
        "barrier.h",
        "compiler.h",
        "cycle_clock.h",
        "seqlock.h",
        "syscalls.h",
        "vdso.cc",
        "vdso_amd64.lds",
        "vdso_arm64.lds",
        "vdso_time.h",
        "vdso_time.cc",
    ] + select({
        ":darwin_host_enabled": [
            "@linux_sysroot//:sysroot",
            "@linux_sysroot//:sysroot/root",
        ],
        "//conditions:default": [],
    }),
    outs = [
        "vdso.so",
    ],
    # On macOS hosts: Apple clang, lld from Homebrew, and Linux headers from
    # @linux_sysroot.
    cmd = select({
        ":darwin_host_amd64": _DARWIN_CC + "--target=x86_64-linux-gnu ",
        ":darwin_host_arm64": _DARWIN_CC + "--target=aarch64-linux-gnu ",
        "//conditions:default": "$(CC) $(CC_FLAGS) ",
    }) +
          "-I. " +
          "-O2 " +
          "-std=c++11 " +
          "-fPIC " +
          "-fno-sanitize=all " +
          # Some toolchains enable stack protector by default. Disable it, the
          # VDSO has no hooks to handle failures.
          "-fno-stack-protector " +
          "-shared " +
          "-nostdlib " +
          "-Wl,-soname=linux-vdso.so.1 " +
          "-Wl,--hash-style=sysv " +
          "-Wl,--no-undefined " +
          "-Wl,-Bsymbolic " +
          "-Wl,-z,max-page-size=4096 " +
          "-Wl,-z,common-page-size=4096 " +
          select_arch(
              amd64 = "-Wl,-T$(location vdso_amd64.lds) ",
              arm64 = "-Wl,-T$(location vdso_arm64.lds) ",
              no_match_error = "unsupported architecture",
          ) +
          "-o $(location vdso.so) " +
          "$(location vdso.cc) " +
          "$(location vdso_time.cc)",
    features = ["-pie"],
    toolchains = [
        cc_toolchain,
        ":no_pie_cc_flags",
    ],
    visibility = ["//:sandbox"],
)

cc_flags_supplier(
    name = "no_pie_cc_flags",
    features = ["-pie"],
)

py_test(
    name = "vdso_test",
    srcs = ["check_vdso.py"],
    args = [
        "--check-data",
        "--vdso=$(location :vdso)",
    ],
    data = [":vdso"],
    main = "check_vdso.py",
)
