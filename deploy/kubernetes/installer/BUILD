load("//tools:defs.bzl", "go_binary", "go_test")

package(
    default_applicable_licenses = ["//:license"],
    licenses = ["notice"],
)

go_binary(
    name = "installer",
    srcs = [
        "containerd.go",
        "kube.go",
        "main.go",
        "runtime.go",
    ],
    pure = True,
    deps = ["@com_github_burntsushi_toml//:go_default_library"],
    static = True,
    visibility = ["//visibility:public"],
)

go_test(
    name = "installer_test",
    size = "small",
    srcs = [
        "containerd.go",
        "containerd_test.go",
        "kube.go",
        "main.go",
        "main_test.go",
        "runtime.go",
    ],
    deps = ["@com_github_burntsushi_toml//:go_default_library"],
)
