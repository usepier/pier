import Foundation

// What PierCore (ios/core) hands over, as its JSON says it.

struct SignInRequest: Codable, Hashable, Sendable {
    let startURL: String
    let ssoRegion: String
    let awsRegion: String
}

struct SignInStatus: Codable, Hashable, Sendable {
    let signedIn: Bool
    /// An account and permission set are picked.
    let ready: Bool
    var accountID: String?
    var roleName: String?
    var region: String?
    var startURL: String?
}

struct DeviceAuthorization: Codable, Hashable, Sendable {
    let verificationURL: String
    let userCode: String
    let expiresAt: Date
}

struct AWSAccount: Identifiable, Codable, Hashable, Sendable {
    let id: String
    let name: String
    let email: String
}

struct AWSRole: Identifiable, Codable, Hashable, Sendable {
    var id: String { name }
    let name: String
}

/// One of the user's pier sessions.
struct Session: Identifiable, Codable, Hashable, Sendable {
    let id: String
    let name: String
    let repo: String
    var origin: String?
    var base: String?
    var branch: String
    /// creating, running, working, idle or parked.
    var state: String
    let machineType: String
    var createdAt: String?

    var isParked: Bool { state == "parked" }
    var isCreating: Bool { state == "creating" }
    var isUp: Bool { !isParked && !isCreating }

    var stateLabel: String {
        switch state {
        case "creating": "starting"
        case "working": "working"
        case "idle": "idle"
        case "parked": "parked"
        default: "running"
        }
    }
}

/// A tmux window of the session's main.
struct TmuxWindow: Identifiable, Codable, Hashable, Sendable {
    let id: String
    let index: Int
    let active: Bool
    let command: String
    var role: String?
    let name: String

    var title: String { name.isEmpty ? command : name }
}

struct ListeningPort: Identifiable, Codable, Hashable, Sendable {
    var id: Int { port }
    let port: Int
    let process: String
}

/// One of the session's agent chats, as its supervisor sums it up.
struct ChatSummary: Identifiable, Codable, Hashable, Sendable {
    let id: String
    let agent: String
    let title: String
    let state: String
    var waiting: String?
    var last: String?
    var ask: String?
    var queued: Int?

    var agentName: String { agent == "codex" ? "Codex" : "Claude" }
}

/// What a running session is doing.
struct SessionSnapshot: Codable, Hashable, Sendable {
    let session: Session
    let windows: [TmuxWindow]
    let ports: [ListeningPort]
    let chats: [ChatSummary]
    let strained: Bool
    var setup: String?
    let cpu: Double
    let memory: Double
    var parkAt: Date?
    var capAt: Date?
}
