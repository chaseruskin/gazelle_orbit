// Package orbit contains the Gazelle language plugin (gazelle_orbit) that
// generates vhdl_library and verilog_library targets from HDL sources by
// invoking the bundled orbit binary as a blueprint backend.
package orbit

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// BlueprintEntry is one file entry from `orbit analyze --plan json --local
// --force`. One entry per HDL source file in the current project, with
// resolved absolute filepaths for every direct dependency (local or
// external — orbit's blueprint doesn't discriminate).
//
// Fileset is orbit's classification of the file: "VHDL", "VLOG"
// (Verilog), "SYSV" (SystemVerilog), or a project-defined custom set.
// Only the built-in HDL filesets drive plugin-generated rules; custom
// filesets are ignored.
type BlueprintEntry struct {
	Fileset      string   `json:"fileset"`
	Library      string   `json:"library"`
	Filepath     string   `json:"filepath"`
	Dependencies []string `json:"dependencies"`
}

// resolveOrbitBin picks the orbit executable to invoke. Priority:
//  1. explicit --orbit_bin flag (treated as a direct path, then as an
//     rlocationpath, in that order).
//  2. the rlocationpath baked in by the build system (orbitBinRlocation).
//  3. "orbit" on $PATH.
func resolveOrbitBin(orbitBinFlag string) string {
	if orbitBinFlag != "" {
		if _, err := os.Stat(orbitBinFlag); err == nil {
			return orbitBinFlag
		}
		if resolved, err := runfiles.Rlocation(orbitBinFlag); err == nil {
			return resolved
		}
	}
	if orbitBinRlocation != "" {
		if resolved, err := runfiles.Rlocation(orbitBinRlocation); err == nil {
			return resolved
		}
	}
	return "orbit"
}

// orbitBinRlocation is the runfiles-relative path to the orbit binary that
// gets shipped with this plugin. It is overridden at link time via the
// go_library's x_defs (see //gazelle:BUILD.bazel). At runtime we resolve it
// through the bazel runfiles library to get a real filesystem path.
//
// When the value is empty (e.g. when the plugin is built outside Bazel) we
// fall back to the orbit_bin flag, then to "orbit" on $PATH.
var orbitBinRlocation = ""

// runOrbitAnalyze invokes `orbit analyze --plan json --local --force` in
// dir and parses the returned blueprint. orbit auto-syncs lockfiles
// (including for path-based dependency projects that don't have their own
// lockfile yet), so no separate `orbit lock` step is needed.
//
// --local: only report entries for files in the current project;
// cross-project dependencies still appear as filepaths in each entry's
// `dependencies` list (letting the plugin route them to the right Bazel
// package's target).
//
// --force: emit a (possibly-incomplete) blueprint even when there are
// HDL parse errors, so a single broken file doesn't halt BUILD-file
// generation for the whole project.
func runOrbitAnalyze(orbitBin, dir string) ([]BlueprintEntry, error) {
	bin := resolveOrbitBin(orbitBin)

	cmd := exec.Command(bin, "-C", dir, "analyze", "--plan", "json", "--local", "--force")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("orbit analyze failed in %s: %w\nstderr: %s", dir, err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("orbit analyze failed in %s: %w", dir, err)
	}
	var entries []BlueprintEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse orbit blueprint JSON: %w", err)
	}
	return entries, nil
}
