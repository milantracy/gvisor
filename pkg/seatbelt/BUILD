load("//tools:defs.bzl", "go_library", "go_test")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

go_library(
    name = "seatbelt",
    srcs = [
        "seatbelt.go",
        "seatbelt_darwin_arm64.s",
        "seatbelt_darwin_arm64_unsafe.go",
    ],
    visibility = ["//:sandbox"],
    deps = ["@org_golang_x_sys//unix:go_default_library"],
)

go_test(
    name = "seatbelt_test",
    size = "small",
    srcs = ["seatbelt_darwin_arm64_test.go"],
    library = ":seatbelt",
    tags = ["local"],
    deps = ["@org_golang_x_sys//unix:go_default_library"],
)
