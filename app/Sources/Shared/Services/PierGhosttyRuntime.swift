import Foundation
import GhosttyKit
import CoreText

#if os(macOS)
import AppKit
#elseif os(iOS)
import UIKit
#endif

final class PierGhosttySurfaceContext: @unchecked Sendable {
    var surface: ghostty_surface_t?
    var onTitleChange: @MainActor (String) -> Void
    var onClose: @MainActor () -> Void
    var focusKeyboard: @MainActor () -> Void = {}
    let writeHandler: @Sendable (Data) -> Void
    let resizeHandler: @Sendable (Int, Int) -> Void

    init(
        onTitleChange: @escaping @MainActor (String) -> Void,
        onClose: @escaping @MainActor () -> Void = {},
        write: @escaping @Sendable (Data) -> Void = { _ in },
        resize: @escaping @Sendable (Int, Int) -> Void = { _, _ in }
    ) {
        self.onTitleChange = onTitleChange
        self.onClose = onClose
        writeHandler = write
        resizeHandler = resize
    }

    var opaquePointer: UnsafeMutableRawPointer {
        Unmanaged.passUnretained(self).toOpaque()
    }

    static func from(_ pointer: UnsafeMutableRawPointer?) -> PierGhosttySurfaceContext? {
        guard let pointer else { return nil }
        return Unmanaged<PierGhosttySurfaceContext>.fromOpaque(pointer).takeUnretainedValue()
    }
}

@MainActor
final class PierGhosttyRuntime {
    static let shared = PierGhosttyRuntime()

    private var config: ghostty_config_t?
    private(set) var app: ghostty_app_t?
    private(set) var initializationError: String?
    private(set) var theme: PierTerminalTheme?

    private init() {
        guard ghostty_init(UInt(CommandLine.argc), CommandLine.unsafeArgv) == GHOSTTY_SUCCESS else {
            initializationError = "Ghostty failed to initialize."
            return
        }

        let initialTheme = Self.initialTheme
        do {
            try Self.registerBundledTerminalFont()
            config = try Self.makeConfiguration(for: initialTheme)
            theme = initialTheme
        } catch {
            initializationError = error.localizedDescription
            return
        }

        guard let config else { return }

        var runtime = ghostty_runtime_config_s(
            userdata: Unmanaged.passUnretained(self).toOpaque(),
            supports_selection_clipboard: false,
            wakeup_cb: pierGhosttyWakeup,
            action_cb: pierGhosttyAction,
            read_clipboard_cb: pierGhosttyReadClipboard,
            confirm_read_clipboard_cb: pierGhosttyConfirmReadClipboard,
            write_clipboard_cb: pierGhosttyWriteClipboard,
            close_surface_cb: pierGhosttyCloseSurface
        )

        guard let app = ghostty_app_new(&runtime, config) else {
            initializationError = "Ghostty could not create its application runtime."
            return
        }
        self.app = app
    }

    func tick() {
        if let app { ghostty_app_tick(app) }
    }

    func apply(theme: PierTerminalTheme) {
        guard self.theme != theme, let app else { return }

        do {
            let updatedConfig = try Self.makeConfiguration(for: theme)
            ghostty_app_update_config(app, updatedConfig)
            ghostty_config_free(updatedConfig)
            self.theme = theme
        } catch {
            assertionFailure(error.localizedDescription)
        }
    }

