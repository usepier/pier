#if os(macOS)
import AppKit
import SwiftUI

// SwiftUI's hidden titlebar style extends content into the titlebar, but it
// does not make custom empty regions draggable. Configure the underlying
// NSWindow so the bespoke sidebar/tab bars retain normal window behavior.
struct PierWindowConfigurator: NSViewRepresentable {
    final class Coordinator {
        weak var window: NSWindow?
        var buttonOrigins: [NSWindow.ButtonType: NSPoint] = [:]
    }

    func makeCoordinator() -> Coordinator {
        Coordinator()
    }

    func makeNSView(context: Context) -> NSView {
        let view = NSView()
        DispatchQueue.main.async {
            configure(view.window, coordinator: context.coordinator)
        }
        return view
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        DispatchQueue.main.async {
            configure(nsView.window, coordinator: context.coordinator)
        }
    }

    private func configure(_ window: NSWindow?, coordinator: Coordinator) {
        window?.styleMask.insert(.fullSizeContentView)
        window?.titleVisibility = .hidden
        window?.titlebarAppearsTransparent = true
        window?.titlebarSeparatorStyle = .none
        window?.isMovableByWindowBackground = true
        window?.toolbar = nil

        // Remove AppKit's titlebar safe-area reservation. Our explicit
        // 43-point headers already leave room for the traffic lights.
        if let contentView = window?.contentView,
           contentView.additionalSafeAreaInsets.top == 0,
           contentView.safeAreaInsets.top > 0 {
            var insets = contentView.additionalSafeAreaInsets
            insets.top = -contentView.safeAreaInsets.top
            contentView.additionalSafeAreaInsets = insets
            contentView.needsLayout = true
            contentView.superview?.needsLayout = true
        }

        guard let window else { return }
        if coordinator.window !== window {
            coordinator.window = window
            coordinator.buttonOrigins.removeAll()
        }
        alignTrafficLights(in: window, coordinator: coordinator)
    }

    private func alignTrafficLights(in window: NSWindow, coordinator: Coordinator) {
        let types: [NSWindow.ButtonType] = [.closeButton, .miniaturizeButton, .zoomButton]
        for type in types {
            guard let button = window.standardWindowButton(type), let container = button.superview else { continue }
            let original = coordinator.buttonOrigins[type] ?? button.frame.origin
            coordinator.buttonOrigins[type] = original

            // AppKit centers these for its compact 28-point titlebar. Pier's
            // custom header is 43 points, so shift the system controls by the
            // difference while keeping their native spacing and hit targets.
            let chromeHeight = min(max(container.bounds.height, 28), 43)
            let offset = (43 - chromeHeight) / 2
            let y = original.y + (container.isFlipped ? offset : -offset)
            button.setFrameOrigin(NSPoint(x: original.x, y: y))
        }
    }
}
#endif
