# gazelle_orbit example

A six-package HDL workspace that exercises the `gazelle_orbit` plugin
across VHDL, Verilog, and SystemVerilog with cross-package, cross-library,
and code-generator-output dependencies. Uses [rules_vhdl] and
[rules_verilog] for the generated rule kinds.

[rules_vhdl]: https://github.com/hw-bzl/rules_vhdl
[rules_verilog]: https://github.com/hw-bzl/rules_verilog

## Layout

```
primitives/       VHDL — inverter, and_gate                 (library: primitives)
gates/            VHDL — nand_gate (uses primitives.*),     (library: gates)
                         or_gate
vutils/           Verilog — reg_n, mux2, counter (uses reg_n)
preproc/          Bazel-only — `write_file` produces params.svh, wrapped
                  by a hand-authored verilog_library; consumers pin the
                  dep with `# keep` (see cpu:timer)
uart_regs/        Code-gen wrapper — system_rdl_library +
                  verilog_system_rdl_library; consumer (dsp:dsp_top)
                  pins the dep with `# keep` since orbit never sees the
                  PeakRDL-generated .sv files

cpu/              Top-level (SystemVerilog)
                  alu      : pure SV using vutils.reg_n
                  cpu_top  : :alu + vutils.mux2 + vutils.counter
                  timer    : includes preproc/params.svh (# keep dep)

dsp/              Top-level (Verilog + SV)
                  multiplier : pure Verilog using vutils.reg_n
                  dsp_top    : :multiplier + vutils.counter +
                               uart_regs (module) + uart_regs_pkg (SV import)
```

Both `cpu` and `dsp` share `vutils`; `dsp` additionally consumes the
SystemRDL-generated `uart_regs` library. The plugin resolves refs to
in-workspace HDL automatically; anything orbit can't see (PeakRDL
output, `write_file` output) needs a `# keep` on the consuming rule's
dep line.

> Note: rules_verilog's `verilog_library` requires `VerilogInfo` in its
> deps and rules_vhdl's `vhdl_library` requires `VhdlInfo`, so the two
> ecosystems don't currently mix in a single deps list. The plugin still
> detects mixed-language references in HDL source if you write them — it
> just leaves you on the hook for getting the providers to align (e.g.
> a wrapper rule). The example keeps each top-level project within one
> language so the generated targets build end-to-end.

## Depending on things orbit can't see

Orbit runs at gazelle time and emits a blueprint of the HDL files on
disk and their file-to-file dependencies. Anything generated later —
PeakRDL output, `write_file` output, any other Bazel rule's stdout —
is invisible to it and never appears in the blueprint. The plugin
therefore can't compute a dep on it. Two mechanisms bridge the gap:

### `# keep` — pin a hand-added dep so gazelle preserves it

Two live examples of this in the workspace.

**Code-generator wrapper (`uart_regs/`).** A hand-authored
`verilog_system_rdl_library` (from
[`rules_systemrdl`](https://hw-bzl.github.io/rules_systemrdl/)) wraps
PeakRDL's regblock output. PeakRDL emits `uart_regs.sv` and
`uart_regs_pkg.sv` at build time; orbit never sees them, so blueprint
has no dep on this rule for `dsp_top.sv`'s `import uart_regs_pkg::*;`
and `uart_regs u_regs(...)`. `dsp/BUILD.bazel` pins the dep by hand:

```python
verilog_library(
    name = "dsp_top",
    ...
    deps = [
        "//uart_regs",  # keep
        ...
    ],
)
```

Building `:uart_regs` itself needs the rules_systemrdl toolchain
(PeakRDL + Python); `bazel run //:gazelle` doesn't.

**Preprocessor header (`preproc/`).** A `write_file` from
`bazel_skylib` emits `params.svh`, wrapped by a hand-authored
`verilog_library` via the `hdrs` attribute. `cpu/timer.sv`
`` `include``s the header — a preprocessor directive, not a design-unit
reference — so blueprint again has no dep. `cpu/BUILD.bazel` pins:

```python
verilog_library(
    name = "timer",
    ...
    deps = [
        "//preproc:params",  # keep
    ],
)
```

Gazelle's merger honors `# keep` on any mergeable list attribute
(`deps`, `vhdl_deps`, `verilog_deps`, `tags`), so the entry survives
every regen.

### `# gazelle:resolve` — redirect a computed dep to a different label

Also supported, though not exercised by a live example. Under blueprint
the resolver keys on workspace-relative filepaths. Format:

```
# gazelle:resolve orbit <workspace-relative-path> <label>
```

Example — if you wanted to redirect `cpu/alu.sv`'s dep on
`//vutils:reg_n` to a test-only mock:

```python
# cpu/BUILD.bazel (or any parent BUILD)
# gazelle:resolve orbit vutils/reg_n.v //some_mock:reg_n_mock
```

Directives inherit down the tree; put them at the workspace root for
cross-cutting overrides. `# keep` is the right pick when there's no
dep to redirect (blueprint doesn't emit one); `# gazelle:resolve` is
the right pick when blueprint *does* emit a dep and you want to
redirect it.

## Try it

```bash
bazel run //:gazelle
```

That's the only command. Orbit auto-syncs lockfiles as part of `orbit
analyze`, so you never have to run orbit by hand.

The generated `gates/BUILD.bazel`, `cpu/BUILD.bazel`, and `dsp/BUILD.bazel`
will all contain `deps = [...]` lists that point to the correct cross-
package labels — `//primitives:and_gate`, `//vutils:reg_n`, `:alu`, etc. —
derived from orbit's file-to-file blueprint of your HDL sources.

Re-run `bazel run //:gazelle` any time you add or remove sources; the
BUILD files (and lockfiles) will be kept in sync automatically.
