load("//tools:defs.bzl", "go_library", "go_test")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

go_library(
    name = "ipa",
    srcs = ["ipa.go"],
    importpath = "gvisor.dev/gvisor/pkg/sentry/platform/hvf/ipa",
    visibility = ["//pkg/sentry:internal"],
    deps = ["//pkg/sync"],
)

go_test(
    name = "ipa_test",
    size = "small",
    srcs = ["ipa_test.go"],
    library = ":ipa",
    deps = ["//pkg/memutil"],
)
