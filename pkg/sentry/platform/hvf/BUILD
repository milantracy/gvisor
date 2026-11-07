load("//tools:defs.bzl", "go_library", "go_test")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

go_library(
    name = "hvf",
    srcs = [
        "address_space_darwin_arm64.go",
        "allocator_unsafe.go",
        "context_darwin_arm64.go",
        "doc.go",
        "handoff_darwin_arm64.go",
        "hvf_darwin_arm64.go",
        "osync_darwin_arm64.s",
        "osync_unsafe.go",
        "stub_darwin_arm64.go",
        "vcpu_darwin_arm64.go",
    ],
    importpath = "gvisor.dev/gvisor/pkg/sentry/platform/hvf",
    visibility = ["//pkg/sentry:internal"],
    deps = [
        "//pkg/abi/linux",
        "//pkg/atomicbitops",
        "//pkg/context",
        "//pkg/hostarch",
        "//pkg/hvf",
        "//pkg/memutil",
        "//pkg/ring0/pagetables",
        "//pkg/sentry/arch",
        "//pkg/sentry/memmap",
        "//pkg/sentry/platform",
        "//pkg/sentry/platform/hvf/ipa",
        "//pkg/sentry/platform/interrupt",
        "//pkg/sync",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)

go_test(
    name = "hvf_test",
    size = "small",
    srcs = ["hvf_darwin_arm64_test.go"],
    library = ":hvf",
    tags = ["local"],
    deps = [
        "//pkg/abi/linux",
        "//pkg/context",
        "//pkg/hostarch",
        "//pkg/hvf",
        "//pkg/hvf/hvftest",
        "//pkg/memutil",
        "//pkg/safemem",
        "//pkg/sentry/arch",
        "//pkg/sentry/memmap",
        "//pkg/sentry/pgalloc",
        "//pkg/sentry/platform",
        "//pkg/sentry/usage",
    ],
)
