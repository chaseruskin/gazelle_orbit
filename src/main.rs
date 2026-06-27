use cliproc::*;
use orbit::Orbit;
use std::env;
use std::process::ExitCode;

fn main() -> ExitCode {
    Cli::default().parse(env::args()).go::<Orbit>()
}
