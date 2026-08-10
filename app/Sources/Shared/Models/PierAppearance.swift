import SwiftUI

#if os(macOS)
import AppKit
#elseif os(iOS)
import UIKit
#endif

enum PierAppearance: String, CaseIterable, Identifiable, Hashable {
    case system
    case light
    case dark

    static let storageKey = "pier.appearance"

    var id: String { rawValue }

    var label: String {
        switch self {
        case .system: "System"
        case .light: "Light"
        case .dark: "Dark"
        }
    }

    var systemImage: String {
        switch self {
        case .system: "circle.lefthalf.filled"
        case .light: "sun.max"
        case .dark: "moon"
        }
    }

    var preferredColorScheme: ColorScheme? {
        switch self {
        case .system: nil
        case .light: .light
        case .dark: .dark
        }
    }

    func terminalTheme(systemColorScheme: ColorScheme) -> PierTerminalTheme {
        switch self {
        case .system: systemColorScheme == .dark ? .dark : .light
        case .light: .light
        case .dark: .dark
        }
    }

    static var stored: PierAppearance {
        PierAppearance(rawValue: UserDefaults.standard.string(forKey: storageKey) ?? "") ?? .system
    }
}

enum PierTerminalTheme: String, Equatable {
    case light
    case dark

    var configurationResource: String {
        "pier-ghostty-\(rawValue)"
    }
}

enum PierTerminalFontSize {
    static let storageKey = "pier.terminal-font-size"
    static let range = 8.0...20.0
    static let step = 1.0

    #if os(iOS)
    static let defaultValue = 10.0
    #else
    static let defaultValue = 13.0
    #endif

    static func normalized(_ value: Double) -> Double {
        min(max(value.rounded(), range.lowerBound), range.upperBound)
    }

    static var stored: Double {
        guard UserDefaults.standard.object(forKey: storageKey) != nil else { return defaultValue }
        return normalized(UserDefaults.standard.double(forKey: storageKey))
    }
}

enum PierTheme {
    static let accent = Color(red: 0, green: 122 / 255, blue: 1)
    static let canvas = adaptive(light: 0xFDFCFC, dark: 0x201D1D)
    static let surfaceSoft = adaptive(light: 0xF8F7F7, dark: 0x262222)
    static let surfaceCard = adaptive(light: 0xF1EEEE, dark: 0x302C2C)
    static let ink = adaptive(light: 0x201D1D, dark: 0xFDFCFC)
    static let body = adaptive(light: 0x424245, dark: 0xCFCCCC)
    static let muted = adaptive(light: 0x646262, dark: 0x9A9898)
    static let hairline = adaptive(light: 0xD9D6D6, dark: 0x4C4747)

    #if os(macOS)
    static let nativeTerminalBackground = adaptiveNSColor(light: 0xFDFCFC, dark: 0x201D1D)

    private static func adaptive(light: UInt32, dark: UInt32) -> Color {
        Color(nsColor: adaptiveNSColor(light: light, dark: dark))
    }

    private static func adaptiveNSColor(light: UInt32, dark: UInt32) -> NSColor {
        NSColor(name: nil) { appearance in
            let isDark = appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            return nsColor(isDark ? dark : light)
        }
    }

    private static func nsColor(_ rgb: UInt32) -> NSColor {
        NSColor(
            red: CGFloat((rgb >> 16) & 0xFF) / 255,
            green: CGFloat((rgb >> 8) & 0xFF) / 255,
            blue: CGFloat(rgb & 0xFF) / 255,
            alpha: 1
        )
    }
    #elseif os(iOS)
    static let nativeTerminalBackground = adaptiveUIColor(light: 0xFDFCFC, dark: 0x201D1D)

    private static func adaptive(light: UInt32, dark: UInt32) -> Color {
        Color(uiColor: adaptiveUIColor(light: light, dark: dark))
    }

    private static func adaptiveUIColor(light: UInt32, dark: UInt32) -> UIColor {
        UIColor { traits in
            uiColor(traits.userInterfaceStyle == .dark ? dark : light)
        }
    }

    private static func uiColor(_ rgb: UInt32) -> UIColor {
        UIColor(
            red: CGFloat((rgb >> 16) & 0xFF) / 255,
            green: CGFloat((rgb >> 8) & 0xFF) / 255,
            blue: CGFloat(rgb & 0xFF) / 255,
            alpha: 1
        )
    }
    #endif
}

extension View {
    func pierScrollSurface() -> some View {
        scrollContentBackground(.hidden)
            .background(PierTheme.canvas)
    }
}
