import Foundation
@preconcurrency import PierCore
import Security

/// What the app asks of pier: the Go core, or (debug builds, PIER_IOS_DEMO=1)
/// a demo with made-up sessions.
protocol Backend: Sendable {
    func status() async throws -> SignInStatus
    func beginSignIn(_ request: SignInRequest) async throws -> DeviceAuthorization
    func completeSignIn() async throws -> [AWSAccount]
    func roles(accountID: String) async throws -> [AWSRole]
    func finishSetup(accountID: String, roleName: String) async throws
    func signOut() async throws
    func sessions() async throws -> [Session]
    func inspect(_ id: String) async throws -> SessionSnapshot
    func wake(_ id: String) async throws
    func park(_ id: String) async throws
    func remove(_ id: String) async throws
    func newWindow(sessionID: String, name: String, command: [String]) async throws -> TmuxWindow
    func chatWindow(sessionID: String, chatID: String, takeover: Bool) async throws -> String
    func closeWindow(sessionID: String, windowID: String) async throws
    func openTerminal(sessionID: String, windowID: String, columns: Int, rows: Int, listener: TerminalListener) async throws -> any Terminal
}

/// A live terminal.
protocol Terminal: Sendable {
    func write(_ data: Data) throws
    func resize(columns: Int, rows: Int) throws
    func close()
}

enum Backends {
    static let current: any Backend = {
        #if DEBUG
        if ProcessInfo.processInfo.environment["PIER_IOS_DEMO"] == "1" { return DemoBackend() }
        #endif
        return PierCore.shared
    }()
}

/// The Go core (ios/core) behind an actor: every call is a blocking network
/// round trip, so none runs on the main thread. After each one the sign-in
/// goes back to the Keychain, as a token refresh may have changed it.
actor PierCore: Backend {
    static let shared = PierCore()

    private let keychain = Keychain()
    private let client: PierPiercoreClient
    private let encoder = JSONEncoder()
    /// Reads the core's JSON.
    static let decoder: JSONDecoder = {
        let decoder = JSONDecoder()
        // Go writes times as RFC 3339, the device code's expiry as seconds
        // since 2001.
        decoder.dateDecodingStrategy = .custom { decoder in
            let value = try decoder.singleValueContainer()
            if let seconds = try? value.decode(Double.self) { return Date(timeIntervalSinceReferenceDate: seconds) }
            let text = try value.decode(String.self)
            if let date = (try? Date(text, strategy: .iso8601))
                ?? (try? Date(text, strategy: Date.ISO8601FormatStyle(includingFractionalSeconds: true))) {
                return date
            }
            throw DecodingError.dataCorruptedError(in: value, debugDescription: "not a date: \(text)")
        }
        return decoder
    }()

    private init() {
        let stored = (try? keychain.load()) ?? ""
        SignInDefaults.restore(from: stored)
        var error: NSError?
        if let client = PierPiercoreNewClient(stored, &error) {
            self.client = client
        } else {
            // A damaged Keychain item mustn't keep the app from opening: sign
            // in afresh instead.
            try? keychain.delete()
            guard let client = PierPiercoreNewClient("", &error) else {
                fatalError("PierCore didn't start: \(error?.localizedDescription ?? "unknown error")")
            }
            self.client = client
        }
    }

    func status() throws -> SignInStatus {
        try decode(call { client.status($0) })
    }

    func beginSignIn(_ request: SignInRequest) throws -> DeviceAuthorization {
        let authorization: DeviceAuthorization = try decode(call {
            client.beginSign(in: request.startURL, ssoRegion: request.ssoRegion, awsRegion: request.awsRegion, error: $0)
        })
        SignInDefaults.save(request)
        return authorization
    }

    func completeSignIn() throws -> [AWSAccount] {
        let accounts: [AWSAccount] = try decode(call { client.completeSign(in: $0) })
        try keep()
        return accounts
    }

    func roles(accountID: String) throws -> [AWSRole] {
        let roles: [AWSRole] = try decode(call { client.listRoles(accountID, error: $0) })
        try keep()
        return roles
    }

    func finishSetup(accountID: String, roleName: String) throws {
        try run { try client.finishSetup(accountID, roleName: roleName) }
        try keep()
    }

    func refreshSignIn() throws {
        try run { try client.refreshSignIn() }
        try keep()
    }

    func signOut() throws {
        try run { try client.signOut() }
        try keychain.delete()
    }

    func sessions() throws -> [Session] {
        let sessions: [Session] = try decode(call { client.listSessions($0) })
        try keep()
        return sessions
    }

    func inspect(_ id: String) throws -> SessionSnapshot {
        let snapshot: SessionSnapshot = try decode(call { client.inspect(id, error: $0) })
        try keep()
        return snapshot
    }

    func wake(_ id: String) throws {
        try run { try client.wake(id) }
        try keep()
    }

    func park(_ id: String) throws {
        try run { try client.park(id) }
        try keep()
    }

    func remove(_ id: String) throws {
        try run { try client.remove(id) }
        try keep()
    }

    func newWindow(sessionID: String, name: String, command: [String]) throws -> TmuxWindow {
        let commandJSON = String(decoding: try encoder.encode(command), as: UTF8.self)
        let window: TmuxWindow = try decode(call {
            client.newWindow(sessionID, name: name, commandJSON: commandJSON, error: $0)
        })
        try keep()
        return window
    }

    func chatWindow(sessionID: String, chatID: String, takeover: Bool) throws -> String {
        let window = try call { client.chatWindow(sessionID, chatID: chatID, takeover: takeover, error: $0) }
        try keep()
        return window
    }

    func closeWindow(sessionID: String, windowID: String) throws {
        try run { try client.closeWindow(sessionID, window: windowID) }
        try keep()
    }

    func openTerminal(
        sessionID: String,
        windowID: String,
        columns: Int,
        rows: Int,
        listener: TerminalListener
    ) throws -> any Terminal {
        let terminal = try value {
            try client.openTerminal(sessionID, window: windowID, columns: columns, rows: rows, listener: listener)
        }
        try keep()
        return TerminalHandle(terminal)
    }

    // MARK: - Plumbing

    private func keep() throws {
        try keychain.save(call { client.exportSession($0) })
    }

    private func decode<Value: Decodable>(_ json: String) throws -> Value {
        do {
            return try Self.decoder.decode(Value.self, from: Data(json.utf8))
        } catch {
            throw PierError.badResponse("PierCore answered something unexpected: \(error.localizedDescription)")
        }
    }

    private typealias ErrorPointer = AutoreleasingUnsafeMutablePointer<NSError?>?

    private func run(_ operation: () throws -> Void) throws {
        do { try operation() } catch { throw PierError.from(error) }
    }

    private func value<Value>(_ operation: () throws -> Value) throws -> Value {
        do { return try operation() } catch { throw PierError.from(error) }
    }

    private func call(_ operation: (ErrorPointer) -> String) throws -> String {
        var error: NSError?
        let value = operation(&error)
        if let error { throw PierError.from(error) }
        return value
    }
}

