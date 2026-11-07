load("//pkg/hvf/entitle:defs.bzl", "hvf_signed_binary")
load("//tools:defs.bzl", "go_binary", "go_test")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

# hvfboot is hvfboot_unsigned, ad-hoc signed with the hypervisor entitlement.
hvf_signed_binary(
    name = "hvfboot",
    binary = ":hvfboot_unsigned",
    target_compatible_with = [
        "@platforms//os:macos",
        "@platforms//cpu:aarch64",
    ],
)

go_binary(
    name = "hvfboot_unsigned",
    srcs = [
        "gofer.go",
        "main.go",
        "sentry.go",
    ],
    target_compatible_with = [
        "@platforms//os:macos",
        "@platforms//cpu:aarch64",
    ],
    deps = [
        "//pkg/abi/linux",
        "//pkg/context",
        "//pkg/cpuid",
        "//pkg/errors/linuxerr",
        "//pkg/fd",
        "//pkg/fspath",
        "//pkg/hvf",
        "//pkg/hvf/entitle",
        "//pkg/lisafs",
        "//pkg/log",
        "//pkg/memutil",
        "//pkg/seatbelt",
        "//pkg/sentry/devices/memdev",
        "//pkg/sentry/fdimport",
        "//pkg/sentry/fsimpl/cgroup2fs",
        "//pkg/sentry/fsimpl/dev",
        "//pkg/sentry/fsimpl/erofs",
        "//pkg/sentry/fsimpl/gofer",
        "//pkg/sentry/fsimpl/host",
        "//pkg/sentry/fsimpl/proc",
        "//pkg/sentry/fsimpl/tmpfs",
        "//pkg/sentry/kernel",
        "//pkg/sentry/kernel/auth",
        "//pkg/sentry/limits",
        "//pkg/sentry/loader",
        "//pkg/sentry/pgalloc",
        "//pkg/sentry/platform/hvf",
        "//pkg/sentry/strace",
        "//pkg/sentry/syscalls/linux",
        "//pkg/sentry/time",
        "//pkg/sentry/usage",
        "//pkg/sentry/vfs",
        "//pkg/unet",
        "//runsc/config",
        "//runsc/gofer/fs",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)

go_test(
    name = "confine_test",
    size = "small",
    srcs = [
        "confine_darwin_arm64_test.go",
        "gofer.go",
        "sentry.go",
    ],
    tags = ["local"],
    target_compatible_with = [
        "@platforms//os:macos",
        "@platforms//cpu:aarch64",
    ],
    deps = [
        "//pkg/abi/linux",
        "//pkg/lisafs",
        "//pkg/log",
        "//pkg/memutil",
        "//pkg/seatbelt",
        "//pkg/unet",
        "//runsc/config",
        "//runsc/gofer/fs",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)
