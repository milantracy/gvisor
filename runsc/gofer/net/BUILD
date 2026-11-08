# The go_library rule is imported as a different name in this case,
# in order to avoid automated tooling doing the wrong thing with the
# operating-specific dependencies listed below.
load("//tools:defs.bzl", "go_test", "select_system", exempt_go_library = "go_library")

package(
    default_applicable_licenses = ["//:license"],
    licenses = ["notice"],
)

exempt_go_library(
    name = "net",
    srcs = [
        "link_darwin.go",
        "netgofer.go",
        "netgofer_darwin.go",
        "proxy_darwin.go",
    ],
    stateify = False,
    visibility = [
        "//runsc:__subpackages__",
    ],
    deps = [
        "//pkg/log",
        "//pkg/tcpip",
        "//pkg/tcpip/adapters/gonet",
        "//pkg/tcpip/header",
        "//pkg/tcpip/network/ipv4",
        "//pkg/tcpip/stack",
        "//pkg/tcpip/transport/tcp",
        "//pkg/waiter",
        "//runsc/cmd/util",
        "//runsc/config",
        "//runsc/flag",
        "@com_github_google_subcommands//:go_default_library",
        "@org_golang_x_sys//unix:go_default_library",
    ] + select_system(
        darwin = [
            "//pkg/buffer",
            "//pkg/seatbelt",
            "//pkg/tcpip/link/channel",
            "//pkg/tcpip/transport/udp",
        ],
        linux = [
            "//pkg/tcpip/link/fdbased",
            "//pkg/tcpip/network/ipv6",
            "//runsc/cmd/sandboxsetup",
            "//runsc/gofer/net/egressfilter",
            "//runsc/gofer/net/filter",
            "//runsc/profile",
            "@com_github_opencontainers_runtime_spec//specs-go:go_default_library",
        ],
    ),
)

go_test(
    name = "netgofer_test",
    size = "small",
    srcs = [
        "netgofer_test.go",
    ],
    embed = [":net"],
    target_compatible_with = ["@platforms//os:linux"],
    deps = [
        "//pkg/log",
        "//pkg/tcpip",
        "//pkg/tcpip/adapters/gonet",
        "//pkg/tcpip/header",
        "//pkg/tcpip/link/fdbased",
        "//pkg/tcpip/network/ipv4",
        "//pkg/tcpip/network/ipv6",
        "//pkg/tcpip/stack",
        "//pkg/tcpip/transport/tcp",
        "//runsc/config",
        "//runsc/flag",
        "//runsc/gofer/net/egressfilter",
        "@com_github_google_subcommands//:go_default_library",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)
