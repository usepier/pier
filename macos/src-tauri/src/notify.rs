//! Notifications through the system notification center, with clicks routed
//! back to the app: a click opens the session it is about. (The stock
//! notification plugin can't route clicks on macOS.) Only a bundled app may
//! use the notification center; a development build outside one says so and
//! the web view falls back to its own notifications.

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Mutex, OnceLock};

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::runtime::{Bool, NSObject, NSObjectProtocol, ProtocolObject};
use objc2::{define_class, msg_send, AllocAnyThread};
use objc2_foundation::{NSBundle, NSError, NSString};
use objc2_user_notifications::{
    UNAuthorizationOptions, UNMutableNotificationContent, UNNotification,
    UNNotificationPresentationOptions, UNNotificationRequest, UNNotificationResponse,
    UNNotificationSound, UNUserNotificationCenter, UNUserNotificationCenterDelegate,
};
use tauri::{AppHandle, Emitter, Manager};

static APP: OnceLock<AppHandle> = OnceLock::new();
/// Notification id → the session it is about.
static SESSIONS: OnceLock<Mutex<HashMap<String, String>>> = OnceLock::new();
static ASKED: AtomicBool = AtomicBool::new(false);

define_class!(
    // SAFETY: NSObject has no subclassing requirements, and Delegate has no
    // Drop.
    #[unsafe(super(NSObject))]
    #[name = "PierNotificationDelegate"]
    struct Delegate;

    unsafe impl NSObjectProtocol for Delegate {}

    unsafe impl UNUserNotificationCenterDelegate for Delegate {
        // Show banners while the app is frontmost too: the app only notifies
        // about sessions other than the one in view.
        #[unsafe(method(userNotificationCenter:willPresentNotification:withCompletionHandler:))]
        fn will_present(
            &self,
            _center: &UNUserNotificationCenter,
            _notification: &UNNotification,
            handler: &block2::DynBlock<dyn Fn(UNNotificationPresentationOptions)>,
        ) {
            handler.call((UNNotificationPresentationOptions::Banner
                | UNNotificationPresentationOptions::List
                | UNNotificationPresentationOptions::Sound,));
        }

        #[unsafe(method(userNotificationCenter:didReceiveNotificationResponse:withCompletionHandler:))]
        fn did_receive(
            &self,
            _center: &UNUserNotificationCenter,
            response: &UNNotificationResponse,
            handler: &block2::DynBlock<dyn Fn()>,
        ) {
            let id = response.notification().request().identifier().to_string();
            let session = sessions().lock().ok().and_then(|m| m.get(&id).cloned());
            if let (Some(app), Some(session)) = (APP.get(), session) {
                if let Some(w) = app.get_webview_window("main") {
                    let _ = w.show();
                    let _ = w.unminimize();
                    let _ = w.set_focus();
                }
                let _ = app.emit("notification-clicked", session);
            }
            handler.call(());
        }
    }
);

fn sessions() -> &'static Mutex<HashMap<String, String>> {
    SESSIONS.get_or_init(|| Mutex::new(HashMap::new()))
}

/// Whether this process may use the notification center: it must run from
/// an app bundle.
fn bundled() -> bool {
    NSBundle::mainBundle().bundlePath().to_string().ends_with(".app")
}

/// Installs the click handler. Call once at startup.
pub fn init(app: &AppHandle) {
    let _ = APP.set(app.clone());
    if !bundled() {
        return;
    }
    let delegate: Retained<Delegate> = unsafe { msg_send![super(Delegate::alloc().set_ivars(())), init] };
    let center = UNUserNotificationCenter::currentNotificationCenter();
    center.setDelegate(Some(ProtocolObject::from_ref(&*delegate)));
    // The center holds its delegate weakly: keep it for the app's life.
    std::mem::forget(delegate);
}

/// Shows a notification; a click on it emits "notification-clicked" with
/// the session id. False when this build can't use the notification
/// center (the caller falls back).
#[tauri::command]
pub fn notify(id: String, title: String, body: String, session: String) -> bool {
    if !bundled() {
        return false;
    }
    if let Ok(mut m) = sessions().lock() {
        if m.len() > 500 {
            m.clear();
        }
        m.insert(id.clone(), session);
    }
    let content = UNMutableNotificationContent::new();
    content.setTitle(&NSString::from_str(&title));
    content.setBody(&NSString::from_str(&body));
    content.setSound(Some(&UNNotificationSound::defaultSound()));
    let request = UNNotificationRequest::requestWithIdentifier_content_trigger(
        &NSString::from_str(&id),
        &content,
        None,
    );
    let center = UNUserNotificationCenter::currentNotificationCenter();
    if ASKED.swap(true, Ordering::SeqCst) {
        center.addNotificationRequest_withCompletionHandler(&request, None);
        return true;
    }
    // The first notification asks for permission, then goes out.
    let block = RcBlock::new(move |granted: Bool, _err: *mut NSError| {
        if granted.as_bool() {
            UNUserNotificationCenter::currentNotificationCenter()
                .addNotificationRequest_withCompletionHandler(&request, None);
        }
    });
    center.requestAuthorizationWithOptions_completionHandler(
        UNAuthorizationOptions::Alert | UNAuthorizationOptions::Sound | UNAuthorizationOptions::Badge,
        &block,
    );
    true
}