enum PierError: LocalizedError, Sendable {
    case signInExpired(String)
    case badResponse(String)
    case failed(String)

    /// The core marks an expired sign-in so the app can offer to sign in again.
    static let signInExpiredMark = "aws_login_required:"

    static func from(_ error: Error) -> PierError {
        let text = error.localizedDescription
        if let mark = text.range(of: signInExpiredMark) {
            let message = text[mark.upperBound...].trimmingCharacters(in: .whitespacesAndNewlines)
            return .signInExpired(message.isEmpty ? "Your AWS sign-in expired. Sign in again to reconnect." : message)
        }
        return .failed(text)
    }

    var errorDescription: String? {
        switch self {
        case .signInExpired(let message), .badResponse(let message), .failed(let message): message
        }
    }

    var isSignInExpired: Bool {
        if case .signInExpired = self { return true }
        return false
    }
}

/// The portal and regions last signed in with, to fill the form next time.
enum SignInDefaults {
    static let startURLKey = "pier.signin.startURL"
    static let ssoRegionKey = "pier.signin.ssoRegion"
    static let awsRegionKey = "pier.signin.awsRegion"

    private struct Stored: Decodable {
        let request: SignInRequest
    }

    static func save(_ request: SignInRequest, defaults: UserDefaults = .standard) {
        defaults.set(request.startURL, forKey: startURLKey)
        defaults.set(request.ssoRegion, forKey: ssoRegionKey)
        defaults.set(request.awsRegion, forKey: awsRegionKey)
    }

    static func restore(from sessionJSON: String, defaults: UserDefaults = .standard) {
        guard let stored = try? JSONDecoder().decode(Stored.self, from: Data(sessionJSON.utf8)),
              !stored.request.startURL.isEmpty else { return }
        save(stored.request, defaults: defaults)
    }
}

/// A live terminal in the core.
final class TerminalHandle: Terminal, @unchecked Sendable {
    private let terminal: PierPiercoreTerminal

    init(_ terminal: PierPiercoreTerminal) {
        self.terminal = terminal
    }

    func write(_ data: Data) throws { try terminal.write(data) }
    func resize(columns: Int, rows: Int) throws { try terminal.resize(columns, rows: rows) }
    func close() { try? terminal.close() }
}

/// What the core calls back with: output, on one of Go's threads, and the end.
final class TerminalListener: NSObject, PierPiercoreTerminalListenerProtocol, @unchecked Sendable {
    private let onOutput: @Sendable (Data) -> Void
    private let onClose: @Sendable (String) -> Void

    init(output: @escaping @Sendable (Data) -> Void, closed: @escaping @Sendable (String) -> Void) {
        onOutput = output
        onClose = closed
    }

    func output(_ data: Data?) {
        if let data { onOutput(data) }
    }

    func closed(_ message: String?) {
        onClose(message ?? "")
    }
}

/// The sign-in in the Keychain, on this device only.
private struct Keychain: Sendable {
    private let service = "com.usepier.pier.aws"
    private let account = "piercore-session"

    private var query: [String: Any] {
        [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: account]
    }

    func load() throws -> String? {
        var query = query
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = result as? Data else {
            throw PierError.failed("Couldn't read the sign-in from the Keychain (\(status)).")
        }
        return String(decoding: data, as: UTF8.self)
    }

    func save(_ value: String) throws {
        let attributes: [String: Any] = [
            kSecValueData as String: Data(value.utf8),
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        let updated = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if updated == errSecItemNotFound {
            let added = SecItemAdd(query.merging(attributes) { $1 } as CFDictionary, nil)
            guard added == errSecSuccess else {
                throw PierError.failed("Couldn't keep the sign-in in the Keychain (\(added)).")
            }
        } else if updated != errSecSuccess {
            throw PierError.failed("Couldn't update the sign-in in the Keychain (\(updated)).")
        }
    }

    func delete() throws {
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw PierError.failed("Couldn't clear the sign-in from the Keychain (\(status)).")
        }
    }
}
