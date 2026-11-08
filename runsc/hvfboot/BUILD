load("//pkg/hvf/entitle:defs.bzl", "hvf_signed_binary")
load("//tools:defs.bzl", "go_binary", "go_test")

package(default_applicable_licenses = ["//:license"])

licenses(["notice"])

# hvfboot is hvfboot_unsigned, ad-hoc signed with the hypervisor entitlement.
hvf_signed_binary(
    name = "hvfboot",
    binary = ":hvfboot_unsigned",
    target_compatible_with = [
        "@platforms//os:macos",
        "@platforms//cpu:aarch64",
    ],
)

go_binary(
    name = "hvfboot_unsigned",
    srcs = [
        "gofer.go",
        "link.go",
        "main.go",
        "netproxy.go",
        "runsc.go",
        "sentry.go",
    ],
    target_compatible_with = [
        "@platforms//os:macos",
        "@platforms//cpu:aarch64",
    ],
    deps = [
        "//pkg/abi/linux",
        "//pkg/buffer",
        "//pkg/control/client",
        "//pkg/cpuid",
        "//pkg/fd",
        "//pkg/hvf",
        "//pkg/hvf/entitle",
        "//pkg/lisafs",
        "//pkg/log",
        "//pkg/seatbelt",
        "//pkg/sentry/platform/hvf",
        "//pkg/tcpip",
        "//pkg/tcpip/adapters/gonet",
        "//pkg/tcpip/header",
        "//pkg/tcpip/link/channel",
        "//pkg/tcpip/network/ipv4",
        "//pkg/tcpip/stack",
        "//pkg/tcpip/transport/tcp",
        "//pkg/tcpip/transport/udp",
        "//pkg/timing",
        "//pkg/unet",
        "//pkg/urpc",
        "//pkg/waiter",
        "//runsc/boot",
        "//runsc/boot/sentryapi",
        "//runsc/config",
        "//runsc/flag",
        "//runsc/gofer/fs",
        "//runsc/specutils",
        "@com_github_opencontainers_runtime_spec//specs-go:go_default_library",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)

go_test(
    name = "confine_test",
    size = "small",
    srcs = [
        "confine_darwin_arm64_test.go",
        "gofer.go",
        "link.go",
        "netproxy.go",
        "sentry.go",
    ],
    tags = ["local"],
    target_compatible_with = [
        "@platforms//os:macos",
        "@platforms//cpu:aarch64",
    ],
    deps = [
        "//pkg/abi/linux",
        "//pkg/buffer",
        "//pkg/lisafs",
        "//pkg/log",
        "//pkg/memutil",
        "//pkg/seatbelt",
        "//pkg/tcpip",
        "//pkg/tcpip/adapters/gonet",
        "//pkg/tcpip/header",
        "//pkg/tcpip/link/channel",
        "//pkg/tcpip/network/ipv4",
        "//pkg/tcpip/stack",
        "//pkg/tcpip/transport/tcp",
        "//pkg/tcpip/transport/udp",
        "//pkg/unet",
        "//pkg/waiter",
        "//runsc/config",
        "//runsc/gofer/fs",
        "@org_golang_x_sys//unix:go_default_library",
    ],
)