    private static var initialTheme: PierTerminalTheme {
        switch PierAppearance.stored {
        case .light:
            return .light
        case .dark:
            return .dark
        case .system:
            #if os(macOS)
            let match = NSApp.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua])
            return match == .darkAqua ? .dark : .light
            #elseif os(iOS)
            return UIScreen.main.traitCollection.userInterfaceStyle == .dark ? .dark : .light
            #endif
        }
    }

    private static func registerBundledTerminalFont() throws {
        guard let url = Bundle.main.url(forResource: "JetBrainsMono-Variable", withExtension: "ttf") else {
            throw PierGhosttyConfigurationError.missingResource("JetBrains Mono")
        }

        // Registration is process-wide and harmless when another runtime has
        // already registered the bundled font.
        CTFontManagerRegisterFontsForURL(url as CFURL, .process, nil)
    }

    private static func makeConfiguration(for theme: PierTerminalTheme) throws -> ghostty_config_t {
        guard let url = Bundle.main.url(
            forResource: theme.configurationResource,
            withExtension: "conf"
        ) else {
            throw PierGhosttyConfigurationError.missingResource(theme.configurationResource)
        }
        guard let config = ghostty_config_new() else {
            throw PierGhosttyConfigurationError.creationFailed
        }

        url.path.withCString { ghostty_config_load_file(config, $0) }
        ghostty_config_finalize(config)

        let diagnostics = (0..<ghostty_config_diagnostics_count(config)).compactMap { index -> String? in
            guard let message = ghostty_config_get_diagnostic(config, index).message else { return nil }
            return String(cString: message)
        }
        guard diagnostics.isEmpty else {
            ghostty_config_free(config)
            throw PierGhosttyConfigurationError.invalid(diagnostics.joined(separator: "\n"))
        }
        return config
    }
}

private enum PierGhosttyConfigurationError: LocalizedError {
    case creationFailed
    case missingResource(String)
    case invalid(String)

    var errorDescription: String? {
        switch self {
        case .creationFailed:
            "Ghostty could not create its configuration."
        case .missingResource(let resource):
            "The bundled terminal resource “\(resource)” is missing."
        case .invalid(let details):
            "The bundled Ghostty configuration is invalid: \(details)"
        }
    }
}

// Ghostty invokes runtime callbacks from its own worker threads. Keeping these
// as file-level functions prevents Swift from inheriting @MainActor isolation
// from PierGhosttyRuntime.init before we explicitly hop to the main queue.
private func pierGhosttyWakeup(_ userdata: UnsafeMutableRawPointer?) {
    guard let userdata else { return }
    let runtime = Unmanaged<PierGhosttyRuntime>.fromOpaque(userdata).takeUnretainedValue()
    DispatchQueue.main.async {
        MainActor.assumeIsolated { runtime.tick() }
    }
}

private func pierGhosttyCloseSurface(_ userdata: UnsafeMutableRawPointer?, _ processAlive: Bool) {
    guard let context = PierGhosttySurfaceContext.from(userdata) else { return }
    Task { @MainActor in context.onClose() }
}

private func pierGhosttySurfaceContext(_ target: ghostty_target_s) -> PierGhosttySurfaceContext? {
    guard target.tag == GHOSTTY_TARGET_SURFACE, let surface = target.target.surface else { return nil }
    return PierGhosttySurfaceContext.from(ghostty_surface_userdata(surface))
}

private func pierGhosttyAction(
    _ app: ghostty_app_t?,
    _ target: ghostty_target_s,
    _ action: ghostty_action_s
) -> Bool {
    guard let context = pierGhosttySurfaceContext(target) else { return false }

    switch action.tag {
    case GHOSTTY_ACTION_SET_TITLE:
        guard let pointer = action.action.set_title.title else { return false }
        let title = String(cString: pointer)
        Task { @MainActor in context.onTitleChange(title) }
        return true

    case GHOSTTY_ACTION_OPEN_URL:
        guard let pointer = action.action.open_url.url else { return false }
        let urlString = String(cString: pointer)
        Task { @MainActor in
            guard let url = URL(string: urlString) else { return }
            #if os(macOS)
            NSWorkspace.shared.open(url)
            #elseif os(iOS)
            UIApplication.shared.open(url)
            #endif
        }
        return true

    case GHOSTTY_ACTION_SHOW_ON_SCREEN_KEYBOARD:
        Task { @MainActor in context.focusKeyboard() }
        return true

    default:
        return false
    }
}

