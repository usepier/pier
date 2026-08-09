#if os(iOS)
import SwiftTerm
import SwiftUI
import UIKit

struct PierMobileTerminalView: UIViewRepresentable {
    let instanceID: String
    let tabID: String
    let onTitleChange: (String) -> Void

    @MainActor
    final class Coordinator: NSObject, @preconcurrency TerminalViewDelegate {
        private let session = PierMobileTerminalSession()
        weak var terminal: TerminalView?
        var onTitleChange: (String) -> Void

        init(onTitleChange: @escaping (String) -> Void) {
            self.onTitleChange = onTitleChange
        }

        func start(instanceID: String, tabID: String) {
            Task {
                await session.start(instanceID: instanceID, tabID: tabID) { [weak self] bytes in
                    Task { @MainActor [weak self] in
                        self?.terminal?.feed(byteArray: bytes[...])
                    }
                } onClose: { [weak self] message in
                    guard !message.isEmpty else { return }
                    Task { @MainActor [weak self] in
                        self?.terminal?.feed(text: "\r\nPier SSH: \(message)\r\n")
                    }
                }
            }
        }

        func stop() {
            Task { await session.stop() }
        }

        func send(source: TerminalView, data: ArraySlice<UInt8>) {
            let bytes = Data(data)
            Task { await session.send(bytes) }
        }

        func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
            Task { await session.resize(columns: newCols, rows: newRows) }
        }

        func setTerminalTitle(source: TerminalView, title: String) {
            onTitleChange(title)
        }

        func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
        func scrolled(source: TerminalView, position: Double) {}
        func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}

        func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
            guard let url = URL(string: link) else { return }
            UIApplication.shared.open(url)
        }

        func clipboardCopy(source: TerminalView, content: Data) {
            if let value = String(data: content, encoding: .utf8) {
                UIPasteboard.general.string = value
            }
        }
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(onTitleChange: onTitleChange)
    }

    func makeUIView(context: Context) -> TerminalView {
        let terminal = TerminalView(frame: .zero)
        terminal.backgroundColor = .black
        terminal.nativeBackgroundColor = .black
        terminal.terminalDelegate = context.coordinator
        context.coordinator.terminal = terminal
        context.coordinator.start(instanceID: instanceID, tabID: tabID)
        return terminal
    }

    func updateUIView(_ uiView: TerminalView, context: Context) {
        context.coordinator.onTitleChange = onTitleChange
    }

    static func dismantleUIView(_ uiView: TerminalView, coordinator: Coordinator) {
        coordinator.stop()
        coordinator.terminal = nil
    }
}

private actor PierMobileTerminalSession {
    private var terminal: PierMobileTerminalHandle?
    private var listener: PierMobileTerminalListener?

    func start(
        instanceID: String,
        tabID: String,
        onOutput: @escaping @Sendable ([UInt8]) -> Void,
        onClose: @escaping @Sendable (String) -> Void
    ) async {
        stop()
        let listener = PierMobileTerminalListener(
            output: { onOutput(Array($0)) },
            closed: onClose
        )
        self.listener = listener
        do {
            terminal = try await PierMobileCore.shared.openTerminal(
                instanceID: instanceID,
                tabID: tabID,
                listener: listener
            )
        } catch {
            onClose(error.localizedDescription)
        }
    }

    func send(_ data: Data) {
        try? terminal?.write(data)
    }

    func resize(columns: Int, rows: Int) {
        try? terminal?.resize(columns: columns, rows: rows)
    }

    func stop() {
        terminal?.close()
        terminal = nil
        listener = nil
    }
}
#endif
