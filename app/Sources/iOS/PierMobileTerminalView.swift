#if os(iOS)
import GhosttyKit
import QuartzCore
import SwiftUI
import UIKit

struct PierMobileTerminalView: UIViewRepresentable {
    @AppStorage(PierTerminalFontSize.storageKey) private var fontSize = PierTerminalFontSize.defaultValue
    let instanceID: String
    let tabID: String
    let onTitleChange: (String) -> Void

    @MainActor
    final class Coordinator {
        private let session = PierMobileTerminalSession()
        weak var terminal: PierGhosttyIOSSurfaceView?
        var onTitleChange: (String) -> Void

        init(onTitleChange: @escaping (String) -> Void) {
            self.onTitleChange = onTitleChange
        }

        func start(instanceID: String, tabID: String) {
            Task {
                await session.start(instanceID: instanceID, tabID: tabID) { [weak self] data in
                    Task { @MainActor [weak self] in self?.terminal?.feed(data) }
                } onClose: { [weak self] message in
                    guard !message.isEmpty else { return }
                    let data = Data("\r\nPier SSH: \(message)\r\n".utf8)
                    Task { @MainActor [weak self] in self?.terminal?.feed(data) }
                }
            }
        }

        func stop() {
            Task { await session.stop() }
        }

        nonisolated func send(_ data: Data) {
            Task { await session.send(data) }
        }

        nonisolated func resize(columns: Int, rows: Int) {
            Task { await session.resize(columns: columns, rows: rows) }
        }
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(onTitleChange: onTitleChange)
    }

    func makeUIView(context: Context) -> UIView {
        do {
            let terminal = try PierGhosttyIOSSurfaceView(
                fontSize: fontSize,
                onTitleChange: { [weak coordinator = context.coordinator] title in
                    coordinator?.onTitleChange(title)
                },
                write: { [weak coordinator = context.coordinator] data in
                    coordinator?.send(data)
                },
                resize: { [weak coordinator = context.coordinator] columns, rows in
                    coordinator?.resize(columns: columns, rows: rows)
                }
            )
            context.coordinator.terminal = terminal
            context.coordinator.start(instanceID: instanceID, tabID: tabID)
            return terminal
        } catch {
            let fallback = UIView()
            fallback.backgroundColor = PierTheme.nativeTerminalBackground
            let label = UILabel()
            label.text = error.localizedDescription
            label.textColor = .secondaryLabel
            label.textAlignment = .center
            label.numberOfLines = 0
            label.translatesAutoresizingMaskIntoConstraints = false
            fallback.addSubview(label)
            NSLayoutConstraint.activate([
                label.centerXAnchor.constraint(equalTo: fallback.centerXAnchor),
                label.centerYAnchor.constraint(equalTo: fallback.centerYAnchor),
                label.widthAnchor.constraint(lessThanOrEqualTo: fallback.widthAnchor, constant: -48),
            ])
            return fallback
        }
    }

    func updateUIView(_ uiView: UIView, context: Context) {
        context.coordinator.onTitleChange = onTitleChange
        (uiView as? PierGhosttyIOSSurfaceView)?.setFontSize(fontSize)
    }

    static func dismantleUIView(_ uiView: UIView, coordinator: Coordinator) {
        coordinator.stop()
        coordinator.terminal?.close()
        coordinator.terminal = nil
    }
}

private actor PierMobileTerminalSession {
    private var terminal: PierMobileTerminalHandle?
    private var listener: PierMobileTerminalListener?
    private var pendingSize: (columns: Int, rows: Int)?

    func start(
        instanceID: String,
        tabID: String,
        onOutput: @escaping @Sendable (Data) -> Void,
        onClose: @escaping @Sendable (String) -> Void
    ) async {
        stop()
        let listener = PierMobileTerminalListener(output: onOutput, closed: onClose)
        self.listener = listener
        do {
            let terminal = try await PierMobileCore.shared.openTerminal(
                instanceID: instanceID,
                tabID: tabID,
                listener: listener
            )
            self.terminal = terminal
            if let pendingSize {
                try? terminal.resize(columns: pendingSize.columns, rows: pendingSize.rows)
            }
        } catch {
            onClose(error.localizedDescription)
        }
    }

    func send(_ data: Data) {
        try? terminal?.write(data)
    }

    func resize(columns: Int, rows: Int) {
        pendingSize = (columns, rows)
        try? terminal?.resize(columns: columns, rows: rows)
    }

    func stop() {
        terminal?.close()
        terminal = nil
        listener = nil
    }
}

