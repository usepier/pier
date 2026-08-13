#if os(iOS)
import Foundation
@preconcurrency import PierCore
import Security

struct PierMobileService: PierServicing {
    private let core = PierMobileCore.shared

    func setupStatus() async throws -> PierSetupStatus { try await core.setupStatus() }
    func listInstances() async throws -> [PierInstance] { try await core.listInstances() }
    func listProjects() async throws -> [PierProject] { try await core.listProjects() }

    func addProject(path: String) async throws -> PierProject {
        throw PierServiceFailure.unavailable("On iOS, projects are discovered from Pier instances in AWS.")
    }

    func listBranches(projectID: String) async throws -> PierBranchOptions {
        try await core.listBranches(projectID: projectID)
    }

    func createInstance(
        projectID: String,
        name: String,
        baseBranch: String,
        onProgress: @escaping @Sendable (PierCreationEvent) -> Void
    ) async throws -> PierInstance {
        throw PierServiceFailure.unavailable(
            "Creating a VM still needs the repository snapshot from the Mac. Existing sessions are fully manageable from iOS."
        )
    }

    func unparkInstance(id: String) async throws { try await core.unparkInstance(id: id) }
    func removeInstance(id: String) async throws { try await core.removeInstance(id: id) }
    func inspectInstance(id: String) async throws -> PierInstanceSnapshot { try await core.inspectInstance(id: id) }
    func createTab(instanceID: String, name: String, command: [String]) async throws -> PierTab {
        try await core.createTab(instanceID: instanceID, name: name, command: command)
    }
    func closeTab(instanceID: String, tabID: String) async throws {
        try await core.closeTab(instanceID: instanceID, tabID: tabID)
    }
    func signInAWS() async throws { try await core.refreshSignIn() }
    func beginMobileSignIn(_ request: PierMobileSignInRequest) async throws -> PierMobileAuthorization {
        try await core.beginSignIn(request)
    }
    func completeMobileSignIn() async throws -> [PierAWSAccount] { try await core.completeSignIn() }
    func listMobileRoles(accountID: String) async throws -> [PierAWSRole] {
        try await core.listRoles(accountID: accountID)
    }
    func finishMobileSetup(accountID: String, roleName: String) async throws {
        try await core.finishSetup(accountID: accountID, roleName: roleName)
    }
    func signOutMobile() async throws { try await core.signOut() }
}

actor PierMobileCore {
    static let shared = PierMobileCore()

    private let keychain = PierMobileKeychain()
    private let client: PierPiercoreClient
    private let decoder = JSONDecoder()
    private let encoder = JSONEncoder()

    private init() {
        let stored = (try? keychain.load()) ?? ""
        PierMobileSignInStorage.restoreRequest(from: stored)
        var restoreError: NSError?
        if let client = PierPiercoreNewClient(stored, &restoreError) {
            self.client = client
        } else {
            // A damaged Keychain value must not make the app unlaunchable. The
            // empty client leads the user through a fresh AWS sign-in instead.
            try? keychain.delete()
            var freshError: NSError?
            guard let client = PierPiercoreNewClient("", &freshError) else {
                fatalError("PierCore could not initialize: \(freshError?.localizedDescription ?? "Unknown error")")
            }
            self.client = client
        }
    }

    func setupStatus() throws -> PierSetupStatus {
        try decode(coreString { client.setupStatus($0) })
    }

    func beginSignIn(_ request: PierMobileSignInRequest) throws -> PierMobileAuthorization {
        let authorization: PierMobileAuthorization = try decode(coreString {
            client.beginSign(in: request.startURL, ssoRegion: request.ssoRegion, awsRegion: request.awsRegion, error: $0)
        })
        PierMobileSignInStorage.save(request)
        return authorization
    }

    func completeSignIn() throws -> [PierAWSAccount] {
        let accounts: [PierAWSAccount] = try decode(coreString { client.completeSign(in: $0) })
        try persistSession()
        return accounts
    }

    func listRoles(accountID: String) throws -> [PierAWSRole] {
        let roles: [PierAWSRole] = try decode(coreString { client.listRoles(accountID, error: $0) })
        try persistSession()
        return roles
    }

    func finishSetup(accountID: String, roleName: String) throws {
        try coreOperation { try client.finishSetup(accountID, roleName: roleName) }
        try persistSession()
    }

    func refreshSignIn() throws {
        try coreOperation { try client.refreshSignIn() }
        try persistSession()
    }

    func signOut() throws {
        try coreOperation { try client.signOut() }
        try keychain.delete()
    }

    func listInstances() throws -> [PierInstance] {
        let instances: [PierInstance] = try decode(coreString { client.listInstances($0) })
        try persistSession()
        return instances
    }

    func listProjects() throws -> [PierProject] {
        let projects: [PierProject] = try decode(coreString { client.listProjects($0) })
        try persistSession()
        return projects
    }

    func listBranches(projectID: String) throws -> PierBranchOptions {
        let branches: PierBranchOptions = try decode(coreString { client.listBranches(projectID, error: $0) })
        try persistSession()
        return branches
    }

    func removeInstance(id: String) throws {
        try coreOperation { try client.removeInstance(id) }
        try persistSession()
    }

    func unparkInstance(id: String) throws {
        try coreOperation { try client.unparkInstance(id) }
        try persistSession()
    }

    func inspectInstance(id: String) throws -> PierInstanceSnapshot {
        let snapshot: PierInstanceSnapshot = try decode(coreString { client.inspectInstance(id, error: $0) })
        try persistSession()
        return snapshot
    }

    func createTab(instanceID: String, name: String, command: [String]) throws -> PierTab {
        let commandData = try encoder.encode(command)
        guard let commandJSON = String(data: commandData, encoding: .utf8) else {
            throw PierServiceFailure.invalidResponse("Could not encode the tab command.")
        }
        let tab: PierTab = try decode(coreString {
            client.createTab(instanceID, name: name, commandJSON: commandJSON, error: $0)
        })
        try persistSession()
        return tab
    }

    func closeTab(instanceID: String, tabID: String) throws {
        try coreOperation { try client.closeTab(instanceID, tabID: tabID) }
        try persistSession()
    }

    func openTerminal(
        instanceID: String,
        tabID: String,
        listener: PierMobileTerminalListener
    ) throws -> PierMobileTerminalHandle {
        let terminal = try coreValue {
            try client.openTerminal(instanceID, tabID: tabID, listener: listener)
        }
        try persistSession()
        return PierMobileTerminalHandle(terminal)
    }

    private func decode<Value: Decodable>(_ json: String) throws -> Value {
        guard let data = json.data(using: .utf8) else {
            throw PierServiceFailure.invalidResponse("PierCore returned invalid text.")
        }
        do {
            return try decoder.decode(Value.self, from: data)
        } catch {
            throw PierServiceFailure.invalidResponse("PierCore returned an invalid response: \(error.localizedDescription)")
        }
    }

    private func persistSession() throws {
        try keychain.save(coreString { client.exportSession($0) })
    }

    private typealias ErrorPointer = AutoreleasingUnsafeMutablePointer<NSError?>?

    private func coreOperation(_ operation: () throws -> Void) throws {
        do {
            try operation()
        } catch {
            throw mappedCoreError(error)
        }
    }

    private func coreValue<Value>(_ operation: () throws -> Value) throws -> Value {
        do {
            return try operation()
        } catch {
            throw mappedCoreError(error)
        }
    }

    private func coreString(_ operation: (ErrorPointer) -> String) throws -> String {
        var operationError: NSError?
        let value = operation(&operationError)
        if let operationError { throw mappedCoreError(operationError) }
        return value
    }

    private func mappedCoreError(_ error: Error) -> Error {
        let description = error.localizedDescription
        guard let markerRange = description.range(of: PierMobileSignInStorage.authenticationRequiredMarker) else {
            return error
        }
        let message = description[markerRange.upperBound...]
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return PierServiceFailure.authenticationRequired(
            message.isEmpty ? "Your AWS session has expired. Sign in again to reconnect Pier." : message
        )
    }

}

