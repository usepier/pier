import Foundation

enum PierInstanceState: String, Codable, CaseIterable, Sendable {
    case creating
    case running
    case working
    case idle
    case parked
    case dead

    var label: String {
        rawValue.capitalized
    }

    var systemImage: String {
        switch self {
        case .creating: "shippingbox.and.arrow.backward"
        case .running: "bolt.fill"
        case .working: "hammer.fill"
        case .idle: "moon.fill"
        case .parked: "pause.circle.fill"
        case .dead: "exclamationmark.triangle.fill"
        }
    }
}

struct PierInstance: Identifiable, Codable, Hashable, Sendable {
    let id: String
    let name: String
    let repo: String
    let branch: String
    let user: String
    let driver: String
    let state: PierInstanceState
    let strained: Bool
    let setup: String
    let instanceType: String
    let createdAt: String
    let costNote: String
    let localPath: String?
    let projectID: String?

    var subtitle: String {
        repo == branch || branch.isEmpty ? repo : "\(repo) · \(branch)"
    }

    var displayBranch: String {
        branch.isEmpty ? name : branch
    }

    var displayLocalPath: String {
        localPath ?? repo
    }

    var projectName: String {
        let name = URL(fileURLWithPath: repo).lastPathComponent
        return name.isEmpty ? repo : name
    }
}

struct PierProject: Identifiable, Codable, Hashable, Sendable {
    let id: String
    let name: String
    let path: String
}

struct PierBranchOptions: Codable, Hashable, Sendable {
    let project: PierProject
    let branches: [String]
    let defaultBranch: String
    let fetchWarning: String?
}

enum PierCreationStatus: Equatable, Sendable {
    case provisioning
    case completed
    case failed(String)
}

enum PierCreationEvent: Equatable, Sendable {
    case step(String)
    case setupStarted(String)
    case setupOutput(String)
    case setupFinished(name: String, exitCode: Int)
}

enum PierCustomSetupStatus: Equatable, Sendable {
    case running
    case completed
    case failed(Int)
}

struct PierCustomSetupProgress: Equatable, Sendable {
    let name: String
    var output: [String]
    var status: PierCustomSetupStatus
}

struct PierCreationStep: Identifiable, Equatable, Sendable {
    let id = UUID()
    let message: String
}

struct PierCreationProgress: Equatable, Sendable {
    let instanceID: PierInstance.ID
    let project: PierProject
    let name: String
    let baseBranch: String
    var steps: [PierCreationStep]
    var status: PierCreationStatus
    var customSetup: PierCustomSetupProgress? = nil
}

struct PierTab: Identifiable, Codable, Hashable, Sendable {
    let id: String
    let name: String
    let active: Bool
    let panes: Int
    let command: String
    let workingDirectory: String
}

struct PierPort: Identifiable, Codable, Hashable, Sendable {
    var id: Int { number }
    let number: Int
    let process: String?
    let isHTTP: Bool
    let scheme: String?
}

struct PierPortEndpoint: Hashable, Sendable {
    let address: String
    let browserURL: URL?
}

struct PierInstanceSnapshot: Codable, Hashable, Sendable {
    let instance: PierInstance
    let proxyHost: String?
    let tabs: [PierTab]
    let ports: [PierPort]
}

struct PierDependency: Codable, Hashable, Identifiable, Sendable {
    var id: String { name }
    let name: String
    let available: Bool
    let detail: String
}

struct PierSetupStatus: Codable, Hashable, Sendable {
    let configured: Bool
    let configPath: String
    let cliVersion: String
    let profiles: [String]
    let dependencies: [PierDependency]
}

struct PierMobileSignInRequest: Codable, Hashable, Sendable {
    let startURL: String
    let ssoRegion: String
    let awsRegion: String
}

struct PierMobileAuthorization: Codable, Hashable, Sendable {
    let verificationURL: String
    let userCode: String
    let expiresAt: Date
}

struct PierAWSAccount: Identifiable, Codable, Hashable, Sendable {
    let id: String
    let name: String
    let email: String
}

struct PierAWSRole: Identifiable, Codable, Hashable, Sendable {
    var id: String { name }
    let name: String
}

struct PierAPIError: Codable, Hashable, LocalizedError, Sendable {
    let code: String
    let message: String

    var errorDescription: String? { message }
}

struct PierAPIEnvelope<Value: Decodable & Sendable>: Decodable, Sendable {
    let version: Int
    let data: Value?
    let error: PierAPIError?
}
