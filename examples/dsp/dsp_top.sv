// Top-level DSP block: streams two bit inputs through the multiplier, keeps
// a sample count, and exposes a control register from a SystemRDL-generated
// register file in the //uart_regs package.
import uart_regs_pkg::*;

module dsp_top (
  input  logic       clk,
  input  logic       rst_n,
  input  logic       sample_a,
  input  logic       sample_b,
  input  logic       reg_wr_en,
  input  logic       reg_wr_data,
  output logic       partial_q,
  output logic [7:0] sample_count,
  output logic       regs_ready
);
  multiplier u_mul (
    .clk(clk), .rst_n(rst_n), .a(sample_a), .b(sample_b), .partial_q(partial_q)
  );

  counter #(.WIDTH(8)) u_count (
    .clk(clk), .rst_n(rst_n), .enable(1'b1), .count(sample_count)
  );

  // Generated register block from //uart_regs:uart_regs.
  //
  // Both `import uart_regs_pkg::*;` above and `uart_regs u_regs (...)`
  // below refer to the PeakRDL-generated .sv files. Orbit runs before
  // Bazel builds anything, so it never sees those files — the deps
  // don't appear in the blueprint. `dsp/BUILD.bazel` pins the dep on
  // this rule with `# keep` on the dep line.
  uart_regs u_regs (
    .clk(clk), .rst_n(rst_n),
    .wr_en(reg_wr_en), .wr_data(reg_wr_data),
    .ready(regs_ready)
  );
endmodule
