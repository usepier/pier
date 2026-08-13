import SwiftUI

struct PierSettingsView: View {
    @AppStorage(PierAppearance.storageKey) private var appearanceValue = PierAppearance.system.rawValue
    @AppStorage(PierTerminalFontSize.storageKey) private var terminalFontSize = PierTerminalFontSize.defaultValue
    @Environment(\.dismiss) private var dismiss

    var showsDoneButton = false

    private var appearance: PierAppearance {
        PierAppearance(rawValue: appearanceValue) ?? .system
    }

    private var appearanceSelection: Binding<PierAppearance> {
        Binding(
            get: { appearance },
            set: { appearanceValue = $0.rawValue }
        )
    }

    private var terminalFontSizeSelection: Binding<Double> {
        Binding(
            get: { PierTerminalFontSize.normalized(terminalFontSize) },
            set: { terminalFontSize = PierTerminalFontSize.normalized($0) }
        )
    }

    var body: some View {
        Form {
            Section("Appearance") {
                Picker("Theme", selection: appearanceSelection) {
                    ForEach(PierAppearance.allCases) { option in
                        Label(option.label, systemImage: option.systemImage)
                            .tag(option)
                    }
                }
                #if os(iOS)
                .pickerStyle(.segmented)
                #endif

                LabeledContent("Palette") {
                    HStack(spacing: 5) {
                        paletteSwatch(PierTheme.canvas, label: "Canvas")
                        paletteSwatch(PierTheme.surfaceSoft, label: "Soft surface")
                        paletteSwatch(PierTheme.surfaceCard, label: "Card surface")
                        paletteSwatch(PierTheme.ink, label: "Ink")
                        paletteSwatch(PierTheme.accent, label: "Accent")
                    }
                }
            }

            Section("Terminal") {
                LabeledContent("Font", value: "JetBrains Mono")
                Stepper(
                    value: terminalFontSizeSelection,
                    in: PierTerminalFontSize.range,
                    step: PierTerminalFontSize.step
                ) {
                    LabeledContent(
                        "Font size",
                        value: "\(Int(PierTerminalFontSize.normalized(terminalFontSize))) pt"
                    )
                }
                .accessibilityValue("\(Int(PierTerminalFontSize.normalized(terminalFontSize))) points")

                Button("Reset Font Size") {
                    terminalFontSize = PierTerminalFontSize.defaultValue
                }
                .disabled(PierTerminalFontSize.normalized(terminalFontSize) == PierTerminalFontSize.defaultValue)
            }
        }
        .pierScrollSurface()
        .tint(PierTheme.accent)
        .navigationTitle("Settings")
        .preferredColorScheme(appearance.preferredColorScheme)
        .toolbar {
            if showsDoneButton {
                ToolbarItem(placement: .confirmationAction) {
                    doneButton
                }
            }
        }
    }

    @ViewBuilder
    private var doneButton: some View {
        #if os(iOS)
        if #available(iOS 26.0, *) {
            Button("Done", systemImage: "checkmark", role: .confirm) { dismiss() }
                .labelStyle(.iconOnly)
                .keyboardShortcut(.defaultAction)
        } else {
            Button("Done", systemImage: "checkmark") { dismiss() }
                .labelStyle(.iconOnly)
                .keyboardShortcut(.defaultAction)
        }
        #else
        Button("Done") { dismiss() }
            .keyboardShortcut(.defaultAction)
        #endif
    }

    private func paletteSwatch(_ color: Color, label: String) -> some View {
        RoundedRectangle(cornerRadius: 5)
            .fill(color)
            .frame(width: 24, height: 24)
            .overlay {
                RoundedRectangle(cornerRadius: 5)
                    .stroke(PierTheme.hairline, lineWidth: 1)
            }
            .accessibilityLabel(label)
    }
}
