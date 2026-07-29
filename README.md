# gazelle_orbit

This is a [gazelle](https://github.com/bazel-contrib/bazel-gazelle) extension that generates and updates [VHDL rules](https://github.com/hw-bzl/rules_vhdl) and [Verilog rules](https://github.com/hw-bzl/rules_verilog) for [Bazel](https://bazel.build) from [Orbit](https://chaseruskin.github.io/orbit) files.

To generate and update your HDL rules:
```
bazel run //:gazelle
```

The above command walks the current workspace, runs `orbit analyze`, and writes per-package `BUILD.bazel` files whose `srcs` and `deps` lists reflect the relationship between your HDL files. Cross-package and cross-library references resolve automatically through a workspace-wide index.

## Examples

See the [`examples`](./examples) directory for working examples for VHDL, Verilog, SystemVerilog, and RDL sources.

## Getting Started

Add the following lines to your `MODULE.bazel` file:

```python
bazel_dep(name = "gazelle", version = "0.51.0")
bazel_dep(name = "gazelle_orbit", version = "0.1.0")
bazel_dep(name = "rules_verilog", version = "1.2.0")
bazel_dep(name = "rules_vhdl", version = "0.2.0")
```

Add the following lines to your root `BUILD.bazel` file:

```python
load("@gazelle//:def.bzl", "gazelle")

gazelle(
    name = "gazelle",
    gazelle = "@gazelle_orbit",
)
```

Run the following command to update VHDL rules and Verilog rules:
```
bazel run //:gazelle
```