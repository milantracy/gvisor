load("//tools:defs.bzl", "go_library")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

go_library(
    name = "fsutil",
    srcs = [
        "fsutil.go",
        "fsutil_amd64_unsafe.go",
        "fsutil_arm64_unsafe.go",
        "fsutil_darwin.go",
        "fsutil_darwin_unsafe.go",
        "fsutil_linux.go",
        "fsutil_unsafe.go",
    ],
    importpath = "gvisor.dev/gvisor/pkg/fsutil",
    visibility = ["//visibility:public"],
    deps = [
        "//pkg/abi/linux",
        "//pkg/syserr",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)
