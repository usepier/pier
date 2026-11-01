import Foundation
import Observation

/// The app's state: the sign-in, the user's sessions and what each running
/// one is doing, and what's in flight.
@MainActor
@Observable
final class AppModel {
    private let core = Backends.current

    private(set) var status: SignInStatus?
    private(set) var sessions: [Session] = []
    private(set) var snapshots: [Session.ID: SessionSnapshot] = [:]
    private(set) var loaded = false
    /// Sessions something is being done to: waking, parking, removing.
    private(set) var busy: [Session.ID: String] = [:]
    private(set) var inspecting: Set<Session.ID> = []
    /// Why a running session couldn't be reached on the last look (sshd
    /// still starting after a wake, a network that blocks port 22, …).
    private(set) var unreachable: [Session.ID: String] = [:]

    // Signing in
    private(set) var authorization: DeviceAuthorization?
    private(set) var authorizing = false
    private(set) var accounts: [AWSAccount] = []
    private(set) var roles: [AWSRole] = []
    private(set) var loadingRoles = false
    /// The sign-in expired: the form says so and keeps the account picked.
    private(set) var reconnecting = false

    var error: PierError?

    var ready: Bool { status?.ready == true && !reconnecting }

    /// Sessions by repo, repos in name order.
    var byRepo: [(repo: String, sessions: [Session])] {
        Dictionary(grouping: sessions, by: \.repo)
            .map { (repo: $0.key, sessions: $0.value.sorted { $0.name.localizedStandardCompare($1.name) == .orderedAscending }) }
            .sorted { $0.repo.localizedStandardCompare($1.repo) == .orderedAscending }
    }

    func session(_ id: Session.ID) -> Session? {
        snapshots[id]?.session ?? sessions.first { $0.id == id }
    }

    func start() async {
        await perform { self.status = try await self.core.status() }
    }

    // MARK: Sessions

    func refresh() async {
        guard ready else { return }
        await perform {
            self.sessions = try await self.core.sessions()
            self.loaded = true
            let ids = Set(self.sessions.map(\.id))
            self.snapshots = self.snapshots.filter { ids.contains($0.key) }
        }
    }

    func inspect(_ id: Session.ID) async {
        guard ready, let s = sessions.first(where: { $0.id == id }), s.isUp, !inspecting.contains(id) else { return }
        inspecting.insert(id)
        defer { inspecting.remove(id) }
        // A look runs every few seconds: a miss says so in place, not in an alert.
        do {
            snapshots[id] = try await core.inspect(id)
            unreachable[id] = nil
        } catch {
            let e = (error as? PierError) ?? PierError.from(error)
            if e.isSignInExpired { reconnecting = true } else { unreachable[id] = e.localizedDescription }
        }
    }

    func wake(_ id: Session.ID) async {
        await act(id, "Waking") {
            try await self.core.wake(id)
            self.sessions = try await self.core.sessions()
        }
    }

    func park(_ id: Session.ID) async {
        await act(id, "Parking") {
            try await self.core.park(id)
            self.snapshots[id] = nil
            self.sessions = try await self.core.sessions()
        }
    }

    func remove(_ id: Session.ID) async {
        await act(id, "Removing") {
            try await self.core.remove(id)
            self.sessions.removeAll { $0.id == id }
            self.snapshots[id] = nil
        }
    }

    func newWindow(in id: Session.ID, name: String, command: [String]) async -> TmuxWindow? {
        var made: TmuxWindow?
        await perform {
            made = try await self.core.newWindow(sessionID: id, name: name, command: command)
            self.snapshots[id] = try await self.core.inspect(id)
        }
        return made
    }

    /// The session's chats in a tmux window: watching them, or — takeover —
    /// carrying one on in its agent's own interface.
    func chatWindow(in id: Session.ID, chatID: String, takeover: Bool) async -> String? {
        var window: String?
        await perform {
            window = try await self.core.chatWindow(sessionID: id, chatID: chatID, takeover: takeover)
        }
        return window
    }

    func closeWindow(_ windowID: String, in id: Session.ID) async {
        await perform {
            try await self.core.closeWindow(sessionID: id, windowID: windowID)
            self.snapshots[id] = try await self.core.inspect(id)
        }
    }

    // MARK: Signing in

    func beginSignIn(_ request: SignInRequest) async -> DeviceAuthorization? {
        authorization = nil
        accounts = []
        roles = []
        await perform { self.authorization = try await self.core.beginSignIn(request) }
        return authorization
    }

    /// Waits for the browser approval. True when the account picked before
    /// still stands, and sign-in is done.
    func completeSignIn() async -> Bool {
        authorizing = true
        defer { authorizing = false }
        var done = false
        await perform {
            self.accounts = try await self.core.completeSignIn()
            self.status = try await self.core.status()
            done = self.status?.ready == true
        }
        if done {
            reconnecting = false
            authorization = nil
            accounts = []
            await refresh()
        }
        return done
    }

    func loadRoles(accountID: String) async {
        loadingRoles = true
        defer { loadingRoles = false }
        roles = []
        await perform { self.roles = try await self.core.roles(accountID: accountID) }
    }

    func finishSetup(accountID: String, roleName: String) async {
        await perform {
            try await self.core.finishSetup(accountID: accountID, roleName: roleName)
            self.status = try await self.core.status()
        }
        if status?.ready == true {
            reconnecting = false
            authorization = nil
            accounts = []
            roles = []
            await refresh()
        }
    }

    func signOut() async {
        await perform {
            try await self.core.signOut()
            self.status = try await self.core.status()
        }
        sessions = []
        snapshots = [:]
        loaded = false
        reconnecting = false
    }

    // MARK: Plumbing

    private func act(_ id: Session.ID, _ label: String, _ work: @escaping () async throws -> Void) async {
        guard busy[id] == nil else { return }
        busy[id] = label
        defer { busy[id] = nil }
        await perform(work)
    }

    private func perform(_ work: () async throws -> Void) async {
        do {
            try await work()
        } catch {
            let e = (error as? PierError) ?? PierError.from(error)
            if e.isSignInExpired {
                reconnecting = true
            } else {
                self.error = e
            }
        }
    }
}
