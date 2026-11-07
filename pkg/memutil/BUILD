load("//tools:defs.bzl", "go_library")

package(
    default_applicable_licenses = ["//:license"],
    licenses = ["notice"],
)

go_library(
    name = "memutil",
    srcs = [
        "memfd_darwin.go",
        "memfd_linux_unsafe.go",
        "memutil_unsafe.go",
        "mmap.go",
        "observer.go",
    ],
    importpath = "gvisor.dev/gvisor/pkg/memutil",
    visibility = ["//visibility:public"],
    deps = [
        "//pkg/abi/linux",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)
