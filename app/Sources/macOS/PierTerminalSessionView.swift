#if os(macOS)
import AppKit
import GhosttyKit
import SwiftUI

struct PierTerminalSessionView: NSViewRepresentable {
    @AppStorage(PierTerminalFontSize.storageKey) private var fontSize = PierTerminalFontSize.defaultValue
    let instanceID: String
    let tabID: String
    let onTitleChange: (String) -> Void

    @MainActor
    final class Coordinator {
        var terminal: PierGhosttyMacSurfaceView?
        var onTitleChange: (String) -> Void

        init(onTitleChange: @escaping (String) -> Void) {
            self.onTitleChange = onTitleChange
        }
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(onTitleChange: onTitleChange)
    }

    func makeNSView(context: Context) -> NSView {
        let container = NSView()
        container.wantsLayer = true
        applyTerminalBackground(to: container)

        do {
            let terminal = try PierGhosttyMacSurfaceView(
                instanceID: instanceID,
                tabID: tabID,
                fontSize: fontSize,
                onTitleChange: { [weak coordinator = context.coordinator] title in
                    coordinator?.onTitleChange(title)
                }
            )
            terminal.translatesAutoresizingMaskIntoConstraints = false
            container.addSubview(terminal)
            NSLayoutConstraint.activate([
                terminal.leadingAnchor.constraint(equalTo: container.leadingAnchor),
                terminal.trailingAnchor.constraint(equalTo: container.trailingAnchor),
                terminal.topAnchor.constraint(equalTo: container.topAnchor),
                terminal.bottomAnchor.constraint(equalTo: container.bottomAnchor),
            ])
            context.coordinator.terminal = terminal
        } catch {
            let label = NSTextField(wrappingLabelWithString: error.localizedDescription)
            label.textColor = .secondaryLabelColor
            label.alignment = .center
            label.translatesAutoresizingMaskIntoConstraints = false
            container.addSubview(label)
            NSLayoutConstraint.activate([
                label.centerXAnchor.constraint(equalTo: container.centerXAnchor),
                label.centerYAnchor.constraint(equalTo: container.centerYAnchor),
                label.widthAnchor.constraint(lessThanOrEqualTo: container.widthAnchor, constant: -48),
            ])
        }

        return container
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        context.coordinator.onTitleChange = onTitleChange
        context.coordinator.terminal?.setFontSize(fontSize)
        applyTerminalBackground(to: nsView)
    }

    static func dismantleNSView(_ nsView: NSView, coordinator: Coordinator) {
        coordinator.terminal?.close()
        coordinator.terminal = nil
    }
}

@MainActor
final class PierGhosttyMacSurfaceView: NSView {
    private let surfaceContext: PierGhosttySurfaceContext
    nonisolated(unsafe) private var surface: ghostty_surface_t?

    override var acceptsFirstResponder: Bool { true }

