import SwiftUI

/// A terminal, full screen: the window's title (as the program sets it)
/// and a way back. Leaving detaches; the window keeps running.
struct TerminalScreen: View {
    @Environment(\.dismiss) private var dismiss
    let terminal: OpenTerminal
    @State private var title = ""

    var body: some View {
        NavigationStack {
            TerminalView(sessionID: terminal.sessionID, windowID: terminal.windowID) { title = $0 }
                .ignoresSafeArea(.container, edges: .bottom)
                .background(Color(uiColor: PierTheme.nativeTerminalBackground).ignoresSafeArea())
                .navigationTitle(title.isEmpty ? terminal.title : title)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Done") { dismiss() }
                    }
                }
        }
    }
}
