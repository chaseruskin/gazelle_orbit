package orbit

import (
	"encoding/json"
	"reflect"
	"testing"
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
