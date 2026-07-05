package orbit

import (
	"sort"
	"strings"
	"sync"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/repo"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

// HDL language tags used to drive Resolve-time cross-language routing.
// A rule's language is derived from its kind: `vhdl_library` provides
// `languageVhdl`, `verilog_library` provides `languageVerilog`. Cross-
// language deps (where the consumer's language differs from the dep's)
// get routed to `verilog_deps` (on vhdl_library) or `vhdl_deps` (on
// verilog_library) rather than the plain `deps` attr.
const (
	languageVhdl    = "vhdl"
	languageVerilog = "verilog"
)

// labelLanguages tracks which HDL language each generated rule provides.
// Imports() writes to it for every plugin-managed rule it sees in the
// workspace; Resolve() reads from it to classify the language of each
// resolved dep label and route it to the appropriate deps attribute.
//
// Keys are normalized to "pkg:name" (no repo prefix) because Imports()
// doesn't see the apparent repo name attached to its rules while Resolve()
// gets labels with the consumer's repo baked in. Cross-workspace cross-
// language refs aren't a real concern (different workspaces have separate
// HDL dep graphs); within a single workspace, pkg:name uniquely identifies
// each rule.
//
// Gazelle's processing model serializes Imports() across all rules before
// any Resolve() call runs, so by the time we read this map every relevant
// entry is present. We use sync.Map so the Imports phase can run
// concurrently across packages without an explicit mutex.
var labelLanguages sync.Map // map[string]string — "pkg:name" → "vhdl" | "verilog"

// labelKey produces the normalized key used by labelLanguages — strips the
// repo prefix so Imports-time and Resolve-time labels match regardless of
// whether the apparent repo name was attached.
func labelKey(lbl label.Label) string {
	return lbl.Pkg + ":" + lbl.Name
}

// recordLabelLanguage stores the language a rule's label provides.
// Called from Imports(); overwrites previous entries (idempotent on
// re-resolve). Empty language is skipped so unknown-kind rules don't
// pollute the index.
func recordLabelLanguage(lbl label.Label, lang string) {
	if lang == "" {
		return
	}
	labelLanguages.Store(labelKey(lbl), lang)
}

// lookupLabelLanguage returns the language associated with lbl, or empty
// string if the label wasn't indexed (typically because it's an external
// label or a hand-authored rule not in our codegen list).
func lookupLabelLanguage(lbl label.Label) string {
	v, ok := labelLanguages.Load(labelKey(lbl))
	if !ok {
		return ""
	}
	return v.(string)
}

// languageForKind returns the HDL language a rule of the given kind
// provides. Codegen kinds (e.g. `verilog_system_rdl_library` — Verilog/SV
// output from SystemRDL) are also mapped here so their outputs are
// classified correctly for cross-language routing decisions.
func languageForKind(kind string) string {
	switch kind {
	case kindVhdlLibrary:
		return languageVhdl
	case kindVerilogLibrary, "verilog_system_rdl_library":
		return languageVerilog
	default:
		return ""
	}
}

// labelFromRule constructs the canonical label for a rule given the file it
// lives in. Mirrors how Bazel forms labels: `//<f.Pkg>:<r.Name()>` in the
// main repo, no apparent-name prefix.
func labelFromRule(r *rule.Rule, f *rule.File) label.Label {
	pkg := ""
	if f != nil {
		pkg = f.Pkg
	}
	return label.New("", pkg, r.Name())
}

// ruleImport is the per-import data we attached to each generated rule in
// GenerateRules; Resolve() receives this as the `imports interface{}`.
//
// Library: the qualifying library name from the HDL source (e.g. "ieee" or
// the work library), or empty for unqualified Verilog refs.
// Name: the design-unit name being referenced.
// OriginLib: the HDL library of the rule that owns this import — used to
// resolve the VHDL `work` library back to a concrete library name.
type ruleImport struct {
	Library   string
	Name      string
	OriginLib string
}

// Imports implements resolve.Resolver. It tells Gazelle which import keys
// this rule provides (so other rules' refs can be matched to it).
//
// We index each design unit twice: once unqualified ("<unit_name>") for
// Verilog-style instantiations, and once library-qualified
// ("<library>.<unit_name>") for VHDL use-clauses.
//
// Three sources contribute unit names:
//  1. The private attr stashed by GenerateRules — authoritative for rules
//     the plugin emitted.
//  2. `orbit_unit=<name>` entries in the rule's `tags` attribute — the
//     mechanism a hand-author uses to declare design units provided by
//     a wrapper rule around a code generator (e.g. SystemRDL → vhdl_library).
//  3. The rule's `name` — used either as the sole unit name when no other
//     source is present, or always merged in alongside the others so the
//     conventional case (one rule == one unit) needs no annotation.
func (*orbitLang) Imports(c *config.Config, r *rule.Rule, f *rule.File) []resolve.ImportSpec {
	// Both vhdl_library and verilog_library (rules_verilog v1.3.0+) have
	// a public `library` attr. Hand-authored rules that predate the
	// verilog_library.library attr — or targets that want a library name
	// different from what's set on the rule — can still declare it via
	// `tags = ["orbit_library=<name>"]` for backwards compatibility.
	library := strings.ToLower(r.AttrString("library"))
	if library == "" {
		for _, tag := range r.AttrStrings("tags") {
			if rest, ok := strings.CutPrefix(tag, tagOrbitLibraryPrefix); ok {
				library = strings.ToLower(rest)
				break
			}
		}
	}

	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		n = strings.ToLower(n)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		names = append(names, n)
	}

	if pluginNames, ok := r.PrivateAttr(unitNamesAttr).([]string); ok {
		for _, n := range pluginNames {
			add(n)
		}
	}
	for _, tag := range r.AttrStrings("tags") {
		if rest, ok := strings.CutPrefix(tag, tagOrbitUnitPrefix); ok {
			add(rest)
		}
	}
	add(r.Name())

	specs := make([]resolve.ImportSpec, 0, len(names)*2)
	for _, n := range names {
		specs = append(specs, resolve.ImportSpec{Lang: languageName, Imp: n})
		if library != "" {
			specs = append(specs, resolve.ImportSpec{
				Lang: languageName,
				Imp:  library + "." + n,
			})
		}
	}

	// Record this rule's language for Resolve-time cross-language routing.
	recordLabelLanguage(labelFromRule(r, f), languageForKind(r.Kind()))

	return specs
}