@MainActor
final class PierGhosttyIOSSurfaceView: UIView, UIKeyInput {
    private let surfaceContext: PierGhosttySurfaceContext
    nonisolated(unsafe) private var surface: ghostty_surface_t?
    private weak var rendererLayer: CALayer?
    private var controlLatched = false
    private var optionLatched = false
    private var commandLatched = false
    private weak var controlButton: UIButton?
    private weak var optionButton: UIButton?
    private weak var commandButton: UIButton?

    override class var layerClass: AnyClass { CAMetalLayer.self }
    override var canBecomeFirstResponder: Bool { true }
    var hasText: Bool { true }

    private lazy var terminalAccessoryView: UIView = {
        let accessory = UIInputView(
            frame: CGRect(x: 0, y: 0, width: 0, height: 50),
            inputViewStyle: .keyboard
        )
        accessory.autoresizingMask = .flexibleWidth

        let escape = makeAccessoryButton(title: "Esc", action: #selector(sendEscape))
        let tab = makeAccessoryButton(title: "Tab", action: #selector(sendTab))
        let control = makeAccessoryButton(title: "Ctrl", action: #selector(toggleControl))
        let option = makeAccessoryButton(title: "⌥", action: #selector(toggleOption), accessibilityLabel: "Option")
        let command = makeAccessoryButton(title: "⌘", action: #selector(toggleCommand), accessibilityLabel: "Command")
        let left = makeAccessoryButton(symbol: "arrow.left", action: #selector(sendLeft), accessibilityLabel: "Left Arrow")
        let down = makeAccessoryButton(symbol: "arrow.down", action: #selector(sendDown), accessibilityLabel: "Down Arrow")
        let up = makeAccessoryButton(symbol: "arrow.up", action: #selector(sendUp), accessibilityLabel: "Up Arrow")
        let right = makeAccessoryButton(symbol: "arrow.right", action: #selector(sendRight), accessibilityLabel: "Right Arrow")

        controlButton = control
        optionButton = option
        commandButton = command

        let stack = UIStackView(arrangedSubviews: [
            escape, tab, control, option, command, left, down, up, right,
        ])
        stack.axis = .horizontal
        stack.alignment = .fill
        stack.distribution = .fillEqually
        stack.spacing = 4
        stack.translatesAutoresizingMaskIntoConstraints = false
        accessory.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: accessory.leadingAnchor, constant: 6),
            stack.trailingAnchor.constraint(equalTo: accessory.trailingAnchor, constant: -6),
            stack.topAnchor.constraint(equalTo: accessory.topAnchor, constant: 6),
            stack.bottomAnchor.constraint(equalTo: accessory.bottomAnchor, constant: -6),
        ])
        return accessory
    }()

    override var inputAccessoryView: UIView? { terminalAccessoryView }

    init(
        fontSize: Double,
        onTitleChange: @escaping @MainActor (String) -> Void,
        write: @escaping @Sendable (Data) -> Void,
        resize: @escaping @Sendable (Int, Int) -> Void
    ) throws {
        surfaceContext = PierGhosttySurfaceContext(
            onTitleChange: onTitleChange,
            write: write,
            resize: resize
        )
        super.init(frame: CGRect(x: 0, y: 0, width: 800, height: 600))
        backgroundColor = PierTheme.nativeTerminalBackground
        clipsToBounds = true

        guard let app = PierGhosttyRuntime.shared.app else {
            throw PierGhosttyIOSViewError.initialization(
                PierGhosttyRuntime.shared.initializationError ?? "Ghostty is unavailable."
            )
        }

        var transport = ghostty_external_transport_s(
            userdata: surfaceContext.opaquePointer,
            write: pierGhosttyExternalWrite,
            resize: pierGhosttyExternalResize
        )
        var config = ghostty_surface_config_new()
        config.platform_tag = GHOSTTY_PLATFORM_IOS
        config.platform = ghostty_platform_u(ios: ghostty_platform_ios_s(
            uiview: Unmanaged.passUnretained(self).toOpaque()
        ))
        config.userdata = surfaceContext.opaquePointer
        config.scale_factor = Double(UIScreen.main.scale)
        config.font_size = Float(PierTerminalFontSize.normalized(fontSize))

        let created = withUnsafePointer(to: &transport) { transportPointer in
            config.external_transport = transportPointer
            return ghostty_surface_new(app, &config)
        }
        guard let surface = created else {
            throw PierGhosttyIOSViewError.initialization("Ghostty could not create the terminal surface.")
        }
        self.surface = surface
        // libghostty attaches its IOSurface renderer as a sublayer. UIKit
        // does not automatically resize that layer with its hosting view.
        rendererLayer = layer.sublayers?.last
        surfaceContext.surface = surface
        surfaceContext.focusKeyboard = { [weak self] in _ = self?.becomeFirstResponder() }

        let tap = UITapGestureRecognizer(target: self, action: #selector(handleTap(_:)))
        addGestureRecognizer(tap)
        let selection = UILongPressGestureRecognizer(target: self, action: #selector(handleSelection(_:)))
        selection.minimumPressDuration = 0.35
        addGestureRecognizer(selection)
        let scroll = UIPanGestureRecognizer(target: self, action: #selector(handleScroll(_:)))
        scroll.minimumNumberOfTouches = 2
        scroll.maximumNumberOfTouches = 2
        addGestureRecognizer(scroll)
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

    func feed(_ data: Data) {
        guard let surface else { return }
        data.withUnsafeBytes { bytes in
            guard let baseAddress = bytes.baseAddress else { return }
            ghostty_surface_feed(
                surface,
                baseAddress.assumingMemoryBound(to: CChar.self),
                UInt(bytes.count)
            )
        }
    }

    func setFontSize(_ points: Double) {
        guard let surface else { return }
        pierGhosttySetFontSize(surface: surface, points: points)
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        updateSurfaceGeometry()
        if window != nil { _ = becomeFirstResponder() }
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        updateSurfaceGeometry()
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

    func insertText(_ text: String) {
        let keyCode: UInt32
        if text == "\n" || text == "\r" {
            keyCode = 0x24
        } else {
            keyCode = pierMacVirtualKeyCode(for: text) ?? 0
        }
        let modifiers = pierGhosttyModifiers(
            shift: false,
            control: controlLatched,
            option: optionLatched,
            command: commandLatched
        )
        clearLatchedModifiers()
        sendKey(code: keyCode, text: text, modifiers: modifiers)
    }

    func deleteBackward() {
        sendAccessoryKey(code: 0x33)
    }

    override func pressesBegan(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        var handled = false
        let hadLatchedModifiers = controlLatched || optionLatched || commandLatched
        for press in presses {
            guard let key = press.key else { continue }
            let code = pierMacVirtualKeyCode(for: key.charactersIgnoringModifiers)
                ?? pierMacVirtualKeyCode(forHIDUsage: UInt32(key.keyCode.rawValue))
                ?? 0
            handled = sendKey(
                code: code,
                text: key.characters,
                modifiers: modifiers(key.modifierFlags, includeLatched: true)
            ) || handled
        }
        if hadLatchedModifiers, handled { clearLatchedModifiers() }
        if !handled { super.pressesBegan(presses, with: event) }
    }

    override func pressesEnded(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        for press in presses {
            guard let key = press.key else { continue }
            let code = pierMacVirtualKeyCode(for: key.charactersIgnoringModifiers)
                ?? pierMacVirtualKeyCode(forHIDUsage: UInt32(key.keyCode.rawValue))
                ?? 0
            _ = sendKey(
                action: GHOSTTY_ACTION_RELEASE,
                code: code,
                text: key.characters,
                modifiers: modifiers(key.modifierFlags)
            )
        }
    }

    @objc private func sendEscape() { sendAccessoryKey(code: 0x35) }
    @objc private func sendTab() { sendAccessoryKey(code: 0x30) }
    @objc private func sendLeft() { sendAccessoryKey(code: 0x7B) }
    @objc private func sendRight() { sendAccessoryKey(code: 0x7C) }
    @objc private func sendDown() { sendAccessoryKey(code: 0x7D) }
    @objc private func sendUp() { sendAccessoryKey(code: 0x7E) }

    @objc private func toggleControl() {
        setControlLatched(!controlLatched)
    }

    @objc private func toggleOption() {
        setOptionLatched(!optionLatched)
    }

    @objc private func toggleCommand() {
        setCommandLatched(!commandLatched)
    }

    private func setControlLatched(_ value: Bool) {
        controlLatched = value
        updateModifierButton(controlButton, active: value)
    }

    private func setOptionLatched(_ value: Bool) {
        optionLatched = value
        updateModifierButton(optionButton, active: value)
    }

    private func setCommandLatched(_ value: Bool) {
        commandLatched = value
        updateModifierButton(commandButton, active: value)
    }

    private func clearLatchedModifiers() {
        setControlLatched(false)
        setOptionLatched(false)
        setCommandLatched(false)
    }

    private func sendAccessoryKey(code: UInt32) {
        let modifiers = pierGhosttyModifiers(
            shift: false,
            control: controlLatched,
            option: optionLatched,
            command: commandLatched
        )
        clearLatchedModifiers()
        sendKey(code: code, text: nil, modifiers: modifiers)
    }

    private func makeAccessoryButton(
        title: String? = nil,
        symbol: String? = nil,
        action: Selector,
        accessibilityLabel: String? = nil
    ) -> UIButton {
        var configuration = UIButton.Configuration.filled()
        configuration.title = title
        configuration.image = symbol.flatMap { UIImage(systemName: $0) }
        configuration.baseForegroundColor = .label
        configuration.baseBackgroundColor = .tertiarySystemFill
        configuration.cornerStyle = .medium
        configuration.contentInsets = NSDirectionalEdgeInsets(top: 5, leading: 3, bottom: 5, trailing: 3)

        let button = UIButton(configuration: configuration)
        button.titleLabel?.font = .monospacedSystemFont(ofSize: 12, weight: .semibold)
        button.addTarget(self, action: action, for: .touchUpInside)
        button.accessibilityLabel = accessibilityLabel ?? title
        return button
    }

    private func updateModifierButton(_ button: UIButton?, active: Bool) {
        guard let button else { return }
        var configuration = button.configuration
        configuration?.baseForegroundColor = active ? .white : .label
        configuration?.baseBackgroundColor = active ? .systemOrange : .tertiarySystemFill
        button.configuration = configuration
        button.accessibilityValue = active ? "On" : "Off"
    }

    @objc private func handleTap(_ recognizer: UITapGestureRecognizer) {
        guard let surface else { return }
        _ = becomeFirstResponder()
        let point = recognizer.location(in: self)
        ghostty_surface_mouse_pos(surface, point.x, point.y, GHOSTTY_MODS_NONE)
        _ = ghostty_surface_mouse_button(surface, GHOSTTY_MOUSE_PRESS, GHOSTTY_MOUSE_LEFT, GHOSTTY_MODS_NONE)
        _ = ghostty_surface_mouse_button(surface, GHOSTTY_MOUSE_RELEASE, GHOSTTY_MOUSE_LEFT, GHOSTTY_MODS_NONE)
    }

    @objc private func handleSelection(_ recognizer: UILongPressGestureRecognizer) {
        guard let surface else { return }
        let point = recognizer.location(in: self)
        ghostty_surface_mouse_pos(surface, point.x, point.y, GHOSTTY_MODS_NONE)
        switch recognizer.state {
        case .began:
            _ = ghostty_surface_mouse_button(surface, GHOSTTY_MOUSE_PRESS, GHOSTTY_MOUSE_LEFT, GHOSTTY_MODS_NONE)
        case .ended, .cancelled, .failed:
            _ = ghostty_surface_mouse_button(surface, GHOSTTY_MOUSE_RELEASE, GHOSTTY_MOUSE_LEFT, GHOSTTY_MODS_NONE)
        default:
            break
        }
    }

    @objc private func handleScroll(_ recognizer: UIPanGestureRecognizer) {
        guard let surface else { return }
        let translation = recognizer.translation(in: self)
        ghostty_surface_mouse_scroll(surface, translation.x, translation.y, 0)
        recognizer.setTranslation(.zero, in: self)
    }

    private func updateSurfaceGeometry() {
        guard let surface, bounds.width > 0, bounds.height > 0 else { return }
        let scale = window?.screen.scale ?? UIScreen.main.scale
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        rendererLayer?.frame = bounds
        CATransaction.commit()
        ghostty_surface_set_content_scale(surface, Double(scale), Double(scale))
        ghostty_surface_set_size(
            surface,
            UInt32(bounds.width * scale),
            UInt32(bounds.height * scale)
        )
    }

    @discardableResult
    private func sendKey(
        action: ghostty_input_action_e = GHOSTTY_ACTION_PRESS,
        code: UInt32,
        text: String?,
        modifiers: ghostty_input_mods_e = GHOSTTY_MODS_NONE
    ) -> Bool {
        guard let surface else { return false }
        return pierGhosttySendKey(
            surface: surface,
            action: action,
            keyCode: code,
            text: text,
            modifiers: modifiers
        )
    }

    private func modifiers(
        _ flags: UIKeyModifierFlags,
        includeLatched: Bool = false
    ) -> ghostty_input_mods_e {
        pierGhosttyModifiers(
            shift: flags.contains(.shift),
            control: flags.contains(.control) || (includeLatched && controlLatched),
            option: flags.contains(.alternate) || (includeLatched && optionLatched),
            command: flags.contains(.command) || (includeLatched && commandLatched),
            capsLock: flags.contains(.alphaShift)
        )
    }
}

// External transport callbacks are also invoked on Ghostty worker threads.
// File-level callbacks keep them independent from the view's @MainActor.
private func pierGhosttyExternalWrite(
    _ userdata: UnsafeMutableRawPointer?,
    _ pointer: UnsafePointer<CChar>?,
    _ count: Int
) {
    guard
        let context = PierGhosttySurfaceContext.from(userdata),
        let pointer
    else { return }
    context.writeHandler(Data(bytes: pointer, count: count))
}

private func pierGhosttyExternalResize(
    _ userdata: UnsafeMutableRawPointer?,
    _ columns: UInt16,
    _ rows: UInt16
) {
    PierGhosttySurfaceContext.from(userdata)?.resizeHandler(Int(columns), Int(rows))
}

private enum PierGhosttyIOSViewError: LocalizedError {
    case initialization(String)

    var errorDescription: String? {
        switch self {
        case .initialization(let message): message
        }
    }
}

private func pierMacVirtualKeyCode(for value: String) -> UInt32? {
    guard let character = value.lowercased().first else { return nil }
    return [
        "a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05,
        "z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C,
        "w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10, "t": 0x11, "1": 0x12,
        "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17, "=": 0x18,
        "9": 0x19, "7": 0x1A, "-": 0x1B, "8": 0x1C, "0": 0x1D, "]": 0x1E,
        "o": 0x1F, "u": 0x20, "[": 0x21, "i": 0x22, "p": 0x23, "l": 0x25,
        "j": 0x26, "'": 0x27, "k": 0x28, ";": 0x29, "\\": 0x2A, ",": 0x2B,
        "/": 0x2C, "n": 0x2D, "m": 0x2E, ".": 0x2F, " ": 0x31, "`": 0x32,
    ][character]
}

private func pierMacVirtualKeyCode(forHIDUsage usage: UInt32) -> UInt32? {
    switch usage {
    case 0x28: 0x24
    case 0x29: 0x35
    case 0x2A: 0x33
    case 0x2B: 0x30
    case 0x4A: 0x73
    case 0x4B: 0x74
    case 0x4C: 0x75
    case 0x4D: 0x77
    case 0x4E: 0x79
    case 0x4F: 0x7C
    case 0x50: 0x7B
    case 0x51: 0x7D
    case 0x52: 0x7E
    default: nil
    }
}
#endif
