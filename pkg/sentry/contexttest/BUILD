# The go_library rule is imported as a different name in this case,
# in order to avoid automated tooling doing the wrong thing with the
# operating-specific dependencies listed below.
load("//tools:defs.bzl", "select_system", exempt_go_library = "go_library")

package(
    default_applicable_licenses = ["//:license"],
    licenses = ["notice"],
)

exempt_go_library(
    name = "contexttest",
    testonly = 1,
    srcs = [
        "contexttest.go",
        "contexttest_darwin.go",
        "contexttest_linux.go",
    ],
    importpath = "gvisor.dev/gvisor/pkg/sentry/contexttest",
    # Nothing needs to be stateified, and stateify has trouble when select is
    # used to choose deps.
    stateify = False,
    visibility = ["//pkg/sentry:internal"],
    deps = [
        "//pkg/atomicbitops",
        "//pkg/context",
        "//pkg/memutil",
        "//pkg/sentry/kernel/auth",
        "//pkg/sentry/ktime",
        "//pkg/sentry/limits",
        "//pkg/sentry/pgalloc",
        "//pkg/sentry/platform",
        "//pkg/sentry/uniqueid",
    ] + select_system(
        darwin = [],
        linux = ["//pkg/sentry/platform/ptrace"],
    ),
)
