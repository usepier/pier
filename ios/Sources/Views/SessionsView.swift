import SwiftUI

/// The user's sessions, by repo, as the Mac's sidebar lists them.
struct SessionsView: View {
    @Environment(AppModel.self) private var model
    @State private var showsSettings = false
    @State private var removing: Session?

    var body: some View {
        List {
            if model.loaded && model.sessions.isEmpty {
                ContentUnavailableView(
                    "No sessions",
                    systemImage: "shippingbox",
                    description: Text("Sessions you start with pier on your Mac show here.")
                )
                .listRowBackground(Color.clear)
            }
            ForEach(model.byRepo, id: \.repo) { group in
                Section(group.repo.isEmpty ? "Other" : group.repo) {
                    ForEach(group.sessions) { session in
                        NavigationLink(value: session.id) {
                            SessionRow(session: session, busy: model.busy[session.id])
                        }
                        .swipeActions(edge: .trailing) {
                            Button("Remove", systemImage: "trash", role: .destructive) { removing = session }
                        }
                        .swipeActions(edge: .leading) {
                            if session.isParked {
                                Button("Wake", systemImage: "power") { Task { await model.wake(session.id) } }
                                    .tint(PierTheme.accent)
                            } else if session.isUp {
                                Button("Park", systemImage: "moon.zzz") { Task { await model.park(session.id) } }
                            }
                        }
                    }
                }
            }
        }
        .pierScrollSurface()
        .overlay {
            if !model.loaded { ProgressView() }
        }
        .refreshable { await model.refresh() }
        .navigationTitle("Sessions")
        .navigationDestination(for: Session.ID.self) { SessionView(id: $0) }
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                Button { showsSettings = true } label: { Label("Settings", systemImage: "gearshape") }
            }
        }
        .sheet(isPresented: $showsSettings) {
            NavigationStack { SettingsView() }
        }
        .confirmationDialog(
            "Remove \(removing?.name ?? "")?",
            isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
            titleVisibility: .visible,
            presenting: removing
        ) { session in
            Button("Remove", role: .destructive) { Task { await model.remove(session.id) } }
        } message: { _ in
            Text("Its machine and disk go, and whatever isn't pushed goes with them.")
        }
    }
}

struct SessionRow: View {
    let session: Session
    var busy: String?

    var body: some View {
        HStack(spacing: 12) {
            StateDot(state: session.state)
            VStack(alignment: .leading, spacing: 2) {
                Text(session.name).font(.body.weight(.medium)).lineLimit(1)
                Text(busy.map { "\($0)…" } ?? detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 0)
            if busy != nil { ProgressView().controlSize(.small) }
        }
        .padding(.vertical, 2)
    }

    private var detail: String {
        session.branch == session.name ? session.stateLabel : "\(session.stateLabel) · \(session.branch)"
    }
}

/// A session's state at a glance, as the Mac shows it: filled while it runs,
/// in the accent while it works, hollow when parked.
struct StateDot: View {
    let state: String

    var body: some View {
        Circle()
            .fill(fill)
            .overlay { Circle().strokeBorder(PierTheme.muted, lineWidth: state == "parked" ? 1.5 : 0) }
            .frame(width: 9, height: 9)
            .accessibilityLabel(state)
    }

    private var fill: Color {
        switch state {
        case "parked": .clear
        case "creating": .orange
        case "working": PierTheme.accent
        default: .green
        }
    }
}
