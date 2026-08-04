package orbit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/bazelbuild/bazel-gazelle/rule"
)

func TestAssignRuleNames(t *testing.T) {
	cases := []struct {
		name    string
		relSrcs []string
		want    []string
	}{
		{
			name:    "flat package uses bare names",
			relSrcs: []string{"foo.vhd", "bar.v"},
			want:    []string{"foo", "bar"},
		},
		{
			name:    "subdir sources default to bare names",
			relSrcs: []string{"subdir/foo.vhd", "bar.v"},
			want:    []string{"foo", "bar"},
		},
		{
			name:    "basename collision falls back to path-preserving names",
			relSrcs: []string{"a/foo.vhd", "b/foo.vhd"},
			want:    []string{"a/foo", "b/foo"},
		},
		{
			name:    "collision affects only the conflicting names",
			relSrcs: []string{"a/foo.vhd", "b/foo.vhd", "unique.v"},
			want:    []string{"a/foo", "b/foo", "unique"},
		},
		{
			name:    "collision across extensions still collides on bare name",
			relSrcs: []string{"foo.vhd", "sub/foo.v"},
			want:    []string{"foo", "sub/foo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assignRuleNames(tc.relSrcs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("assignRuleNames(%v) =\n  got %v\n want %v", tc.relSrcs, got, tc.want)
			}
		})
	}
}

func TestParseTagList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"no-lint", []string{"no-lint"}},
		{"no-lint,no-format", []string{"no-lint", "no-format"}},
		{"  no-lint ,   no-format  ", []string{"no-lint", "no-format"}},
		{"a,,b", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := parseTagList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseTagList(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestStaleGeneratedRules(t *testing.T) {
	// A tempdir stands in for the BUILD directory. Files that "still
	// exist" go here; missing files simply aren't created.
	dir := t.TempDir()
	writeFile := func(rel string) {
		t.Helper()
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("still_here.vhd")
	writeFile("sub/still_here.v")
	writeFile("regenerated.vhd")

	mkRule := func(kind, name string, srcs []string) *rule.Rule {
		r := rule.NewRule(kind, name)
		if srcs != nil {
			r.SetAttr("srcs", srcs)
		}
		return r
	}

	meta := mkRule(kindVhdlLibrary, "meta", nil)
	meta.SetAttr("deps", []string{"//other:lib"})

	existing := []*rule.Rule{
		// Sweep: our kind, single src, file removed.
		mkRule(kindVhdlLibrary, "removed_vhdl", []string{"gone.vhd"}),
		// Sweep: verilog counterpart.
		mkRule(kindVerilogLibrary, "removed_verilog", []string{"sub/gone.v"}),
		// Keep: our kind, single src, file still present.
		mkRule(kindVhdlLibrary, "still_here", []string{"still_here.vhd"}),
		mkRule(kindVerilogLibrary, "sub_still_here", []string{"sub/still_here.v"}),
		// Keep: hand-crafted meta-target (no srcs, deps aggregation).
		meta,
		// Keep: hand-crafted multi-src bundle — even with missing files.
		mkRule(kindVhdlLibrary, "bundle", []string{"still_here.vhd", "also_gone.vhd"}),
		// Keep: unrelated rule kind, even with a missing single src.
		mkRule("some_other_library", "not_ours", []string{"gone.vhd"}),
		// Keep: matches a src we're regenerating this run (merge handles it).
		mkRule(kindVhdlLibrary, "regenerated", []string{"regenerated.vhd"}),
		// Keep: single-src but the file happens to still be regenerated
		// this run under a *different* rule name — the merge index keys on
		// srcs, so it must not appear in Empty.
		mkRule(kindVhdlLibrary, "old_name_for_regenerated", []string{"regenerated.vhd"}),
	}

	owned := []ownedEntry{{relSrc: "regenerated.vhd"}}
	got := staleGeneratedRules(existing, owned, dir)

	var gotNames []string
	for _, r := range got {
		gotNames = append(gotNames, r.Name())
	}
	sort.Strings(gotNames)

	want := []string{"removed_verilog", "removed_vhdl"}
	if !reflect.DeepEqual(gotNames, want) {
		t.Errorf("staleGeneratedRules names = %v, want %v", gotNames, want)
	}

	// The stubs must carry the original rule's kind so Gazelle's Empty
	// matcher can find them by (kind, name) in the existing BUILD.
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name()] = r.Kind()
	}
	if byName["removed_vhdl"] != kindVhdlLibrary {
		t.Errorf("removed_vhdl kind = %q, want %q", byName["removed_vhdl"], kindVhdlLibrary)
	}
	if byName["removed_verilog"] != kindVerilogLibrary {
		t.Errorf("removed_verilog kind = %q, want %q", byName["removed_verilog"], kindVerilogLibrary)
	}
}

func TestBlueprintUnmarshal(t *testing.T) {
	raw := []byte(`[
  {
    "fileset": "VLOG",
    "library": "vutils",
    "filepath": "/tmp/vutils/counter.v",
    "dependencies": [
      "/tmp/vutils/reg_n.v"
    ]
  },
  {
    "fileset": "VHDL",
    "library": "gates",
    "filepath": "/tmp/gates/or_gate.vhd",
    "dependencies": []
  }
]`)

	var got []BlueprintEntry
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	want := []BlueprintEntry{
		{
			Fileset:      "VLOG",
			Library:      "vutils",
			Filepath:     "/tmp/vutils/counter.v",
			Dependencies: []string{"/tmp/vutils/reg_n.v"},
		},
		{
			Fileset:      "VHDL",
			Library:      "gates",
			Filepath:     "/tmp/gates/or_gate.vhd",
			Dependencies: []string{},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("blueprint parse mismatch\n got: %#v\nwant: %#v", got, want)
	}
}
