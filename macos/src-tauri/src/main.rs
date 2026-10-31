// Prevents an extra console window on Windows release builds; harmless on macOS.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    pier_app_lib::run()
}
