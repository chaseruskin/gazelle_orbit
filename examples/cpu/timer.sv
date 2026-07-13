// Small tick counter whose width comes from a build-time-generated header.
//
// `params.svh` is emitted by //preproc:params (a `write_file` rule); orbit
// runs at gazelle time so it never sees the generated header, and there's
// no module-level reference for it to hook into (`` `include`` is a
// preprocessor directive, not a design-unit ref). The dep on
// //preproc:params is pinned in cpu/BUILD.bazel with `# keep` — the
// canonical Gazelle idiom for holding a dep that resolve can't compute.
`include "params.svh"

module timer (
  input  logic                  clk,
  input  logic                  rst_n,
  output logic [`GEN_WIDTH-1:0] tick_count
);
  always_ff @(posedge clk or negedge rst_n) begin
    if (!rst_n) tick_count <= '0;
    else        tick_count <= tick_count + 1'b1;
  end
endmodule
