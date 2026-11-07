load("//tools:defs.bzl", "go_binary")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

go_binary(
    name = "hvfboot",
    srcs = ["main.go"],
    target_compatible_with = [
        "@platforms//os:macos",
        "@platforms//cpu:aarch64",
    ],
    deps = [
        "//pkg/abi/linux",
        "//pkg/context",
        "//pkg/cpuid",
        "//pkg/fd",
        "//pkg/fspath",
        "//pkg/hvf/entitle",
        "//pkg/log",
        "//pkg/memutil",
        "//pkg/sentry/devices/memdev",
        "//pkg/sentry/fdimport",
        "//pkg/sentry/fsimpl/cgroup2fs",
        "//pkg/sentry/fsimpl/dev",
        "//pkg/sentry/fsimpl/erofs",
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
        "@org_golang_x_sys//unix:go_default_library",
    ],
)
