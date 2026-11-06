"""Repository rule for a header-only Linux sysroot made from Debian packages.

The sysroot has the C library and kernel UAPI headers for arm64 and amd64 and
no libraries. It is for code that is built with -nostdlib for Linux on a host
that is not Linux, such as the vDSO on macOS.
"""

def _linux_sysroot_impl(ctx):
    for i, pkg in enumerate(ctx.attr.packages):
        url, sha256 = pkg.split(" ")
        deb = "deb%d" % i
        ctx.download_and_extract(url = url, sha256 = sha256, output = deb, type = "deb")
        data = [f for f in ctx.path(deb).readdir() if f.basename.startswith("data.tar")]
        if len(data) != 1:
            fail("%s: expected one data.tar.* member, found %s" % (url, data))
        ctx.extract(data[0], output = "sysroot")
        ctx.delete(deb)
    ctx.file("BUILD.bazel", """\
filegroup(
    name = "sysroot",
    srcs = glob(["sysroot/usr/include/**"]),
    visibility = ["//visibility:public"],
)

# A file at the sysroot root; its directory is the --sysroot argument.
exports_files(["sysroot/root"], visibility = ["//visibility:public"])
""")
    ctx.file("sysroot/root", "")

linux_sysroot = repository_rule(
    implementation = _linux_sysroot_impl,
    attrs = {
        "packages": attr.string_list(
            doc = "Debian packages, each as \"<url> <sha256>\".",
            mandatory = True,
        ),
    },
)
