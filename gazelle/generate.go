package orbit

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

// filesetKind maps orbit's fileset classification to the Bazel rule kind
// that should own a file of that fileset. Unrecognized filesets (custom
// project-defined ones) return "" and their entries are skipped.
func filesetKind(fileset string) string {
	switch fileset {
	case "VHDL":
		return kindVhdlLibrary
	case "VLOG", "SYSV":
		return kindVerilogLibrary
	default:
		return ""
	}
}

// ownedEntry is a blueprint entry the plugin has decided this BUILD
// owns (per the nearest-existing-ancestor rule), paired with its
// derived rule kind and package-relative srcs path.
type ownedEntry struct {
	entry  BlueprintEntry
	kind   string
	relSrc string
}

// GenerateRules implements language.Language.
//
// Strategy: for any directory we decide to "own" (per the placement
// rules below), invoke `orbit analyze --plan json --local --force` and
// translate the blueprint into vhdl_library / verilog_library rules —
// one per HDL source file, named after the file's basename (with any
// subdirectory prefix preserved and the extension stripped).
//
// Placement rule (the "nearest existing ancestor BUILD" rule): gazelle
// never invents a new sub-BUILD inside a subtree where an ancestor
// BUILD already owns the source files. This keeps `glob(["<subdir>/**"])`-
// style targets in the ancestor BUILD intact (no fragmenting of
// subtrees), which matters for vendor IP repos that ship as
// `vivado_packaged_ip(srcs = glob([...]))` and need a recursive glob to
// stay un-truncated.
//
// Cross-package dependencies (blueprint entries whose absolute filepath
// resolves to a file in another Bazel package) are left as raw filepath
// import specs and resolved to labels in Resolve().
//
// Cleanup: previously-generated rules whose source file was removed or
// renamed are marked stale via result.Empty. See staleGeneratedRules for
// the three-condition discriminator that keeps this from touching hand-
// authored rules.
func (*orbitLang) GenerateRules(args language.GenerateArgs) language.GenerateResult {
	cfg := getConfig(args.Config)
	if cfg.disabled {
		return language.GenerateResult{}
	}

	// One BUILD-presence cache shared across the placement walkers, so
	// every `hasBuildFile` lookup happens at most once per unique dir per
	// GenerateRules invocation.
	cache := buildFileCache{}

	// Placement gate. If this dir has no BUILD of its own AND some ancestor
	// already does, defer to the ancestor — its GenerateRules call collects
	// our HDL (and cleans up its stale rules) via the subtree walk below.
	if args.File == nil && anyAncestorHasBuild(args.Dir, args.Config.RepoRoot, cache) {
		return language.GenerateResult{}
	}

	result := language.GenerateResult{}
	var owned []ownedEntry

	// Skip the orbit-analyze call when the subtree has no HDL at all —
	// but keep going so the cleanup sweep below still gets a chance to
	// remove stale rules from a subtree whose last HDL file was just
	// deleted.
	if hasOwnedHDLInSubtree(args.Dir, cache) {
		entries, err := runOrbitAnalyze(cfg.orbitBin, args.Dir)
		if err != nil {
			// Preserve existing rules on analyze failure. A transient
			// orbit error (missing Orbit.toml mid-refactor, parse blowup,
			// etc.) shouldn't cascade into a mass rule-deletion.
			log.Printf("gazelle_orbit: skipping %s: %v", args.Rel, err)
			return language.GenerateResult{}
		}

		// Sort entries by filepath so generated rule order is deterministic
		// regardless of orbit's internal ordering.
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Filepath < entries[j].Filepath
		})

		// First pass: collect owned entries so we can compute rule names
		// in a single batch. Name-assignment needs to see every rule at
		// once to detect basename collisions (see assignRuleNames).
		for _, entry := range entries {
			kind := filesetKind(entry.Fileset)
			if kind == "" {
				// Custom / unrecognized fileset — nothing we can express
				// as a vhdl_library / verilog_library rule.
				continue
			}
			relSrc, ok := fileUnderBuildOwnership(entry.Filepath, args.Dir, cache)
			if !ok {
				continue
			}
			owned = append(owned, ownedEntry{entry: entry, kind: kind, relSrc: relSrc})
		}

		relSrcs := make([]string, 0, len(owned))
		for _, o := range owned {
			relSrcs = append(relSrcs, o.relSrc)
		}
		names := assignRuleNames(relSrcs)

		for i, o := range owned {
			entry := o.entry
			r := rule.NewRule(o.kind, names[i])
			r.SetAttr("srcs", []string{o.relSrc})
			r.SetAttr("library", entry.Library)
			// HDL libraries are almost always consumed across packages
			// (cpu/dsp depending on primitives, vutils, etc.) so we
			// default-emit public visibility. If a user wants narrower
			// scoping they can edit the BUILD file and Gazelle will leave
			// their override in place.
			r.SetAttr("visibility", []string{"//visibility:public"})
			if len(cfg.extraTags) > 0 {
				r.SetAttr("tags", append([]string(nil), cfg.extraTags...))
			}
			result.Gen = append(result.Gen, r)
			// Blueprint dependencies are absolute filepaths — pass them
			// through to Resolve() which maps each to the workspace label
			// whose srcs contain that file.
			result.Imports = append(result.Imports, ruleImports{
				deps: append([]string(nil), entry.Dependencies...),
			})
		}
	}

	// Cleanup sweep. Runs whether or not we just generated anything, so a
	// subtree emptied of HDL still gets its stale rules removed.
	if args.File != nil {
		result.Empty = staleGeneratedRules(args.File.Rules, owned, args.Dir)
	}

	return result
}

