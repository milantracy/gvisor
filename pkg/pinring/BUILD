load("//tools:defs.bzl", "go_library", "go_test")

package(
    default_applicable_licenses = ["//:license"],
    licenses = ["notice"],
)

go_library(
    name = "pinring",
    srcs = [
        "pinring.go",
        "pinring_darwin.go",
        "pinring_unsafe.go",
    ],
    importpath = "gvisor.dev/gvisor/pkg/pinring",
    visibility = ["//:sandbox"],
    deps = [
        "//pkg/abi/linux",
        "//pkg/log",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)

go_test(
    name = "pinring_test",
    size = "medium",
    srcs = ["pinring_test.go"],
    embed = [":pinring"],
    target_compatible_with = ["@platforms//os:linux"],
    deps = [
        "@org_golang_x_sys//unix:go_default_library",
    ],
)