private func pierGhosttyReadClipboard(
    _ userdata: UnsafeMutableRawPointer?,
    _ location: ghostty_clipboard_e,
    _ state: UnsafeMutableRawPointer?
) -> Bool {
    guard
        let context = PierGhosttySurfaceContext.from(userdata),
        let surface = context.surface,
        let state
    else { return false }

    let value: String? = pierGhosttyOnMain {
        #if os(macOS)
        NSPasteboard.general.string(forType: .string)
        #elseif os(iOS)
        UIPasteboard.general.string
        #endif
    }
    guard let value else { return false }
    value.withCString { pointer in
        ghostty_surface_complete_clipboard_request(surface, pointer, state, true)
    }
    return true
}

private func pierGhosttyConfirmReadClipboard(
    _ userdata: UnsafeMutableRawPointer?,
    _ value: UnsafePointer<CChar>?,
    _ state: UnsafeMutableRawPointer?,
    _ request: ghostty_clipboard_request_e
) {
    guard
        let context = PierGhosttySurfaceContext.from(userdata),
        let surface = context.surface,
        let value,
        let state
    else { return }
    ghostty_surface_complete_clipboard_request(surface, value, state, true)
}

private func pierGhosttyWriteClipboard(
    _ userdata: UnsafeMutableRawPointer?,
    _ location: ghostty_clipboard_e,
    _ content: UnsafePointer<ghostty_clipboard_content_s>?,
    _ count: Int,
    _ confirm: Bool
) {
    guard let content else { return }
    var value: String?
    for index in 0..<count {
        let item = content[index]
        guard
            let mime = item.mime,
            String(cString: mime) == "text/plain",
            let data = item.data
        else { continue }
        value = String(cString: data)
        break
    }
    guard let value else { return }
    pierGhosttyOnMain {
        #if os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
        #elseif os(iOS)
        UIPasteboard.general.string = value
        #endif
    }
}

private func pierGhosttyOnMain<Value: Sendable>(_ body: @escaping @MainActor () -> Value) -> Value {
    if Thread.isMainThread {
        return MainActor.assumeIsolated(body)
    }
    return DispatchQueue.main.sync {
        MainActor.assumeIsolated(body)
    }
}

func pierGhosttyModifiers(
    shift: Bool,
    control: Bool,
    option: Bool,
    command: Bool,
    capsLock: Bool = false
) -> ghostty_input_mods_e {
    var raw = GHOSTTY_MODS_NONE.rawValue
    if shift { raw |= GHOSTTY_MODS_SHIFT.rawValue }
    if control { raw |= GHOSTTY_MODS_CTRL.rawValue }
    if option { raw |= GHOSTTY_MODS_ALT.rawValue }
    if command { raw |= GHOSTTY_MODS_SUPER.rawValue }
    if capsLock { raw |= GHOSTTY_MODS_CAPS.rawValue }
    return ghostty_input_mods_e(rawValue: raw)
}

@discardableResult
func pierGhosttySendKey(
    surface: ghostty_surface_t,
    action: ghostty_input_action_e,
    keyCode: UInt32,
    text: String?,
    modifiers: ghostty_input_mods_e,
    composing: Bool = false
) -> Bool {
    let unshifted = text?.unicodeScalars.first?.value ?? 0
    if let text {
        return text.withCString { pointer in
            ghostty_surface_key(surface, ghostty_input_key_s(
                action: action,
                mods: modifiers,
                consumed_mods: GHOSTTY_MODS_NONE,
                keycode: keyCode,
                text: pointer,
                unshifted_codepoint: unshifted,
                composing: composing
            ))
        }
    }
    return ghostty_surface_key(surface, ghostty_input_key_s(
        action: action,
        mods: modifiers,
        consumed_mods: GHOSTTY_MODS_NONE,
        keycode: keyCode,
        text: nil,
        unshifted_codepoint: unshifted,
        composing: composing
    ))
}

@discardableResult
func pierGhosttySetFontSize(surface: ghostty_surface_t, points: Double) -> Bool {
    let points = PierTerminalFontSize.normalized(points)
    let action = String(
        format: "set_font_size:%.1f",
        locale: Locale(identifier: "en_US_POSIX"),
        points
    )
    return action.withCString { pointer in
        ghostty_surface_binding_action(surface, pointer, UInt(action.utf8.count))
    }
}
