import SwiftUI

/// One session: what it is, what its agents are doing, its terminals and
/// ports — and waking, parking or removing it.
struct SessionView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    let id: Session.ID

    @State private var terminal: OpenTerminal?
    @State private var opening = false
    @State private var confirmsRemove = false

    private var session: Session? { model.session(id) }
    private var snapshot: SessionSnapshot? { model.snapshots[id] }

    var body: some View {
        List {
            if let session {
                Section { header(session) }
                if session.isUp {
                    if let snapshot {
                        if !snapshot.chats.isEmpty { chats(snapshot.chats) }
                        windows(snapshot.windows.filter { $0.role != "home" || $0.command != "zsh" && $0.command != "bash" })
                        if !snapshot.ports.isEmpty { ports(snapshot.ports) }
                    } else {
                        Section {
                            HStack { ProgressView(); Text("Reaching the session…").foregroundStyle(.secondary) }
                        } footer: {
                            if let why = model.unreachable[id] { Text(why) }
                        }
                    }
                }
            }
        }
        .pierScrollSurface()
        .navigationTitle(session?.name ?? "")
        .navigationBarTitleDisplayMode(.inline)
        .refreshable { await model.inspect(id) }
        .task(id: session?.state) {
            // A running session is looked at every few seconds while shown.
            while !Task.isCancelled, session?.isUp == true {
                await model.inspect(id)
                try? await Task.sleep(for: .seconds(5))
            }
        }
        .toolbar { toolbar }
        .overlay { if opening { ProgressView().controlSize(.large) } }
        .fullScreenCover(item: $terminal) { TerminalScreen(terminal: $0) }
        .confirmationDialog("Remove \(session?.name ?? "")?", isPresented: $confirmsRemove, titleVisibility: .visible) {
            Button("Remove", role: .destructive) {
                Task {
                    await model.remove(id)
                    dismiss()
                }
            }
        } message: {
            Text("Its machine and disk go, and whatever isn't pushed goes with them.")
        }
    }

    @ToolbarContentBuilder
    private var toolbar: some ToolbarContent {
        ToolbarItem(placement: .primaryAction) {
            Menu {
                if let session, session.isUp {
                    Section("New terminal") {
                        ForEach(Launch.allCases) { launch in
                            Button(launch.title, systemImage: launch.icon) { open(launch) }
                        }
                    }
                    Button("Park", systemImage: "moon.zzz") { Task { await model.park(id) } }
                }
                Button("Remove…", systemImage: "trash", role: .destructive) { confirmsRemove = true }
            } label: {
                Label("Actions", systemImage: "ellipsis.circle")
            }
        }
    }

    // MARK: Sections

    @ViewBuilder
    private func header(_ session: Session) -> some View {
        HStack(spacing: 10) {
            StateDot(state: session.state)
            Text(model.busy[id].map { "\($0)…" } ?? session.stateLabel.capitalized)
                .font(.headline)
            Spacer()
            if session.isParked {
                Button("Wake") { Task { await model.wake(id) } }
                    .buttonStyle(.borderedProminent)
                    .disabled(model.busy[id] != nil)
            }
        }
        LabeledContent("Branch", value: session.branch)
        if let base = session.base, !base.isEmpty { LabeledContent("Off", value: base) }
        LabeledContent("Machine", value: session.machineType)
        if let snapshot, session.isUp {
            LabeledContent("Load", value: "\(Int(snapshot.cpu.rounded()))% CPU · \(Int(snapshot.memory.rounded()))% memory")
            if snapshot.strained {
                Label("Short of CPU or memory — a bigger machine would help (resize it on your Mac).", systemImage: "gauge.with.dots.needle.67percent")
                    .font(.footnote)
                    .foregroundStyle(.orange)
            }
            if snapshot.setup == "failed" {
                Label("Its setup script failed: the setup window says why.", systemImage: "exclamationmark.triangle")
                    .font(.footnote)
                    .foregroundStyle(.red)
            }
            if let parkAt = snapshot.parkAt {
                LabeledContent("Parks") { Text(parkAt, style: .relative) }
            }
        }
    }

    private func chats(_ chats: [ChatSummary]) -> some View {
        Section {
            ForEach(chats) { chat in
                Button { openChat(chat.id, takeover: false) } label: { ChatRow(chat: chat).rowShape() }
                    .buttonStyle(.plain)
                    .contextMenu {
                        Button("Watch", systemImage: "eye") { openChat(chat.id, takeover: false) }
                        Button("Continue in \(chat.agentName)", systemImage: "terminal") { openChat(chat.id, takeover: true) }
                    }
                    .swipeActions(edge: .leading) {
                        Button("Continue", systemImage: "terminal") { openChat(chat.id, takeover: true) }
                            .tint(PierTheme.accent)
                    }
            }
        } header: {
            Text("Chats")
        } footer: {
            Text("Tap to watch a chat live; swipe to carry it on in \(chats.count == 1 ? chats[0].agentName : "its agent")'s own interface.")
        }
    }

    private func windows(_ windows: [TmuxWindow]) -> some View {
        Section("Terminals") {
            ForEach(windows) { window in
                Button { terminal = OpenTerminal(sessionID: id, windowID: window.id, title: window.title) } label: {
                    HStack {
                        Image(systemName: icon(for: window)).foregroundStyle(.secondary).frame(width: 22)
                        VStack(alignment: .leading) {
                            Text(window.title)
                            if window.command != window.title {
                                Text(window.command).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                    }
                    .rowShape()
                }
                .buttonStyle(.plain)
                .swipeActions {
                    Button("Close", systemImage: "xmark", role: .destructive) {
                        Task { await model.closeWindow(window.id, in: id) }
                    }
                }
            }
            Menu {
                ForEach(Launch.allCases) { launch in
                    Button(launch.title, systemImage: launch.icon) { open(launch) }
                }
            } label: {
                Label("New Terminal", systemImage: "plus")
            }
        }
    }

    private func ports(_ ports: [ListeningPort]) -> some View {
        Section {
            ForEach(ports) { port in
                LabeledContent {
                    Text(port.process)
                } label: {
                    Text(String(port.port)).monospacedDigit()
                }
            }
        } header: {
            Text("Listening")
        } footer: {
            Text("Open them from your Mac, where pier's proxy serves them.")
        }
    }

    // MARK: Opening terminals

    private func open(_ launch: Launch) {
        Task {
            opening = true
            defer { opening = false }
            if let window = await model.newWindow(in: id, name: launch.windowName, command: launch.command) {
                terminal = OpenTerminal(sessionID: id, windowID: window.id, title: window.title)
            }
        }
    }

    private func openChat(_ chatID: String, takeover: Bool) {
        Task {
            opening = true
            defer { opening = false }
            if let window = await model.chatWindow(in: id, chatID: chatID, takeover: takeover) {
                terminal = OpenTerminal(sessionID: id, windowID: window, title: takeover ? "Chat" : "Chats")
            }
        }
    }

    private func icon(for window: TmuxWindow) -> String {
        switch window.role {
        case "agent": "sparkles"
        case "setup": "wrench.and.screwdriver"
        default: window.command.contains("claude") || window.command.contains("codex") ? "sparkles" : "terminal"
        }
    }
}

/// What a new terminal runs.
enum Launch: String, CaseIterable, Identifiable {
    case shell, claude, codex

    var id: String { rawValue }
    var title: String {
        switch self {
        case .shell: "Shell"
        case .claude: "Claude Code"
        case .codex: "Codex"
        }
    }
    var icon: String {
        switch self {
        case .shell: "terminal"
        case .claude: "sparkles"
        case .codex: "chevron.left.forwardslash.chevron.right"
        }
    }
    var windowName: String { self == .shell ? "" : rawValue }
    var command: [String] {
        switch self {
        case .shell: []
        case .claude: ["bash", "-lc", "exec claude"]
        case .codex: ["bash", "-lc", "exec codex"]
        }
    }
}

struct ChatRow: View {
    let chat: ChatSummary

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Image(systemName: "sparkles").foregroundStyle(PierTheme.accent).font(.caption)
                Text(chat.title.isEmpty ? chat.agentName : chat.title)
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                Spacer()
                Text(state).font(.caption.weight(.medium)).foregroundStyle(stateColor)
            }
            if let line = chat.ask ?? chat.last, !line.isEmpty {
                Text(line).font(.caption).foregroundStyle(.secondary).lineLimit(2)
            }
        }
    }

    private var state: String {
        if let waiting = chat.waiting, !waiting.isEmpty {
            switch waiting {
            case "permission": return "needs permission"
            case "question": return "has a question"
            case "plan": return "has a plan"
            default: return "waiting"
            }
        }
        return chat.state
    }

    private var stateColor: Color {
        chat.waiting?.isEmpty == false ? .orange : .secondary
    }
}

struct OpenTerminal: Identifiable, Hashable {
    let sessionID: String
    let windowID: String
    let title: String
    var id: String { sessionID + windowID }
}

private extension View {
    /// The whole row takes the tap, not just its text.
    func rowShape() -> some View {
        frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
    }
}
