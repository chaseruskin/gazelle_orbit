"""Transition wrapper that forces @curl to be built with the openssl SSL backend.

The BCR curl module defaults to BoringSSL, which is C++ and pulls in
libstdc++ symbols. Some toolchains drop the C++ runtime in the wrong link
order, producing undefined references to `__gxx_personality_v0`, etc. We
already ship OpenSSL via the `openssl` bazel_dep — telling curl to use it
keeps the link line pure C.

Rather than push `--@curl//:ssl_lib=openssl` onto every downstream
consumer's bazelrc, this rule pins the flag via an outgoing transition
applied to the curl dep itself. The wrapper forwards curl's CcInfo so it
drops into the same dep slot curl-sys would otherwise occupy.
"""

load("@rules_cc//cc/common:cc_info.bzl", "CcInfo")

def _force_openssl_transition_impl(_settings, _attr):
    return {"@curl//:ssl_lib": "openssl"}

_force_openssl_transition = transition(
    implementation = _force_openssl_transition_impl,
    inputs = [],
    outputs = ["@curl//:ssl_lib"],
)

def _curl_openssl_impl(ctx):
    # ctx.attr.target is a list because outgoing transitions can produce
    # multiple configurations; ours always produces a single one.
    target = ctx.attr.target[0]
    return [
        target[CcInfo],
        DefaultInfo(files = target[DefaultInfo].files),
    ]

curl_openssl = rule(
    implementation = _curl_openssl_impl,
    attrs = {
        "target": attr.label(
            cfg = _force_openssl_transition,
            mandatory = True,
            providers = [CcInfo],
            doc = "The curl cc_library to wrap.",
        ),
    },
    doc = "Wraps a curl cc_library, forcing its ssl_lib build setting to openssl.",
)
