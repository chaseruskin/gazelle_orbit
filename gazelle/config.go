package orbit

import (
	"flag"
	"path/filepath"
	"strings"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/rule"
)

// orbitConfig is the per-directory configuration state for the plugin.
// Created fresh at the root and inherited (then mutated) for each
// subdirectory via Configure(). The state lives in config.Config.Exts
// under languageName.
type orbitConfig struct {
	// orbitBin is the path to the orbit executable. May be empty, in which
	// case "orbit" is looked up on $PATH.
	orbitBin string

	// disabled disables rule generation entirely for this directory subtree.
	disabled bool

	// extraTags is the list of tags applied to every rule generated in
	// this directory (and inherited subtrees, until overridden). Populated
	// by the `# gazelle:orbit_tags a,b,c` directive; empty by default.
	extraTags []string
}

func defaultConfig() *orbitConfig {
	return &orbitConfig{}
}

func (c *orbitConfig) clone() *orbitConfig {
	return &orbitConfig{
		orbitBin:  c.orbitBin,
		disabled:  c.disabled,
		extraTags: append([]string(nil), c.extraTags...),
	}
}

// getConfig returns the orbit config for c, creating a default if absent.
func getConfig(c *config.Config) *orbitConfig {
	if v, ok := c.Exts[languageName]; ok {
		return v.(*orbitConfig)
	}
	cfg := defaultConfig()
	c.Exts[languageName] = cfg
	return cfg
}

// RegisterFlags implements config.Configurer.
func (*orbitLang) RegisterFlags(fs *flag.FlagSet, cmd string, c *config.Config) {
	cfg := getConfig(c)
	fs.StringVar(&cfg.orbitBin, "orbit_bin", "", "Path to the orbit executable. Defaults to looking up `orbit` on $PATH.")
}

// CheckFlags implements config.Configurer.
func (*orbitLang) CheckFlags(fs *flag.FlagSet, c *config.Config) error {
	if cfg, ok := c.Exts[languageName]; ok {
		oc := cfg.(*orbitConfig)
		if oc.orbitBin != "" {
			abs, err := filepath.Abs(oc.orbitBin)
			if err == nil {
				oc.orbitBin = abs
			}
		}
	}
	return nil
}

// KnownDirectives implements config.Configurer. Plugin directives:
//
//   - `# gazelle:orbit_disable true` — skip rule generation in this
//     subtree.
//   - `# gazelle:orbit_tags <comma-separated>` — apply extra tags to
//     every rule the plugin generates in this dir and its descendants.
//     Comma-separated, whitespace-trimmed; empty value clears any
//     inherited list. Tags are additive: removing the directive later
//     won't retroactively strip tags a prior run committed.
//
// Note: Gazelle's built-in `# gazelle:resolve orbit <import> <label>`
// directive is the canonical way to override how a specific dep resolves
// (imports are workspace-relative filepaths under the blueprint backing).
// The resolver consults it in resolve.go via resolve.FindRuleWithOverride,
// so we don't declare it here.
func (*orbitLang) KnownDirectives() []string {
	return []string{
		"orbit_disable",
		"orbit_tags",
	}
}

// Configure implements config.Configurer. It is called for each
// directory during traversal; we inherit (clone) the parent's config
// then apply any directives found in this directory's BUILD file.
func (*orbitLang) Configure(c *config.Config, rel string, f *rule.File) {
	parent := getConfig(c)
	cfg := parent.clone()
	c.Exts[languageName] = cfg

	if f == nil {
		return
	}
	for _, d := range f.Directives {
		switch d.Key {
		case "orbit_disable":
			cfg.disabled = strings.EqualFold(strings.TrimSpace(d.Value), "true")
		case "orbit_tags":
			// Each occurrence REPLACES the list for this dir + descendants
			// (Gazelle convention). Comma-separated, whitespace-trimmed;
			// an empty value clears any inherited tags for this subtree.
			cfg.extraTags = parseTagList(d.Value)
		}
	}
}

// parseTagList splits a comma-separated directive value into trimmed,
// non-empty tags. Returns nil for empty/whitespace-only input so downstream
// callers can compare against nil to detect "no extra tags".
func parseTagList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		t := strings.TrimSpace(part)
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	return out
}
