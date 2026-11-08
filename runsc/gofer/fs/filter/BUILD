load("//pkg/seccomp/precompiledseccomp:defs.bzl", "precompiled_seccomp_rules")
# The go_library rule is imported as a different name in this case,
# in order to avoid automated tooling doing the wrong thing with the
# operating-specific dependencies listed below.
load("//tools:defs.bzl", "select_system", exempt_go_library = "go_library")

package(
    default_applicable_licenses = ["//:license"],
    licenses = ["notice"],
)

precompiled_seccomp_rules(
    name = "filter_precompiled",
    out = "filter_precompiled.go",
    exclude_in_fastbuild = True,
    out_package_name = "filter",
    programs_to_compile_go_import = "gvisor.dev/gvisor/runsc/gofer/fs/filter/config",
    programs_to_compile_go_library = "//runsc/gofer/fs/filter/config",
)

exempt_go_library(
    name = "filter",
    srcs = [
        "filter.go",
        "filter_darwin.go",
        "filter_precompiled.go",
    ],
    # Nothing needs to be stateified, and stateify has trouble when select is
    # used to choose deps.
    stateify = False,
    visibility = [
        "//runsc:__subpackages__",
    ],
    deps = [
        "//pkg/seccomp/precompiledseccomp",
        "//pkg/sync",
    ] + select_system(
        darwin = [
            "//pkg/seatbelt",
            "//pkg/seccomp",
        ],
        linux = [
            "//runsc/gofer/filter",
            "//runsc/gofer/fs/filter/config",
        ],
    ),
)
