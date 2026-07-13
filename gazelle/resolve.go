package orbit

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/label"
	"github.com/bazelbuild/bazel-gazelle/repo"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

// HDL language tags used to drive Resolve-time cross-language routing.
// Derived from orbit's blueprint `fileset` for imported files, and from
// the rule kind for the consumer's own language.
const (
	languageVhdl    = "vhdl"
	languageVerilog = "verilog"
)

// filesetLanguage maps orbit's blueprint fileset to the HDL language
// we use to classify cross-language deps. Unknown filesets return "".
func filesetLanguage(fileset string) string {
	switch fileset {
	case "VHDL":
		return languageVhdl
	case "VLOG", "SYSV":
		return languageVerilog
	default:
		return ""
	}
}

// languageForKind returns the HDL language a rule of the given kind
// provides. Only the two kinds the plugin generates are recognized.
func languageForKind(kind string) string {
	switch kind {
	case kindVhdlLibrary:
		return languageVhdl
	case kindVerilogLibrary:
		return languageVerilog
	default:
		return ""
	}
}

// Imports implements resolve.Resolver. It tells Gazelle which import
// keys this rule provides — one per src file, using the file's
// workspace-relative path as the key. Resolve() looks up dependency
// filepaths against this index to find the label that owns each dep.
//
// Hand-authored rules whose HDL orbit can't see (code generators,
// build-time-emitted headers, vendor stubs) get no automatic wiring;
// consumers pin their deps by hand with `# keep` on the dep line.
func (*orbitLang) Imports(c *config.Config, r *rule.Rule, f *rule.File) []resolve.ImportSpec {
	srcs := r.AttrStrings("srcs")
	if len(srcs) == 0 {
		return nil
	}
	pkg := ""
	if f != nil {
		pkg = f.Pkg
	}
	specs := make([]resolve.ImportSpec, 0, len(srcs))
	for _, s := range srcs {
		key := workspaceRelPath(pkg, s)
		if key == "" {
			continue
		}
		specs = append(specs, resolve.ImportSpec{Lang: languageName, Imp: key})
	}
	return specs
}

// workspaceRelPath joins a Bazel package (workspace-relative) with a
// package-relative srcs entry to produce a single workspace-relative
// slash-separated path — the canonical key we index and resolve against.
func workspaceRelPath(pkg, src string) string {
	if src == "" {
		return ""
	}
	if pkg == "" {
		return src
	}
	return pkg + "/" + src
}

// Resolve implements resolve.Resolver. Each blueprint dependency is an
// absolute filepath; we normalize it to a workspace-relative path,
// look up the owning rule's label in the workspace index, and route
// same-language deps to `deps` vs. cross-language deps to
// `verilog_deps` / `vhdl_deps` based on fileset.
//
// A dep whose file isn't in any indexed rule (external orbit cache path,
// non-Bazel-visible file) is dropped silently — the user can add it by
// hand and mark it `# keep`.
func (*orbitLang) Resolve(c *config.Config, ix *resolve.RuleIndex, rc *repo.RemoteCache, r *rule.Rule, imports interface{}, from label.Label) {
	imps, ok := imports.(ruleImports)
	if !ok {
		return
	}

	ownLang := languageForKind(r.Kind())

	sameLangDeps := map[string]bool{}
	crossLangDeps := map[string]bool{}
	seenLabels := map[string]bool{}
	for _, depPath := range imps.deps {
		key := absToWorkspaceRel(depPath, c.RepoRoot)
		if key == "" {
			continue
		}
		lbl, ok := resolveDep(c, ix, key, from)
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

		// Dep's language is the language of the file itself (from the
		// blueprint's absolute filepath). No index walk needed —
		// blueprint's filesets are 1:1 with file extensions.
		depLang := languageForExt(filepath.Ext(depPath))
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

// absToWorkspaceRel converts an absolute filepath from a blueprint
// entry to a workspace-relative slash-separated path suitable for the
// resolve index. Returns "" for paths outside the workspace (e.g.
// entries in orbit's ~/.orbit/cache directory).
func absToWorkspaceRel(absPath, repoRoot string) string {
	if absPath == "" || repoRoot == "" {
		return ""
	}
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// resolveDep looks up the label that provides the given workspace-
// relative dependency file. Resolution order:
//  1. `# gazelle:resolve orbit <workspace-relative-path> <label>` override.
//  2. Workspace index lookup by the same key.
func resolveDep(c *config.Config, ix *resolve.RuleIndex, key string, from label.Label) (label.Label, bool) {
	spec := resolve.ImportSpec{Lang: languageName, Imp: key}
	if lbl, ok := resolve.FindRuleWithOverride(c, spec, languageName); ok {
		return lbl, true
	}
	results := ix.FindRulesByImportWithConfig(c, spec, languageName)
	for _, res := range results {
		if res.IsSelfImport(from) {
			continue
		}
		return res.Label, true
	}
	return label.NoLabel, false
}

// languageForExt returns the HDL language a file of the given extension
// belongs to, mirroring orbit's built-in fileset classification. Empty
// string for anything we don't recognize.
func languageForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".vhd", ".vhdl":
		return languageVhdl
	case ".v", ".vh", ".sv", ".svh":
		return languageVerilog
	default:
		return ""
	}
}

// setOrDelSortedAttr writes the sorted keys of `set` to the rule's
// attribute `attr` when non-empty, and deletes the attribute otherwise.
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
// Empty `ownLang` falls back to `deps`.
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
