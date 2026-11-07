load("//tools:defs.bzl", "go_library")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

exports_files(["hypervisor.entitlements"])

go_library(
    name = "entitle",
    srcs = ["entitle.go"],
    embedsrcs = ["hypervisor.entitlements"],
    importpath = "gvisor.dev/gvisor/pkg/hvf/entitle",
    visibility = ["//visibility:public"],
)
