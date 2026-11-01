import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @AppStorage(PierAppearance.storageKey) private var appearance = PierAppearance.system
    @AppStorage(PierTerminalFontSize.storageKey) private var fontSize = PierTerminalFontSize.defaultValue
    @State private var confirmsSignOut = false

    var body: some View {
        Form {
            Section("Appearance") {
                Picker("Theme", selection: $appearance) {
                    ForEach(PierAppearance.allCases) { Label($0.label, systemImage: $0.systemImage).tag($0) }
                }
                .pickerStyle(.segmented)
            }

            Section("Terminal") {
                Stepper(value: $fontSize, in: PierTerminalFontSize.range, step: PierTerminalFontSize.step) {
                    LabeledContent("Font size", value: "\(Int(PierTerminalFontSize.normalized(fontSize))) pt")
                }
                Button("Reset Font Size") { fontSize = PierTerminalFontSize.defaultValue }
                    .disabled(PierTerminalFontSize.normalized(fontSize) == PierTerminalFontSize.defaultValue)
            }

            if let status = model.status, status.signedIn {
                Section("AWS") {
                    if let account = status.accountID { LabeledContent("Account", value: account) }
                    if let role = status.roleName { LabeledContent("Permission set", value: role) }
                    if let region = status.region { LabeledContent("Region", value: region) }
                    Button("Sign Out", role: .destructive) { confirmsSignOut = true }
                }
            }

            Section {
                LabeledContent("Version", value: Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String ?? "–")
            }
        }
        .pierScrollSurface()
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
        }
        .confirmationDialog("Sign out of AWS?", isPresented: $confirmsSignOut, titleVisibility: .visible) {
            Button("Sign Out", role: .destructive) {
                Task {
                    await model.signOut()
                    dismiss()
                }
            }
        } message: {
            Text("This phone forgets its AWS sign-in. Your sessions carry on.")
        }
    }
}
