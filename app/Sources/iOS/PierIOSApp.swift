import SwiftUI

@main
struct PierIOSApp: App {
    @State private var model = PierAppModel(service: PierServiceFactory.make())

    var body: some Scene {
        WindowGroup {
            PierRootView()
                .environment(model)
        }
    }
}