enum PierMobileSignInStorage {
    static let startURLKey = "pier.mobile.startURL"
    static let ssoRegionKey = "pier.mobile.ssoRegion"
    static let awsRegionKey = "pier.mobile.awsRegion"
    static let authenticationRequiredMarker = "aws_login_required:"

    private struct StoredSession: Decodable {
        let request: PierMobileSignInRequest
    }

    static func save(_ request: PierMobileSignInRequest, defaults: UserDefaults = .standard) {
        defaults.set(request.startURL, forKey: startURLKey)
        defaults.set(request.ssoRegion, forKey: ssoRegionKey)
        defaults.set(request.awsRegion, forKey: awsRegionKey)
    }

    static func restoreRequest(from sessionJSON: String, defaults: UserDefaults = .standard) {
        guard let data = sessionJSON.data(using: .utf8),
              let stored = try? JSONDecoder().decode(StoredSession.self, from: data) else { return }
        save(stored.request, defaults: defaults)
    }
}

final class PierMobileTerminalHandle: @unchecked Sendable {
    private let terminal: PierPiercoreTerminal

    init(_ terminal: PierPiercoreTerminal) {
        self.terminal = terminal
    }

    func write(_ data: Data) throws {
        try terminal.write(data)
    }

    func resize(columns: Int, rows: Int) throws {
        try terminal.resize(columns, rows: rows)
    }

    func close() {
        try? terminal.close()
    }
}

final class PierMobileTerminalListener: NSObject, PierPiercoreTerminalListenerProtocol, @unchecked Sendable {
    private let outputHandler: @Sendable (Data) -> Void
    private let closeHandler: @Sendable (String) -> Void

    init(
        output: @escaping @Sendable (Data) -> Void,
        closed: @escaping @Sendable (String) -> Void
    ) {
        outputHandler = output
        closeHandler = closed
    }

    func output(_ data: Data?) {
        guard let data else { return }
        outputHandler(data)
    }

    func closed(_ message: String?) {
        closeHandler(message ?? "")
    }
}

private struct PierMobileKeychain: Sendable {
    private let service = "com.pier.client.mobile-aws"
    private let account = "piercore-session"

    func load() throws -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess,
              let data = result as? Data,
              let value = String(data: data, encoding: .utf8) else {
            throw PierServiceFailure.unavailable("Could not read AWS credentials from Keychain (\(status)).")
        }
        return value
    }

    func save(_ value: String) throws {
        let data = Data(value.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        let attributes: [String: Any] = [
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        let updated = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if updated == errSecItemNotFound {
            var insert = query
            attributes.forEach { insert[$0.key] = $0.value }
            let inserted = SecItemAdd(insert as CFDictionary, nil)
            guard inserted == errSecSuccess else {
                throw PierServiceFailure.unavailable("Could not save AWS credentials to Keychain (\(inserted)).")
            }
        } else if updated != errSecSuccess {
            throw PierServiceFailure.unavailable("Could not update AWS credentials in Keychain (\(updated)).")
        }
    }

    func delete() throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        let status = SecItemDelete(query as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw PierServiceFailure.unavailable("Could not reset AWS credentials in Keychain (\(status)).")
        }
    }
}
#endif
