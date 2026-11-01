import XCTest

/// The app on its demo backend: sessions, one session, a terminal.
final class FlowTests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    func testSessionsToATerminal() throws {
        let app = XCUIApplication()
        app.launchEnvironment["PIER_IOS_DEMO"] = "1"
        app.launch()

        let session = app.staticTexts["checkout-flow"]
        XCTAssertTrue(session.waitForExistence(timeout: 10))
        keep(app, "sessions")

        session.tap()
        XCTAssertTrue(app.staticTexts["Pay with saved cards"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["needs permission"].exists)
        keep(app, "session")

        app.staticTexts["server"].tap()
        XCTAssertTrue(app.buttons["Done"].waitForExistence(timeout: 10))
        sleep(2)
        keep(app, "terminal")
        app.buttons["Done"].tap()

        // A parked session offers waking, and nothing to open.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        app.staticTexts["search-v2"].tap()
        XCTAssertTrue(app.buttons["Wake"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["Terminals"].exists)
    }

    private func keep(_ app: XCUIApplication, _ name: String) {
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
    }
}
