package orbit

import (
	"encoding/json"
	"reflect"
	"testing"
)

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
