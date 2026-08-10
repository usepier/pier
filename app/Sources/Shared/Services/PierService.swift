import Foundation

protocol PierServicing: Sendable {
    func setupStatus() async throws -> PierSetupStatus
    func listInstances() async throws -> [PierInstance]
    func listProjects() async throws -> [PierProject]
    func addProject(path: String) async throws -> PierProject
    func listBranches(projectID: String) async throws -> PierBranchOptions
    func createInstance(
        projectID: String,
        name: String,
        baseBranch: String,
        onProgress: @escaping @Sendable (PierCreationEvent) -> Void
    ) async throws -> PierInstance
    func unparkInstance(id: String) async throws
    func removeInstance(id: String) async throws
    func inspectInstance(id: String) async throws -> PierInstanceSnapshot
    func preparePortProxy() async throws
    func openPort(proxyHost: String, port: PierPort) async throws -> PierPortEndpoint
    func createTab(instanceID: String, name: String, command: [String]) async throws -> PierTab
    func closeTab(instanceID: String, tabID: String) async throws
    func signInAWS() async throws
    func beginMobileSignIn(_ request: PierMobileSignInRequest) async throws -> PierMobileAuthorization
    func completeMobileSignIn() async throws -> [PierAWSAccount]
    func listMobileRoles(accountID: String) async throws -> [PierAWSRole]
    func finishMobileSetup(accountID: String, roleName: String) async throws
    func signOutMobile() async throws
}

extension PierServicing {
    func preparePortProxy() async throws {}

    func openPort(proxyHost: String, port: PierPort) async throws -> PierPortEndpoint {
        throw PierServiceFailure.unavailable("Opening port tunnels is currently available on macOS.")
    }

    func beginMobileSignIn(_ request: PierMobileSignInRequest) async throws -> PierMobileAuthorization {
        throw PierServiceFailure.unavailable("Mobile AWS sign-in is only available on iOS.")
    }

    func completeMobileSignIn() async throws -> [PierAWSAccount] {
        throw PierServiceFailure.unavailable("Mobile AWS sign-in is only available on iOS.")
    }

    func listMobileRoles(accountID: String) async throws -> [PierAWSRole] {
        throw PierServiceFailure.unavailable("Mobile AWS sign-in is only available on iOS.")
    }

    func finishMobileSetup(accountID: String, roleName: String) async throws {
        throw PierServiceFailure.unavailable("Mobile AWS sign-in is only available on iOS.")
    }

    func signOutMobile() async throws {
        throw PierServiceFailure.unavailable("Mobile AWS sign-out is only available on iOS.")
    }
}

enum PierServiceFailure: LocalizedError, Sendable {
    case unavailable(String)
    case commandFailed(String)
    case invalidResponse(String)

    var errorDescription: String? {
        switch self {
        case .unavailable(let message), .commandFailed(let message), .invalidResponse(let message):
            message
        }
    }
}

enum PierServiceFactory {
    static func make() -> any PierServicing {
        #if os(macOS)
        PierCLIService()
        #else
        PierMobileService()
        #endif
    }
}