    init(
        instanceID: String,
        tabID: String,
        fontSize: Double,
        onTitleChange: @escaping @MainActor (String) -> Void
    ) throws {
        surfaceContext = PierGhosttySurfaceContext(onTitleChange: onTitleChange)
        super.init(frame: NSRect(x: 0, y: 0, width: 800, height: 600))
        wantsLayer = true
        applyTerminalBackground(to: self)

        guard let app = PierGhosttyRuntime.shared.app else {
            throw PierGhosttyViewError.initialization(
                PierGhosttyRuntime.shared.initializationError ?? "Ghostty is unavailable."
            )
        }
        let executable = try PierCLIExecutable.locate()
        let command = [
            executable.path,
            "app",
            "attach",
            instanceID,
            tabID,
        ].map(pierShellQuote).joined(separator: " ")

        var config = ghostty_surface_config_new()
        config.platform_tag = GHOSTTY_PLATFORM_MACOS
        config.platform = ghostty_platform_u(macos: ghostty_platform_macos_s(
            nsview: Unmanaged.passUnretained(self).toOpaque()
        ))
        config.userdata = surfaceContext.opaquePointer
        config.scale_factor = Double(NSScreen.main?.backingScaleFactor ?? 2)
        config.font_size = Float(PierTerminalFontSize.normalized(fontSize))
        config.wait_after_command = true

        let environment = PierProcessEnvironment.terminal().compactMap(PierGhosttyEnvironmentValue.init)
        var environmentValues = environment.map(\.value)
        config.env_var_count = environmentValues.count

        let created = command.withCString { commandPointer in
            config.command = commandPointer
            return environmentValues.withUnsafeMutableBufferPointer { values in
                config.env_vars = values.baseAddress
                return ghostty_surface_new(app, &config)
            }
        }
        guard let created else {
            throw PierGhosttyViewError.initialization("Ghostty could not create the terminal surface.")
        }
        surface = created
        surfaceContext.surface = created
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) is not supported")
    }

    deinit {
        if let surface { ghostty_surface_free(surface) }
    }

    func close() {
        guard let surface else { return }
        self.surface = nil
        surfaceContext.surface = nil
        ghostty_surface_free(surface)
    }

    func setFontSize(_ points: Double) {
        guard let surface else { return }
        pierGhosttySetFontSize(surface: surface, points: points)
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        updateSurfaceGeometry()
        if let window, window.firstResponder == nil || window.firstResponder === window.contentView {
            window.makeFirstResponder(self)
        }
        if let surface, let displayID = window?.screen?.deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? UInt32 {
            ghostty_surface_set_display_id(surface, displayID)
        }
    }

    override func layout() {
        super.layout()
        updateSurfaceGeometry()
    }

    override func viewDidChangeBackingProperties() {
        super.viewDidChangeBackingProperties()
        updateSurfaceGeometry()
    }

    override func viewDidChangeEffectiveAppearance() {
        super.viewDidChangeEffectiveAppearance()
        applyTerminalBackground(to: self)
    }

    override func becomeFirstResponder() -> Bool {
        let result = super.becomeFirstResponder()
        if result, let surface { ghostty_surface_set_focus(surface, true) }
        return result
    }

    override func resignFirstResponder() -> Bool {
        let result = super.resignFirstResponder()
        if result, let surface { ghostty_surface_set_focus(surface, false) }
        return result
    }

    override func keyDown(with event: NSEvent) {
        if !send(event, action: event.isARepeat ? GHOSTTY_ACTION_REPEAT : GHOSTTY_ACTION_PRESS) {
            super.keyDown(with: event)
        }
    }

    override func keyUp(with event: NSEvent) {
        if !send(event, action: GHOSTTY_ACTION_RELEASE) {
            super.keyUp(with: event)
        }
    }

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        send(event, action: event.isARepeat ? GHOSTTY_ACTION_REPEAT : GHOSTTY_ACTION_PRESS)
    }

    override func mouseDown(with event: NSEvent) {
        window?.makeFirstResponder(self)
        sendMousePosition(event)
        guard let surface else { return }
        _ = ghostty_surface_mouse_button(
            surface,
            GHOSTTY_MOUSE_PRESS,
            GHOSTTY_MOUSE_LEFT,
            modifiers(event.modifierFlags)
        )
    }

    override func mouseUp(with event: NSEvent) {
        sendMousePosition(event)
        guard let surface else { return }
        _ = ghostty_surface_mouse_button(
            surface,
            GHOSTTY_MOUSE_RELEASE,
            GHOSTTY_MOUSE_LEFT,
            modifiers(event.modifierFlags)
        )
    }

    override func rightMouseDown(with event: NSEvent) {
        sendMousePosition(event)
        guard let surface else { return super.rightMouseDown(with: event) }
        if !ghostty_surface_mouse_button(
            surface,
            GHOSTTY_MOUSE_PRESS,
            GHOSTTY_MOUSE_RIGHT,
            modifiers(event.modifierFlags)
        ) {
            super.rightMouseDown(with: event)
        }
    }

    override func rightMouseUp(with event: NSEvent) {
        sendMousePosition(event)
        guard let surface else { return super.rightMouseUp(with: event) }
        if !ghostty_surface_mouse_button(
            surface,
            GHOSTTY_MOUSE_RELEASE,
            GHOSTTY_MOUSE_RIGHT,
            modifiers(event.modifierFlags)
        ) {
            super.rightMouseUp(with: event)
        }
    }

    override func mouseMoved(with event: NSEvent) { sendMousePosition(event) }
    override func mouseDragged(with event: NSEvent) { sendMousePosition(event) }
    override func rightMouseDragged(with event: NSEvent) { sendMousePosition(event) }

    override func scrollWheel(with event: NSEvent) {
        guard let surface else { return }
        ghostty_surface_mouse_scroll(surface, event.scrollingDeltaX, event.scrollingDeltaY, 0)
    }

    private func updateSurfaceGeometry() {
        guard let surface, bounds.width > 0, bounds.height > 0 else { return }
        let scale = window?.backingScaleFactor ?? NSScreen.main?.backingScaleFactor ?? 2
        ghostty_surface_set_content_scale(surface, scale, scale)
        let size = convertToBacking(bounds).size
        ghostty_surface_set_size(surface, UInt32(size.width), UInt32(size.height))
    }

    private func send(_ event: NSEvent, action: ghostty_input_action_e) -> Bool {
        guard let surface else { return false }
        return pierGhosttySendKey(
            surface: surface,
            action: action,
            keyCode: UInt32(event.keyCode),
            text: event.characters,
            modifiers: modifiers(event.modifierFlags)
        )
    }

    private func modifiers(_ flags: NSEvent.ModifierFlags) -> ghostty_input_mods_e {
        pierGhosttyModifiers(
            shift: flags.contains(.shift),
            control: flags.contains(.control),
            option: flags.contains(.option),
            command: flags.contains(.command),
            capsLock: flags.contains(.capsLock)
        )
    }

    private func sendMousePosition(_ event: NSEvent) {
        guard let surface else { return }
        let point = convert(event.locationInWindow, from: nil)
        ghostty_surface_mouse_pos(
            surface,
            point.x,
            bounds.height - point.y,
            modifiers(event.modifierFlags)
        )
    }
}

private final class PierGhosttyEnvironmentValue {
    private let key: UnsafeMutablePointer<CChar>
    private let contents: UnsafeMutablePointer<CChar>

    let value: ghostty_env_var_s

    init?(_ entry: String) {
        guard let separator = entry.firstIndex(of: "=") else { return nil }
        let keyString = String(entry[..<separator])
        let valueString = String(entry[entry.index(after: separator)...])
        guard let key = strdup(keyString) else { return nil }
        guard let contents = strdup(valueString) else {
            free(key)
            return nil
        }
        self.key = key
        self.contents = contents
        value = ghostty_env_var_s(key: UnsafePointer(key), value: UnsafePointer(contents))
    }

    deinit {
        free(key)
        free(contents)
    }
}

private enum PierGhosttyViewError: LocalizedError {
    case initialization(String)

    var errorDescription: String? {
        switch self {
        case .initialization(let message): message
        }
    }
}

private func pierShellQuote(_ value: String) -> String {
    "'\(value.replacingOccurrences(of: "'", with: "'\\''"))'"
}

@MainActor
private func applyTerminalBackground(to view: NSView) {
    view.effectiveAppearance.performAsCurrentDrawingAppearance {
        view.layer?.backgroundColor = PierTheme.nativeTerminalBackground.cgColor
    }
}
#endif
