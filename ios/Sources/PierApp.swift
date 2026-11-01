import SwiftUI

@main
struct PierApp: App {
    @State private var model = AppModel()

    var body: some Scene {
        WindowGroup {
            RootView().environment(model)
        }
    }
}

/// Sign-in until there is one, then the sessions — refreshed every few
/// seconds while the app is in front.
struct RootView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.colorScheme) private var systemScheme
    @AppStorage(PierAppearance.storageKey) private var appearance = PierAppearance.system

    var body: some View {
        @Bindable var model = model
        Group {
            if model.status == nil {
                ProgressView()
            } else if model.ready {
                NavigationStack { SessionsView() }
            } else {
                SignInView()
            }
        }
        .background(PierTheme.canvas.ignoresSafeArea())
        .tint(PierTheme.accent)
        .preferredColorScheme(appearance.preferredColorScheme)
        .task(id: appearance.terminalTheme(systemColorScheme: systemScheme)) {
            PierGhosttyRuntime.shared.apply(theme: appearance.terminalTheme(systemColorScheme: systemScheme))
        }
        .task { await model.start() }
        .task(id: Refresh(active: scenePhase == .active, ready: model.ready)) {
            guard scenePhase == .active, model.ready else { return }
            while !Task.isCancelled {
                await model.refresh()
                try? await Task.sleep(for: .seconds(10))
            }
        }
        .alert(
            "That didn't work",
            isPresented: Binding(get: { model.error != nil }, set: { if !$0 { model.error = nil } }),
            presenting: model.error
        ) { _ in
            Button("OK", role: .cancel) {}
        } message: { error in
            Text(error.localizedDescription)
        }
    }
}

private struct Refresh: Equatable {
    let active: Bool
    let ready: Bool
}
