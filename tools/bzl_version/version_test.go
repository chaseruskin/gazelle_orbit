// Package bzl_version_test asserts that the module's version string is
// consistent between version.bzl (single source of truth consumed by the
// release pipeline) and MODULE.bazel (what bzlmod resolves against).
package bzl_version_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

var (
	versionBzlRe  = regexp.MustCompile(`VERSION = "([\w.]+)"`)
	moduleBazelRe = regexp.MustCompile(`module\(\s*name = "gazelle_orbit",\s*version = "([\w.]+)"`)
)

func readVersion(t *testing.T, rlocationpath string, re *regexp.Regexp) string {
	t.Helper()
	path, err := runfiles.Rlocation(rlocationpath)
	if err != nil {
		t.Fatalf("Rlocation(%q): %v", rlocationpath, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	m := re.FindStringSubmatch(string(data))
	if m == nil {
		t.Fatalf("no version match in %q with regex %q", rlocationpath, re)
	}
	return m[1]
}

func TestVersionsInSync(t *testing.T) {
	bzl := readVersion(t, "gazelle_orbit/version.bzl", versionBzlRe)
	mod := readVersion(t, "gazelle_orbit/MODULE.bazel", moduleBazelRe)
	if bzl != mod {
		t.Fatalf("version drift: version.bzl=%q MODULE.bazel=%q — bump both together", bzl, mod)
	}
}