// tagOrbitUnitPrefix is the prefix used inside a rule's `tags = [...]` to
// declare additional design-unit names the rule provides. Used by
// hand-authored wrappers around code generators where the rule name only
// names one of several emitted units.
//
// Example:
//
//	vhdl_library(
//	    name = "uart_regs",
//	    srcs = [":uart_regs_src"],
//	    library = "uart_regs",
//	    tags = [
//	        "orbit_unit=uart_regs_pkg",
//	        "orbit_unit=uart_regs_decoder",
//	    ],
//	)
//
// Downstream HDL that says `use uart_regs.uart_regs_pkg;` (or any of the
// listed units) will resolve to this rule.
const tagOrbitUnitPrefix = "orbit_unit="

// tagOrbitLibraryPrefix declares the HDL library a hand-authored rule
// belongs to. Kept for backward compatibility with rules that predate
// rules_verilog's public `library` attr — new rules should just set
// `library = "..."` directly.
const tagOrbitLibraryPrefix = "orbit_library="

// Resolve implements resolve.Resolver. For each import on the rule, we look
// up a matching Bazel label (either from a configured stdlib mapping or by
// querying the workspace index) and route it to the appropriate deps
// attribute based on cross-language classification:
//
//   - Same-language deps → `deps` (vhdl_library.deps holds VhdlInfo entries;
//     verilog_library.deps holds VerilogInfo entries).
//   - Cross-language deps → `verilog_deps` on vhdl_library (VerilogInfo
//     entries) or `vhdl_deps` on verilog_library (VhdlInfo entries).
//
// A dep with unknown language (external repo, hand-authored rule outside
// our codegen list, or a codegen kind with no `languageForKind` mapping)
// is conservatively routed to `deps` — the caller can hand-edit if that's
// wrong.
func (*orbitLang) Resolve(c *config.Config, ix *resolve.RuleIndex, rc *repo.RemoteCache, r *rule.Rule, imports interface{}, from label.Label) {
	imps, ok := imports.([]ruleImport)
	if !ok || len(imps) == 0 {
		return
	}

	ownLang := languageForKind(r.Kind())

	sameLangDeps := map[string]bool{}
	crossLangDeps := map[string]bool{}
	seenLabels := map[string]bool{}
	for _, imp := range imps {
		lbl, ok := resolveImport(c, ix, imp, from)
		if !ok {
			continue
		}
		if lbl == from || (lbl.Pkg == from.Pkg && lbl.Name == from.Name) {
			continue
		}
		absLbl := lbl.String()
		if seenLabels[absLbl] {
			continue
		}
		seenLabels[absLbl] = true

		depLang := lookupLabelLanguage(lbl)
		relDep := lbl.Rel(from.Repo, from.Pkg).String()

		// Route to the right attr:
		//   - unknown ownLang (rare — e.g. hand-authored rule of an
		//     unrecognized kind): stay conservative, use `deps`.
		//   - unknown depLang (external or non-plugin rule): treat as
		//     same-language — user can override.
		//   - matching languages: `deps`.
		//   - differing languages: cross-language attr.
		if ownLang == "" || depLang == "" || depLang == ownLang {
			sameLangDeps[relDep] = true
		} else {
			crossLangDeps[relDep] = true
		}
	}

	// Always write both deps attrs — even when empty — so a dep that
	// resolved on a previous run and no longer resolves gets removed
	// instead of surviving as a stale entry. Gazelle's ResolveAttrs
	// contract expects the plugin to publish the full set on every call.
	setOrDelSortedAttr(r, "deps", sameLangDeps)
	setOrDelSortedAttr(r, crossLangAttrFor(ownLang), crossLangDeps)
}

