import SwiftUI

private enum QuickTab: String, CaseIterable, Identifiable {
    case shell = "Shell"
    case claude = "Claude"
    case codex = "Codex"

    var id: String { rawValue }
    var name: String { rawValue.lowercased() }

    var command: [String] {
        switch self {
        case .shell: ["bash", "-l"]
        case .claude: ["bash", "-lc", "exec claude"]
        case .codex: ["bash", "-lc", "exec codex"]
        }
    }

    var systemImage: String {
        switch self {
        case .shell: "terminal"
        case .claude: "sparkles"
        case .codex: "chevron.left.forwardslash.chevron.right"
        }
    }

    var detail: String {
        switch self {
        case .shell: "Login shell"
        case .claude: "Claude Code"
        case .codex: "Codex CLI"
        }
    }
}

struct NewTabView: View {
    @Environment(PierAppModel.self) private var model
    let instance: PierInstance
    let onCreated: (PierTab) -> Void

    var body: some View {
        VStack(spacing: 26) {
            VStack(spacing: 7) {
                Text("New Tab")
                    .font(.largeTitle.bold())
                Text("Choose what the new tmux window should run.")
                    .foregroundStyle(.secondary)
            }

            HStack(spacing: 16) {
                ForEach(QuickTab.allCases) { choice in
                    Button {
                        create(choice)
                    } label: {
                        VStack(spacing: 11) {
                            Image(systemName: choice.systemImage)
                                .font(.system(size: 30))
                            Text(choice.rawValue)
                                .font(.headline)
                            Text(choice.detail)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                        .frame(maxWidth: .infinity, minHeight: 128)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .background(Color.primary.opacity(0.055), in: RoundedRectangle(cornerRadius: 14))
                    .overlay {
                        RoundedRectangle(cornerRadius: 14)
                            .stroke(Color.secondary.opacity(0.2), lineWidth: 1)
                    }
                    .disabled(model.isLoading)
                }
            }
            .frame(maxWidth: 620)

            if model.isLoading {
                ProgressView("Creating tmux window…")
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(32)
    }

    private func create(_ choice: QuickTab) {
        Task {
            if let tab = await model.createTab(
                instanceID: instance.id,
                name: choice.name,
                command: choice.command
            ) {
                onCreated(tab)
            }
        }
    }
}
