import XCTest
@testable import Pier

/// The core's JSON, as ios/core writes it, read into the app's models.
final class ModelTests: XCTestCase {
    func testSnapshot() throws {
        let json = """
        {"session":{"id":"i-1","name":"fix-login","repo":"shop","origin":"github.com/acme/shop","base":"main",
          "branch":"add-login","state":"working","machineType":"t4g.medium","createdAt":"2026-10-01T10:00:00Z"},
         "windows":[{"id":"@1","index":0,"active":false,"command":"zsh","role":"home","name":"zsh"},
                    {"id":"@4","index":1,"active":true,"command":"node","name":"server"}],
         "ports":[{"port":3000,"process":"node"}],
         "chats":[{"id":"c1","agent":"codex","title":"Add login","state":"waiting","waiting":"permission",
                   "since":"2026-11-01T11:00:00.123456Z","ask":"Run the tests?"}],
         "strained":true,"cpu":42.5,"memory":25,"parkAt":"2026-11-01T12:00:00Z"}
        """
        let snapshot = try PierCore.decoder.decode(SessionSnapshot.self, from: Data(json.utf8))
        XCTAssertEqual(snapshot.session.stateLabel, "working")
        XCTAssertTrue(snapshot.session.isUp)
        XCTAssertEqual(snapshot.windows.map(\.title), ["zsh", "server"])
        XCTAssertEqual(snapshot.ports.first?.port, 3000)
        XCTAssertEqual(snapshot.chats.first?.agentName, "Codex")
        XCTAssertEqual(snapshot.parkAt, try Date("2026-11-01T12:00:00Z", strategy: .iso8601))
    }

    func testParkedSessionHasNoTerminals() throws {
        let json = #"{"id":"i-2","name":"old","repo":"shop","branch":"old","state":"parked","machineType":"t4g.small"}"#
        let session = try PierCore.decoder.decode(Session.self, from: Data(json.utf8))
        XCTAssertTrue(session.isParked)
        XCTAssertFalse(session.isUp)
    }

    func testDeviceAuthorizationExpiryIsSecondsSince2001() throws {
        let json = #"{"verificationURL":"https://device.sso.aws/?code=X","userCode":"ABCD-EFGH","expiresAt":815220000}"#
        let authorization = try PierCore.decoder.decode(DeviceAuthorization.self, from: Data(json.utf8))
        XCTAssertEqual(authorization.expiresAt, Date(timeIntervalSinceReferenceDate: 815_220_000))
    }

    func testExpiredSignInIsRecognized() {
        let error = PierError.from(NSError(domain: "go", code: 1, userInfo: [
            NSLocalizedDescriptionKey: "aws_login_required: Your AWS session has expired.",
        ]))
        XCTAssertTrue(error.isSignInExpired)
        XCTAssertEqual(error.localizedDescription, "Your AWS session has expired.")
    }
}