// setOrDelSortedAttr writes the sorted keys of `set` to the rule's
// attribute `attr` when non-empty, and deletes the attribute otherwise.
// Deleting on empty (rather than writing `attr = []`) both keeps the
// on-disk BUILD file clean and clears stale values left by a previous
// resolve.
func setOrDelSortedAttr(r *rule.Rule, attr string, set map[string]bool) {
	if len(set) == 0 {
		r.DelAttr(attr)
		return
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	r.SetAttr(attr, out)
}

// crossLangAttrFor returns the attr name a rule with own-language `ownLang`
// should use for cross-language deps.
//   - a vhdl_library carries Verilog deps in `verilog_deps`
//   - a verilog_library carries VHDL deps in `vhdl_deps`
//
// Empty `ownLang` falls back to `deps` (conservative — caller already
// avoids cross-lang routing when ownLang is unknown).
func crossLangAttrFor(ownLang string) string {
	switch ownLang {
	case languageVhdl:
		return "verilog_deps"
	case languageVerilog:
		return "vhdl_deps"
	default:
		return "deps"
	}
}

// resolveImport finds the Bazel label that provides imp.
//
// Resolution order:
//  1. Gazelle's canonical `# gazelle:resolve orbit <import> <label>` override,
//     via resolve.FindRuleWithOverride. Used for vendor IP, external
//     stdlibs, and any "this import maps to this label" mapping.
//  2. Library-qualified workspace index lookup ("<library>.<unit>"). For
//     VHDL refs with library "work", substitute the origin rule's library
//     first.
//  3. Unqualified workspace index lookup ("<unit>") — typical Verilog.
func resolveImport(c *config.Config, ix *resolve.RuleIndex, imp ruleImport, from label.Label) (label.Label, bool) {
	library := imp.Library
	if strings.EqualFold(library, "work") {
		library = imp.OriginLib
	}

	qualifiedKey := strings.ToLower(library + "." + imp.Name)
	unqualifiedKey := strings.ToLower(imp.Name)

	// 1. Honor `# gazelle:resolve orbit <import> <label>` overrides. Try
	// the qualified form first, then unqualified — so users can override
	// at whichever granularity makes sense.
	for _, key := range []string{qualifiedKey, unqualifiedKey} {
		if key == "" {
			continue
		}
		if lbl, ok := resolve.FindRuleWithOverride(c, resolve.ImportSpec{
			Lang: languageName,
			Imp:  key,
		}, languageName); ok {
			return lbl, true
		}
	}

	// 2. Library-qualified workspace index lookup.
	if library != "" {
		results := ix.FindRulesByImportWithConfig(c, resolve.ImportSpec{
			Lang: languageName,
			Imp:  qualifiedKey,
		}, languageName)
		if r, ok := pickFirstNonSelf(results, from); ok {
			return r, true
		}
	}

	// 3. Unqualified workspace index lookup.
	results := ix.FindRulesByImportWithConfig(c, resolve.ImportSpec{
		Lang: languageName,
		Imp:  unqualifiedKey,
	}, languageName)
	if r, ok := pickFirstNonSelf(results, from); ok {
		return r, true
	}

	return label.NoLabel, false
}

// pickFirstNonSelf returns the first label that is not a self-import.
func pickFirstNonSelf(results []resolve.FindResult, from label.Label) (label.Label, bool) {
	for _, res := range results {
		if res.IsSelfImport(from) {
			continue
		}
		return res.Label, true
	}
	return label.NoLabel, false
}
