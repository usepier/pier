import SwiftUI
import XCTest
@testable import PierMacOS

final class PierAppearanceTests: XCTestCase {
    func testExplicitAppearanceOverridesSystemScheme() {
        XCTAssertEqual(PierAppearance.light.terminalTheme(systemColorScheme: .dark), .light)
        XCTAssertEqual(PierAppearance.dark.terminalTheme(systemColorScheme: .light), .dark)
    }

    func testSystemAppearanceTracksSystemScheme() {
        XCTAssertEqual(PierAppearance.system.terminalTheme(systemColorScheme: .light), .light)
        XCTAssertEqual(PierAppearance.system.terminalTheme(systemColorScheme: .dark), .dark)
    }

    func testTerminalFontSizeIsRoundedAndClamped() {
        XCTAssertEqual(PierTerminalFontSize.normalized(7.2), 8)
        XCTAssertEqual(PierTerminalFontSize.normalized(11.6), 12)
        XCTAssertEqual(PierTerminalFontSize.normalized(24), 20)
    }
}