// staleGeneratedRules returns delete-stubs for rules in the current
// BUILD that were previously generated by this plugin but whose source
// file no longer exists on disk (i.e., the file was removed or renamed).
//
// A rule qualifies as "ours and stale" iff ALL of:
//  1. Its kind is one we generate (vhdl_library / verilog_library —
//     same predicate as resolve.go's languageForKind).
//  2. It has exactly one srcs entry — the plugin's shape. Hand-authored
//     meta-targets (srcs empty, deps only) and multi-file bundles fall
//     out here.
//  3. That srcs entry names a file that no longer exists under buildDir.
//     A hand-authored single-src rule pointing at a real file is left
//     alone; only rules whose backing file is gone get swept.
//
// `owned` names the entries we're regenerating this run; rules matching
// one of those are handled by Gazelle's normal merge and must not appear
// in Empty. Rules explicitly marked `# keep` are honored by Gazelle's
// merge phase and stay put even when returned here.
func staleGeneratedRules(existing []*rule.Rule, owned []ownedEntry, buildDir string) []*rule.Rule {
	generatedSrcs := make(map[string]bool, len(owned))
	for _, o := range owned {
		generatedSrcs[o.relSrc] = true
	}
	var out []*rule.Rule
	for _, r := range existing {
		kind := r.Kind()
		if languageForKind(kind) == "" {
			continue
		}
		srcs := r.AttrStrings("srcs")
		if len(srcs) != 1 {
			continue
		}
		if generatedSrcs[srcs[0]] {
			continue
		}
		absSrc := filepath.Join(buildDir, filepath.FromSlash(srcs[0]))
		if _, err := os.Stat(absSrc); err == nil {
			continue
		}
		out = append(out, rule.NewRule(kind, r.Name()))
	}
	return out
}

// ruleImports is the per-rule data GenerateRules attaches for Resolve()
// to consume. `deps` holds the blueprint's absolute-path dependency list
// verbatim; the consumer's own language is derived from the rule's kind
// (kindVhdlLibrary vs kindVerilogLibrary) at resolve time.
type ruleImports struct {
	deps []string
}

// stripExt drops the extension from a path, leaving the directory
// prefix (if any) intact: `subdir/foo.vhd` → `subdir/foo`, `foo.vhd`
// → `foo`.
func stripExt(s string) string {
	return strings.TrimSuffix(s, filepath.Ext(s))
}

// assignRuleNames picks a Bazel target name for each srcs-relative path,
// returned in the same order as `relSrcs`.
//
// Default is the source basename without extension, so `subdir/foo.vhd`
// becomes just `foo`. When two or more sources in the batch would
// collapse to the same bare name, the affected entries fall back to
// their path-preserving form (`subdir/foo`) so each remains unique.
// Non-conflicting entries in the same batch are unaffected.
func assignRuleNames(relSrcs []string) []string {
	counts := map[string]int{}
	for _, rs := range relSrcs {
		counts[stripExt(filepath.Base(rs))]++
	}
	out := make([]string, len(relSrcs))
	for i, rs := range relSrcs {
		bare := stripExt(filepath.Base(rs))
		if counts[bare] > 1 {
			out[i] = stripExt(rs)
		} else {
			out[i] = bare
		}
	}
	return out
}

