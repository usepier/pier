//! The native shell around the web UI. It stays thin on purpose: pier's
//! logic lives in the bundled `pier api` sidecar, which the web view spawns
//! and talks to directly. What is here is only what JavaScript can't do.

use tauri::{Manager, RunEvent, WindowEvent};
use tauri_plugin_window_state::StateFlags;

pub fn run() {
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_process::init())
        .plugin(
            // Restore size and position, never visibility: a window hidden at
            // quit must not come back hidden.
            tauri_plugin_window_state::Builder::new()
                .with_state_flags(StateFlags::all() & !StateFlags::VISIBLE)
                .build(),
        )
        .on_window_event(|window, event| {
            // Closing the window hides it, like a Mac app: sessions keep being
            // watched (badge, notifications) until the app quits.
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .build(tauri::generate_context!())
        .expect("pier failed to start");

    app.run(|handle, event| {
        // A Dock click with no visible window brings the window back.
        if let RunEvent::Reopen { has_visible_windows: false, .. } = event {
            if let Some(w) = handle.get_webview_window("main") {
                let _ = w.show();
                let _ = w.set_focus();
            }
        }
    });
}
