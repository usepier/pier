#if DEBUG
import Foundation

/// Made-up sessions for working on the app without AWS (PIER_IOS_DEMO=1):
/// signed in, a few sessions in two repos, and terminals that print a
/// sample through the real renderer.
actor DemoBackend: Backend {
    private var ready = true
    private var list: [Session] = [
        Session(id: "i-01", name: "checkout-flow", repo: "shop", origin: "github.com/acme/shop", base: "main",
                branch: "checkout-flow", state: "working", machineType: "t4g.large"),
        Session(id: "i-02", name: "fix-login", repo: "shop", origin: "github.com/acme/shop", base: "main",
                branch: "fix-login", state: "idle", machineType: "t4g.medium"),
        Session(id: "i-03", name: "search-v2", repo: "shop", base: "main",
                branch: "search-v2", state: "parked", machineType: "t4g.medium"),
        Session(id: "i-04", name: "api-rate-limits", repo: "api", origin: "github.com/acme/api", base: "main",
                branch: "api-rate-limits", state: "creating", machineType: "t4g.medium"),
    ]
    private var windows: [String: [TmuxWindow]] = [:]

    func status() -> SignInStatus {
        SignInStatus(signedIn: ready, ready: ready, accountID: "123456789012", roleName: "Developer", region: "eu-central-1")
    }

    func beginSignIn(_ request: SignInRequest) -> DeviceAuthorization {
        DeviceAuthorization(verificationURL: "https://example.com", userCode: "DEMO-CODE", expiresAt: .now + 600)
    }

    func completeSignIn() -> [AWSAccount] {
        ready = true
        return []
    }

    func roles(accountID: String) -> [AWSRole] { [AWSRole(name: "Developer")] }
    func finishSetup(accountID: String, roleName: String) { ready = true }
    func signOut() { ready = false }
    func sessions() -> [Session] { list }

    func inspect(_ id: String) -> SessionSnapshot {
        let session = list.first { $0.id == id }!
        let base = [
            TmuxWindow(id: "@1", index: 0, active: false, command: "zsh", role: "home", name: "zsh"),
            TmuxWindow(id: "@2", index: 1, active: true, command: "node", role: nil, name: "server"),
        ]
        let chats = id == "i-01" ? [
            ChatSummary(id: "c1", agent: "claude", title: "Pay with saved cards", state: "working",
                        last: "Adding the saved-card picker to PayButton…"),
            ChatSummary(id: "c2", agent: "codex", title: "Tests for charges", state: "waiting",
                        waiting: "permission", ask: "Run `pnpm test src/api`?"),
        ] : []
        return SessionSnapshot(
            session: session, windows: base + (windows[id] ?? []),
            ports: [ListeningPort(port: 3000, process: "node"), ListeningPort(port: 5432, process: "postgres")],
            chats: chats, strained: false, cpu: 38, memory: 41, parkAt: id == "i-02" ? .now + 1500 : nil
        )
    }

    func wake(_ id: String) { set(id, state: "running") }
    func park(_ id: String) { set(id, state: "parked") }
    func remove(_ id: String) { list.removeAll { $0.id == id } }

    func newWindow(sessionID: String, name: String, command: [String]) -> TmuxWindow {
        let n = 3 + (windows[sessionID]?.count ?? 0)
        let window = TmuxWindow(id: "@\(n)", index: n, active: true, command: name.isEmpty ? "zsh" : name, role: nil,
                                name: name.isEmpty ? "zsh" : name)
        windows[sessionID, default: []].append(window)
        return window
    }

    func chatWindow(sessionID: String, chatID: String, takeover: Bool) -> String { "@9" }

    func closeWindow(sessionID: String, windowID: String) {
        windows[sessionID]?.removeAll { $0.id == windowID }
    }

    func openTerminal(sessionID: String, windowID: String, columns: Int, rows: Int, listener: TerminalListener) -> any Terminal {
        DemoTerminal(listener: listener)
    }

    private func set(_ id: String, state: String) {
        if let i = list.firstIndex(where: { $0.id == id }) { list[i].state = state }
    }
}

/// Prints a sample, then echoes what's typed.
private final class DemoTerminal: Terminal, @unchecked Sendable {
    private let listener: TerminalListener

    init(listener: TerminalListener) {
        self.listener = listener
        let sample = [
            "\u{1b}[1;32magent@checkout-flow\u{1b}[0m:\u{1b}[1;34m~/work/shop\u{1b}[0m$ git log --oneline -3",
            "\u{1b}[33m3f2a91c\u{1b}[0m feat(checkout): pay with a saved card",
            "\u{1b}[33m8d01b7e\u{1b}[0m test(api): charges round to the cent",
            "\u{1b}[33mc44e2d0\u{1b}[0m fix(login): keep the session on refresh",
            "\u{1b}[1;32magent@checkout-flow\u{1b}[0m:\u{1b}[1;34m~/work/shop\u{1b}[0m$ pnpm test",
            " \u{1b}[32m✓\u{1b}[0m src/api/charges.test.ts \u{1b}[2m(12 tests)\u{1b}[0m",
            " \u{1b}[32m✓\u{1b}[0m src/checkout/PayButton.test.tsx \u{1b}[2m(7 tests)\u{1b}[0m",
            "",
            " \u{1b}[1mTest Files\u{1b}[0m  \u{1b}[1;32m2 passed\u{1b}[0m (2)",
            "      \u{1b}[1mTests\u{1b}[0m  \u{1b}[1;32m19 passed\u{1b}[0m (19)",
            "\u{1b}[1;32magent@checkout-flow\u{1b}[0m:\u{1b}[1;34m~/work/shop\u{1b}[0m$ ",
        ].joined(separator: "\r\n")
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.3) { listener.output(Data(sample.utf8)) }
    }

    func write(_ data: Data) throws {
        listener.output(Data(String(decoding: data, as: UTF8.self).replacingOccurrences(of: "\r", with: "\r\n").utf8))
    }

    func resize(columns: Int, rows: Int) throws {}
    func close() {}
}
#endif
