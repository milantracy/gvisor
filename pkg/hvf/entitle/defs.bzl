"""Ad-hoc signing of binaries with the hypervisor entitlement."""

def hvf_signed_binary(name, binary, out = None, **kwargs):
    """Copies binary and ad-hoc signs the copy with the hypervisor entitlement.

    The signed binary can create a Hypervisor.framework VM without
    entitle.Reexec. Ad-hoc signing needs no developer account.

    Args:
      name: the name of the rule and of the signed binary.
      binary: the binary to sign.
      out: the file name of the signed binary, in the directory
        name + "_signed". The default is name.
      **kwargs: passed to genrule.
    """
    entitlements = "//pkg/hvf/entitle:hypervisor.entitlements"
    native.genrule(
        name = name,
        srcs = [binary, entitlements],
        outs = [name + "_signed/" + (out or name)],
        cmd = ("cp $(location {binary}) $@ && chmod u+w $@ && " +
               "/usr/bin/codesign -f -s - --entitlements $(location {entitlements}) $@").format(
            binary = binary,
            entitlements = entitlements,
        ),
        executable = True,
        **kwargs
    )
