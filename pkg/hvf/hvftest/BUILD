load("//tools:defs.bzl", "go_library")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

go_library(
    name = "hvftest",
    testonly = True,
    srcs = ["hvftest.go"],
    importpath = "gvisor.dev/gvisor/pkg/hvf/hvftest",
    visibility = ["//visibility:public"],
    deps = ["//pkg/hvf/entitle"],
)
