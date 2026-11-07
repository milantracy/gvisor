load("//tools:defs.bzl", "go_library", "go_test")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

go_library(
    name = "hvf",
    srcs = [
        "hvf.go",
        "hvf_darwin_arm64.go",
        "hvf_darwin_arm64.s",
        "hvf_darwin_arm64_unsafe.go",
    ],
    importpath = "gvisor.dev/gvisor/pkg/hvf",
    visibility = ["//visibility:public"],
)

go_test(
    name = "hvf_test",
    size = "small",
    srcs = ["hvf_darwin_arm64_test.go"],
    library = ":hvf",
    tags = ["local"],
    deps = [
        "//pkg/hvf/hvftest",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)
