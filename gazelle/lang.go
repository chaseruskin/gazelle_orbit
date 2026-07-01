package orbit

import (
	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

const languageName = "orbit"

// Rule kinds we generate. Every plugin-generated rule is exactly one of
// `vhdl_library` or `verilog_library`, chosen from the file extensions of
// its srcs bucket. Cross-language dependencies are wired via the
// `verilog_deps` (on vhdl_library) and `vhdl_deps` (on verilog_library)
// attributes rather than through a third mixed-language rule kind — see
// the Resolve() path for the routing logic.
const (
	kindVhdlLibrary    = "vhdl_library"
	kindVerilogLibrary = "verilog_library"
)

// codegenLibraryKinds lists additional rule kinds we recognize as
// VerilogInfo / VhdlInfo providers for indexing purposes only — we never
// generate or merge these. Users authoring rules of these kinds can attach
// `tags = ["orbit_library=...", "orbit_unit=..."]` to participate in
// gazelle_orbit's workspace-wide dependency resolution without an extra
// vhdl_library / verilog_library wrapper.
//
// Each entry is treated as a read-only library kind: Imports() runs on it
// (so the rule shows up in the index), but GenerateRules() never produces
// one and merge never touches one.
var codegenLibraryKinds = []string{
	"verilog_system_rdl_library",
}

// orbitLang implements language.Language.
type orbitLang struct{}

// NewLanguage returns a new instance of the gazelle_orbit language plugin.
// This is the constructor that gazelle_binary's def.bzl wires into the
// language registry.
func NewLanguage() language.Language {
	return &orbitLang{}
}

// Name implements language.Language and resolve.Resolver.
func (*orbitLang) Name() string { return languageName }

// generatedKindInfo is the shared KindInfo we use for every kind the plugin
// generates and manages. Same shape for `vhdl_library` and `verilog_library`.
//
// `MatchAttrs` deliberately includes only `srcs`, not `library`. The match
// algorithm requires each attribute to either find exactly one candidate
// or none — `library` is a shared discriminator (e.g. every rule in a
// `k2space`-library package shares the same value), so listing it would
// fail every match with "multiple rules have the same attribute" and the
// generated rule would be silently dropped instead of appended as a new
// target. `srcs` is unique per rule (one file per `vhdl_library` /
// `verilog_library` convention) so it's a safe disambiguator.
var generatedKindInfo = rule.KindInfo{
	MatchAny:   false,
	MatchAttrs: []string{"srcs"},
	NonEmptyAttrs: map[string]bool{
		"srcs": true,
	},
	SubstituteAttrs: map[string]bool{},
	MergeableAttrs: map[string]bool{
		"srcs":         true,
		"deps":         true,
		"verilog_deps": true, // cross-language on vhdl_library
		"vhdl_deps":    true, // cross-language on verilog_library
		"library":      true,
		// `tags` is mergeable so the gen-rule's value (which already
		// includes a union of the existing tags + the `gazelle_orbit`
		// marker — see mergeOrbitTag in generate.go) overwrites the on-
		// disk value. The merge happens in GenerateRules rather than via
		// Gazelle's attr-level union so we can guarantee the marker is
		// always present without needing extra merger hooks.
		"tags": true,
	},
	ResolveAttrs: map[string]bool{
		"deps":         true,
		"verilog_deps": true,
		"vhdl_deps":    true,
	},
}

// Kinds implements language.Language.
func (*orbitLang) Kinds() map[string]rule.KindInfo {
	kinds := map[string]rule.KindInfo{
		kindVhdlLibrary:    generatedKindInfo,
		kindVerilogLibrary: generatedKindInfo,
	}
	// Read-only kinds: Gazelle will call Imports() on rules of these kinds
	// (letting the workspace-wide index see them) but the plugin won't
	// generate or merge anything for them.
	for _, k := range codegenLibraryKinds {
		kinds[k] = rule.KindInfo{
			MatchAny:        false,
			MatchAttrs:      nil,
			NonEmptyAttrs:   nil,
			SubstituteAttrs: nil,
			MergeableAttrs:  nil,
			ResolveAttrs:    nil,
		}
	}
	return kinds
}

// Loads implements language.Language. It's deprecated in favor of
// ApparentLoads on bzlmod but still required by the interface.
func (l *orbitLang) Loads() []rule.LoadInfo {
	return l.ApparentLoads(func(string) string { return "" })
}

// ApparentLoads implements language.ModuleAwareLanguage. We emit load
// statements for the two rule kinds the plugin manages:
//   - `vhdl_library` from `@rules_vhdl//vhdl:defs.bzl`
//   - `verilog_library` from `@rules_verilog//verilog:defs.bzl`
//
// Gazelle only writes the loads that match kinds actually present in the
// file, so a single-language BUILD ends up with one load and a mixed-
// language BUILD (post-refactor: two separate rules of different kinds,
// wired via `verilog_deps` / `vhdl_deps`) ends up with two.
func (*orbitLang) ApparentLoads(moduleToApparentName func(string) string) []rule.LoadInfo {
	rulesVhdl := moduleToApparentName("rules_vhdl")
	if rulesVhdl == "" {
		rulesVhdl = "rules_vhdl"
	}
	rulesVerilog := moduleToApparentName("rules_verilog")
	if rulesVerilog == "" {
		rulesVerilog = "rules_verilog"
	}
	return []rule.LoadInfo{
		{
			Name:    "@" + rulesVhdl + "//vhdl:defs.bzl",
			Symbols: []string{kindVhdlLibrary},
		},
		{
			Name:    "@" + rulesVerilog + "//verilog:defs.bzl",
			Symbols: []string{kindVerilogLibrary},
		},
	}
}

// Fix implements language.Language. No-op: cross-language deps now go to
// dedicated attrs (verilog_deps / vhdl_deps) so there's no rule-kind
// upgrade to perform.
func (*orbitLang) Fix(c *config.Config, f *rule.File) {}

// Embeds implements resolve.Resolver. HDL rules don't embed each other.
func (*orbitLang) Embeds(r *rule.Rule, from label.Label) []label.Label {
	return nil
}
