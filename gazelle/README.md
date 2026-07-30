# gazelle_orbit

A [Gazelle](https://github.com/bazel-contrib/bazel-gazelle) language
extension that generates `vhdl_library` and `verilog_library` Bazel
targets from HDL sources by delegating parsing and dependency analysis
to the [orbit](https://github.com/chaseruskin/orbit) toolchain.

One `bazel run //:gazelle` walks your workspace, runs
`orbit analyze --plan json --local --force` under the hood, and writes
per-package `BUILD.bazel` files whose `srcs` and `deps` lists reflect
orbit's blueprint — a file-level dependency graph over every HDL source
in your workspace. Cross-package and cross-library references resolve
automatically through a workspace-wide file-path index.

## Quick start

In your `MODULE.bazel`:

```python
bazel_dep(name = "gazelle", version = "0.51.0")
bazel_dep(name = "gazelle_orbit", version = "0.1.0")
bazel_dep(name = "rules_verilog", version = "1.2.0")
bazel_dep(name = "rules_vhdl", version = "0.2.0")
```

In your root `BUILD.bazel`:

```python
load("@gazelle//:def.bzl", "gazelle")

gazelle(
    name = "gazelle",
    gazelle = "@gazelle_orbit",
)
```

Then:

```bash
bazel run //:gazelle
```

That's the entire wiring. The `orbit` CLI is bundled as a runfile of the
plugin — no separate install, no `--orbit_bin` flag, no `PATH` setup.
`orbit analyze` auto-syncs `Orbit.lock` on every invocation (including
for path-based dependency projects that don't yet have their own
lockfile), so you never have to invoke orbit by hand either.

## How dependencies are resolved

Orbit's blueprint reports, for every HDL file in the current project,
the absolute filepaths it directly depends on (local files or files in
path-dep projects reachable via `Orbit.toml`). The plugin turns each
entry into one Bazel rule and each dependency filepath into a Bazel
label by looking up the target that owns that file in the workspace.

Resolution order for each dependency filepath:

1. `# gazelle:resolve orbit <workspace-relative-path> <label>` override.
2. Workspace-wide index lookup keyed on the same workspace-relative
   path (populated by each rule's `srcs` via `Imports()`).

If no match, the dependency is silently dropped — usually because it
points at a file outside the Bazel workspace (e.g. an entry in orbit's
`~/.orbit/cache/` directory).

## Rule kinds

The plugin generates two kinds, one per HDL source file:

| Kind             | Loaded from                       | Emitted for filesets |
| ---------------- | --------------------------------- | -------------------- |
| `vhdl_library`   | `@rules_vhdl//vhdl:defs.bzl`      | `VHDL` (.vhd, .vhdl) |
| `verilog_library`| `@rules_verilog//verilog:defs.bzl`| `VLOG` (.v, .vh), `SYSV` (.sv, .svh) |

Target names come from the file itself: the srcs entry with its
extension stripped, subdirectory prefix preserved (so
`subdir/foo.vhd` → `//pkg:subdir/foo`). Multi-file design units (a VHDL
entity + separate architecture, or package + body split across files)
end up as one Bazel target per file with a dep between them, driven by
whatever orbit's blueprint reports.

Generated rules carry `visibility = ["//visibility:public"]` by
default; user overrides stick on subsequent runs.

## Hand-authored rules (code generators, vendor IP, …)

If a rule's HDL isn't visible to orbit at gazelle time — code generators
(PeakRDL, HLS), build-time-emitted headers (`write_file`), vendor stubs
— it doesn't appear in orbit's blueprint, so the plugin can't compute
deps to or through it. Two Gazelle-built-in mechanisms fill the gap.

### `# keep` — pin a hand-added dep

When blueprint produces no dep the plugin can override — typical for
code-generator wrappers whose HDL orbit never sees — hand-write the dep
on the consuming rule and mark the entry
[`# keep`](https://github.com/bazel-contrib/bazel-gazelle/blob/master/Directives.md#directives):

```python
verilog_library(
    name = "dsp_top",
    srcs = ["dsp_top.sv"],
    library = "dsp",
    deps = [
        "//uart_regs",  # keep — PeakRDL output, orbit can't see it
        ":multiplier",
    ],
)
```

Gazelle's merger honors `# keep` on any mergeable list attribute
(`deps`, `verilog_deps`, `vhdl_deps`, `tags`), so the entry survives
every regen even though `Resolve()` never produced it.

### `# gazelle:resolve` — redirect a computed dep

When blueprint *does* produce a dep but you want it to land at a
different label (test-only mock, vendored replacement), use
[Gazelle's built-in](https://github.com/bazel-contrib/bazel-gazelle/blob/master/Directives.md)
`# gazelle:resolve` directive. Format:

```
# gazelle:resolve orbit <workspace-relative-path> <label>
```

Example — swap out `//vutils:reg_n` for a mock in tests that live
under some directory:

```python
# some/tests/BUILD.bazel  (or any parent BUILD)
# gazelle:resolve orbit vutils/reg_n.v //some/tests:reg_n_mock
```

Directives inherit down the tree, so put them at the workspace root
for cross-cutting overrides and at a subtree BUILD to scope them
narrowly.

### Which to use

| Use `# keep` when…                                                              | Use `# gazelle:resolve` when…                                        |
| ------------------------------------------------------------------------------- | -------------------------------------------------------------------- |
| Blueprint has **no** dep to override (the file orbit needs isn't visible to it) | Blueprint **has** a dep, but it should land at a different label     |
| Code-generator wrappers, `` `include``, macros                                  | Test mocks, vendored replacements, alt implementations               |
| Info lives with the consumer that needs it                                      | Cross-cutting override you want to state once at a common ancestor   |

## Other directives

| Directive                       | Effect |
| ------------------------------- | ------ |
| `# gazelle:orbit_disable true`  | Skip rule generation in this subtree |

## How it works

1. **Per-directory traversal.** Gazelle visits each directory in
   dependency order. For any directory owning HDL sources (per the
   nearest-existing-ancestor-BUILD rule), the plugin invokes
   `orbit analyze --plan json --local --force` in it. Orbit auto-syncs
   the lockfile and emits a blueprint of every local HDL file plus the
   absolute filepaths it directly depends on.

2. **File-based rule generation.** One `vhdl_library` /
   `verilog_library` per blueprint entry, kind chosen from orbit's
   `fileset` (`VHDL` / `VLOG` / `SYSV`), name derived from the srcs
   entry's path (extension stripped). Only entries whose filepath is
   owned by this BUILD under the placement rule get a rule here;
   entries owned by an ancestor BUILD are handled by that BUILD's
   invocation of GenerateRules.

3. **Workspace-wide indexing.** Gazelle calls `Imports()` on every
   plugin-managed rule. The plugin registers each rule's srcs (as
   workspace-relative paths) in the resolve index so any consumer's
   blueprint dependency can find the label that owns the file.

4. **Resolution.** For every rule the plugin generated, it resolves
   each blueprint dependency filepath — first through
   `# gazelle:resolve orbit …` overrides, then the workspace-wide
   index — and writes the resulting labels to `deps` (same-language
   deps) or `verilog_deps` / `vhdl_deps` (cross-language, routed by
   the dep file's extension).
