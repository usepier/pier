import SwiftUI

@main
struct PierMacOSApp: App {
    @State private var model = PierAppModel(service: PierServiceFactory.make())

    var body: some Scene {
        Window("Pier", id: "main") {
            PierRootView()
                .environment(model)
                .frame(minWidth: 820, minHeight: 560)
                .background(PierWindowConfigurator())
        }
        .windowStyle(.hiddenTitleBar)
        .defaultSize(width: 1120, height: 760)
        .commands {
            CommandGroup(replacing: .newItem) {}
        }

        Settings {
            Form {
                LabeledContent("Application", value: "Pier")
                LabeledContent("Interface", value: "Native SwiftUI")
            }
            .formStyle(.grouped)
            .padding()
            .frame(width: 420)
        }
    }
}
