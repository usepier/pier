#if os(macOS)
import AppKit
import SwiftTerm
import SwiftUI

struct PierTerminalSessionView: NSViewRepresentable {
    let instanceID: String
    let tabID: String
    let onTitleChange: (String) -> Void

    @MainActor
    final class Coordinator: NSObject, @preconcurrency LocalProcessTerminalViewDelegate {
        var terminal: LocalProcessTerminalView?
        var onTitleChange: (String) -> Void

        init(onTitleChange: @escaping (String) -> Void) {
            self.onTitleChange = onTitleChange
        }

        func sizeChanged(source: LocalProcessTerminalView, newCols: Int, newRows: Int) {}

        func setTerminalTitle(source: LocalProcessTerminalView, title: String) {
            onTitleChange(title)
        }

        func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
        func processTerminated(source: TerminalView, exitCode: Int32?) {}
    }

    func makeCoordinator() -> Coordinator {
        Coordinator(onTitleChange: onTitleChange)
    }

    func makeNSView(context: Context) -> NSView {
        let container = NSView()
        container.wantsLayer = true
        container.layer?.backgroundColor = NSColor.black.cgColor

        do {
            let executable = try PierCLIExecutable.locate()
            let terminal = LocalProcessTerminalView(frame: .zero)
            terminal.translatesAutoresizingMaskIntoConstraints = false
            container.addSubview(terminal)
            NSLayoutConstraint.activate([
                terminal.leadingAnchor.constraint(equalTo: container.leadingAnchor),
                terminal.trailingAnchor.constraint(equalTo: container.trailingAnchor),
                terminal.topAnchor.constraint(equalTo: container.topAnchor),
                terminal.bottomAnchor.constraint(equalTo: container.bottomAnchor),
            ])
            context.coordinator.terminal = terminal
            terminal.processDelegate = context.coordinator
            terminal.startProcess(
                executable: executable.path,
                args: ["app", "attach", instanceID, tabID],
                environment: PierProcessEnvironment.terminal()
            )
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
    }

    static func dismantleNSView(_ nsView: NSView, coordinator: Coordinator) {
        coordinator.terminal?.terminate()
        coordinator.terminal = nil
    }
}
#endif
