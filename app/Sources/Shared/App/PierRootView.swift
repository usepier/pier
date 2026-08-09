import SwiftUI

struct PierRootView: View {
    @Environment(PierAppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase

    var body: some View {
        Group {
            if model.setupStatus == nil {
                ProgressView("Checking Pier…")
            } else if model.showsOnboarding {
                OnboardingView()
            } else {
                PierContentView()
            }
        }
        #if os(macOS)
        .ignoresSafeArea(.container, edges: .top)
        #endif
        .task {
            await model.start()
        }
        .task(id: PierSynchronizationTaskID(
            scenePhase: scenePhase,
            isConfigured: model.setupStatus?.configured == true
        )) {
            guard scenePhase == .active, model.setupStatus?.configured == true else { return }

            await model.syncInstances()
            while !Task.isCancelled {
                do {
                    try await Task.sleep(for: PierSynchronization.interval)
                } catch {
                    return
                }
                await model.syncInstances()
            }
        }
        .alert(
            "Pier couldn’t complete that action",
            isPresented: Binding(
                get: { model.errorMessage != nil },
                set: { if !$0 { model.dismissError() } }
            )
        ) {
            if model.requiresAWSLogin {
                Button("Sign In") {
                    Task { await model.signInAWS() }
                }
            }
            Button(model.requiresAWSLogin ? "Not Now" : "OK", role: .cancel) {
                model.dismissError()
            }
        } message: {
            Text(model.errorMessage ?? "Unknown error")
        }
    }
}

enum PierSynchronization {
    static let interval = Duration.seconds(5)
}

private struct PierSynchronizationTaskID: Equatable {
    let scenePhase: ScenePhase
    let isConfigured: Bool
}
