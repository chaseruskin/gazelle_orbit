package orbit

import (
	"reflect"
	"testing"
)

func TestPathDepRegex(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     []string
	}{
		{
			name:     "no deps",
			manifest: `[package]` + "\n" + `name = "root"`,
			want:     nil,
		},
		{
			name: "single path dep",
			manifest: `[dependencies]` + "\n" +
				`gates = { path = "../gates" }`,
			want: []string{"../gates"},
		},
		{
			name: "path dep alongside other keys",
			manifest: `[dependencies]` + "\n" +
				`vutils = { path = "../vutils", version = "0.1.0" }`,
			want: []string{"../vutils"},
		},
		{
			name: "multiple path deps",
			manifest: `[dependencies]` + "\n" +
				`gates = { path = "../gates" }` + "\n" +
				`primitives = { path = "../primitives" }`,
			want: []string{"../gates", "../primitives"},
		},
		{
			name: "version-only dep is ignored",
			manifest: `[dependencies]` + "\n" +
				`foo = "1.2.3"`,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matches := pathDepRegex.FindAllStringSubmatch(tc.manifest, -1)
			var got []string
			for _, m := range matches {
				got = append(got, m[1])
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pathDepRegex on %q: got %v, want %v", tc.manifest, got, tc.want)
			}
		})
	}
}