// fileUnderBuildOwnership reports whether the absolute path `absFile`
// belongs to the BUILD at `buildDir` under the nearest-existing-
// ancestor-BUILD rule. If so, it returns the file's path relative to
// buildDir (always using forward slashes, valid as a Bazel srcs entry).
//
// A file belongs to buildDir iff every directory strictly between the
// file's parent and buildDir has NO `BUILD.bazel` / `BUILD` — the first
// intervening BUILD encountered while walking up owns the file instead.
func fileUnderBuildOwnership(absFile, buildDir string, cache buildFileCache) (string, bool) {
	absDir, err := filepath.Abs(buildDir)
	if err != nil {
		return "", false
	}
	absF, err := filepath.Abs(absFile)
	if err != nil {
		return "", false
	}
	dir := filepath.Dir(absF)
	rel, err := filepath.Rel(absDir, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	for dir != absDir {
		if cache.has(dir) {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
	relPath, err := filepath.Rel(absDir, absF)
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(relPath), true
}

// anyAncestorHasBuild reports whether any directory strictly between
// startDir (exclusive) and repoRoot (inclusive) holds a BUILD.bazel /
// BUILD. Used to decide whether GenerateRules should defer this dir's
// rules to an ancestor BUILD.
func anyAncestorHasBuild(startDir, repoRoot string, cache buildFileCache) bool {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return false
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return false
	}
	dir := filepath.Dir(abs)
	for {
		if cache.has(dir) {
			return true
		}
		if dir == root {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// buildFileCache memoises hasBuildFile lookups within a single
// GenerateRules call. The three walkers (anyAncestorHasBuild upward,
// fileUnderBuildOwnership upward-bounded, hasOwnedHDLInSubtree downward)
// all stat the same ancestor chains repeatedly when a project has many
// HDL files under one BUILD — the cache turns N walks × M ancestors
// of redundant `os.Stat` calls into one stat per unique dir per call.
type buildFileCache map[string]bool

func (c buildFileCache) has(dirAbs string) bool {
	if v, ok := c[dirAbs]; ok {
		return v
	}
	v := hasBuildFile(dirAbs)
	c[dirAbs] = v
	return v
}

// hasBuildFile reports whether dir contains a `BUILD.bazel` or `BUILD`
// file. Prefer the per-call `buildFileCache.has` over the raw form
// when called from a walk that will revisit the same ancestors.
func hasBuildFile(dirAbs string) bool {
	for _, name := range []string{"BUILD.bazel", "BUILD"} {
		if _, err := os.Stat(filepath.Join(dirAbs, name)); err == nil {
			return true
		}
	}
	return false
}

// hasOwnedHDLInSubtree reports whether any HDL source file under buildDir
// would be owned by buildDir per the nearest-existing-ancestor-BUILD rule
// — i.e., any HDL file in buildDir itself OR in a descendant subdirectory
// whose chain back up to buildDir contains no intervening BUILD.bazel /
// BUILD. Used as an early-out: if the answer is no, GenerateRules can
// return without running orbit analyze.
//
// The walk SKIPS any subdirectory that has its own BUILD.bazel / BUILD —
// those are their own packages and their HDL is owned by their own
// GenerateRules call.
func hasOwnedHDLInSubtree(buildDir string, cache buildFileCache) bool {
	found := false
	_ = filepath.WalkDir(buildDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if path != buildDir && cache.has(path) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if hdlExts[ext] {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// hdlExts lists every source extension the plugin classifies as HDL.
// Kept in one place because both the subtree-walk shortcut in
// hasOwnedHDLInSubtree and (should we ever need it again) source
// filtering elsewhere key off the same set. Matches orbit's built-in
// fileset extensions for VHDL / VLOG / SYSV.
var hdlExts = map[string]bool{
	".vhd":  true,
	".vhdl": true,
	".v":    true,
	".vh":   true,
	".sv":   true,
	".svh":  true,
}
