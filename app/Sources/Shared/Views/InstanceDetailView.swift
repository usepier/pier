import SwiftUI
#if os(macOS)
import AppKit
#endif

enum InstanceWorkspaceTab: Hashable {
    case info
    case setup
    case terminal(PierTab.ID)
    case newTab
}

struct InstanceDetailView: View {
    @Environment(PierAppModel.self) private var model
    @Environment(\.scenePhase) private var scenePhase
    @State private var warmTabIDs: Set<PierTab.ID> = []
    let instance: PierInstance
    @Binding var selection: InstanceWorkspaceTab

    private var snapshot: PierInstanceSnapshot? {
        model.snapshots[instance.id]
    }

    private var tabs: [PierTab] {
        snapshot?.tabs ?? []
    }

    private var creation: PierCreationProgress? {
        model.creation(for: instance.id)
    }

    private var isSessionLoading: Bool {
        model.isInspectingInstance(instance.id) || model.isUnparkingInstance(instance.id)
    }

    var body: some View {
        VStack(spacing: 0) {
            #if !os(macOS)
            InstanceTabStrip(
                instance: instance,
                tabs: tabs,
                selection: $selection
            )

            Divider()
            #endif

            ZStack(alignment: .bottomTrailing) {
                ZStack {
                    InstanceInfoView(
                        instance: instance,
                        snapshot: snapshot,
                        reopenSetup: creation != nil && !model.isSetupTabVisible(for: instance.id) ? {
                            withAnimation(.easeInOut(duration: 0.18)) {
                                model.showSetupTab(for: instance.id)
                                selection = .setup
                            }
                        } : nil
                    )
                        .workspaceVisibility(selection == .info)

                    if let creation {
                        InstanceSetupView(instance: instance, progress: creation)
                            .workspaceVisibility(selection == .setup)
                    }

                    if selection == .newTab {
                        NewTabView(instance: instance) { tab in
                            warmTabIDs.insert(tab.id)
                            selection = .terminal(tab.id)
                        }
                        .workspaceVisibility(true)
                    }

                    ForEach(tabs.filter { warmTabIDs.contains($0.id) }) { tab in
                        TerminalTabView(instance: instance, tab: tab)
                            .workspaceVisibility(selection == .terminal(tab.id))
                    }
                }

                if isSessionLoading {
                    SessionLoadingIndicator(
                        message: model.isUnparkingInstance(instance.id)
                            ? "Unparking session…"
                            : (snapshot != nil ? "Refreshing session…" : "Loading session…")
                    )
                        .padding(12)
                        .transition(.opacity.combined(with: .move(edge: .bottom)))
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .animation(
                .easeInOut(duration: 0.18),
                value: isSessionLoading
            )
        }
        #if !os(macOS)
        .navigationTitle(instance.name)
        #endif
        #if !os(macOS)
        .toolbar {
            ToolbarItem(placement: .primaryAction) {
                if instance.state == .parked {
                    Button {
                        Task { await model.unpark(instance) }
                    } label: {
                        Label("Unpark", systemImage: "play.fill")
                    }
                    .disabled(model.isUnparkingInstance(instance.id))
                } else {
                    Button {
                        Task { await model.inspect(instance) }
                    } label: {
                        Label("Refresh", systemImage: "arrow.clockwise")
                    }
                    .disabled(instance.state == .creating)
                }
            }
        }
        #endif
        .task(id: instance.id) {
            selection = instance.state == .creating && model.isSetupTabVisible(for: instance.id) ? .setup : .info
            warmTabIDs.removeAll()
        }
        .task(id: InstanceSynchronizationID(instanceID: instance.id, scenePhase: scenePhase)) {
            guard scenePhase == .active else { return }
            if model.snapshots[instance.id] == nil {
                await model.inspect(instance)
            } else {
                await model.syncInstance(id: instance.id)
            }
            while !Task.isCancelled {
                do {
                    try await Task.sleep(for: PierSynchronization.interval)
                } catch {
                    return
                }
                await model.syncInstance(id: instance.id)
            }
        }
        .onChange(of: selection) { _, selected in
            if case .terminal(let tabID) = selected {
                warmTabIDs.insert(tabID)
            }
        }
        .onChange(of: tabs.map(\.id)) { _, availableTabIDs in
            warmTabIDs.formIntersection(availableTabIDs)
            if case .terminal(let selectedID) = selection,
               !availableTabIDs.contains(selectedID) {
                selection = .info
            }
        }
        .onChange(of: model.isSetupTabVisible(for: instance.id)) { _, isVisible in
            if !isVisible && selection == .setup {
                selection = .info
            }
        }
    }
}

private struct InstanceSynchronizationID: Equatable {
    let instanceID: PierInstance.ID
    let scenePhase: ScenePhase
}

private struct SessionLoadingIndicator: View {
    let message: String

    var body: some View {
        HStack(spacing: 7) {
            ProgressView()
                .controlSize(.small)

            Text(message)
                .font(.caption)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 7)
        .background(.regularMaterial, in: Capsule())
        .shadow(color: .black.opacity(0.1), radius: 4, y: 2)
        .allowsHitTesting(false)
        .accessibilityElement(children: .combine)
    }
}

struct InstanceTabStrip: View {
    @Environment(PierAppModel.self) private var model
    let instance: PierInstance
    let tabs: [PierTab]
    @Binding var selection: InstanceWorkspaceTab
    var integrated = false

    private var orderedTabs: [PierTab] {
        model.orderedTabs(tabs, for: instance.id)
    }

    var body: some View {
        ScrollView(.horizontal) {
            HStack(spacing: 4) {
                WorkspaceTabButton(
                    title: "Info",
                    systemImage: "info.circle",
                    isSelected: selection == .info
                ) {
                    selection = .info
                }

                if model.isSetupTabVisible(for: instance.id) {
                    WorkspaceTabButton(
                        title: "Setup",
                        systemImage: "gearshape.2",
                        isSelected: selection == .setup,
                        closeHelp: "Close setup tab",
                        close: {
                            withAnimation(.easeInOut(duration: 0.18)) {
                                model.hideSetupTab(for: instance.id)
                                if selection == .setup {
                                    selection = .info
                                }
                            }
                        }
                    ) {
                        selection = .setup
                    }
                }

                ForEach(orderedTabs) { tab in
                    let isClosing = model.isClosingTab(instanceID: instance.id, tabID: tab.id)
                    WorkspaceTabButton(
                        title: model.terminalTitle(instanceID: instance.id, tab: tab),
                        systemImage: terminalIcon(for: tab),
                        isSelected: selection == .terminal(tab.id),
                        isClosing: isClosing,
                        close: {
                            Task {
                                let closed = await model.closeTab(instanceID: instance.id, tabID: tab.id)
                                if closed, selection == .terminal(tab.id) {
                                    withAnimation(.easeInOut(duration: 0.18)) {
                                        selection = .info
                                    }
                                }
                            }
                        }
                    ) {
                        selection = .terminal(tab.id)
                    }
                    .draggable(tab.id)
                    .dropDestination(for: String.self) { draggedIDs, _ in
                        guard let draggedID = draggedIDs.first else { return false }
                        withAnimation(.snappy(duration: 0.2)) {
                            model.moveTab(
                                instanceID: instance.id,
                                tabID: draggedID,
                                to: tab.id,
                                tabs: tabs
                            )
                        }
                        return true
                    }
                }

                if selection == .newTab {
                    WorkspaceTabButton(
                        title: "New Tab",
                        systemImage: "plus.rectangle.on.rectangle",
                        isSelected: true,
                        close: { selection = .info }
                    ) {}
                }

                Button {
                    selection = .newTab
                } label: {
                    Image(systemName: "plus")
                        .frame(width: 28, height: 28)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .help("New tmux tab")
                .disabled(instance.state == .parked || instance.state == .creating)

                Spacer(minLength: 8)

                if instance.state == .parked {
                    Button {
                        Task { await model.unpark(instance) }
                    } label: {
                        Group {
                            if model.isUnparkingInstance(instance.id) {
                                ProgressView()
                                    .controlSize(.small)
                            } else {
                                Image(systemName: "play.fill")
                            }
                        }
                        .frame(width: 28, height: 28)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .help("Unpark instance")
                    .disabled(model.isUnparkingInstance(instance.id))
                } else {
                    #if os(macOS)
                    Button {
                        Task { await model.inspect(instance) }
                    } label: {
                        Image(systemName: "arrow.clockwise")
                            .frame(width: 28, height: 28)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .help("Refresh instance")
                    .disabled(instance.state == .creating)
                    #endif
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, integrated ? 6 : 7)
            .animation(.snappy(duration: 0.24), value: orderedTabs.map(\.id))
        }
        .scrollIndicators(.hidden)
        .background(PierTheme.surfaceSoft)
    }

    private func terminalIcon(for tab: PierTab) -> String {
        switch tab.name.lowercased() {
        case "claude": "sparkles"
        case "codex": "chevron.left.forwardslash.chevron.right"
        default: "terminal"
        }
    }
}

private extension View {
    func workspaceVisibility(_ isVisible: Bool) -> some View {
        opacity(isVisible ? 1 : 0)
            .allowsHitTesting(isVisible)
            .accessibilityHidden(!isVisible)
            .zIndex(isVisible ? 1 : 0)
    }
}

private struct WorkspaceTabButton: View {
    let title: String
    let systemImage: String
    let isSelected: Bool
    var isClosing = false
    var closeHelp = "Close tmux tab"
    var close: (() -> Void)?
    let select: () -> Void

    var body: some View {
        HStack(spacing: 7) {
            Button(action: select) {
                HStack(spacing: 6) {
                    Image(systemName: systemImage)
                    Text(title)
                        .lineLimit(1)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(isClosing)

            if let close {
                Group {
                    if isClosing {
                        ProgressView()
                            .controlSize(.mini)
                            .accessibilityLabel("Closing \(title)")
                    } else {
                        Button(action: close) {
                            Image(systemName: "xmark")
                                .font(.caption2.weight(.semibold))
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .foregroundStyle(.secondary)
                        .help(closeHelp)
                    }
                }
                .frame(width: 16, height: 16)
                .transition(.opacity.combined(with: .scale(scale: 0.8)))
            }
        }
        .padding(.leading, 10)
        .padding(.trailing, close == nil ? 10 : 6)
        .frame(height: 30)
        .background(
            isSelected ? AnyShapeStyle(Color.primary.opacity(0.09)) : AnyShapeStyle(Color.clear),
            in: RoundedRectangle(cornerRadius: 7)
        )
        .overlay {
            if isSelected {
                RoundedRectangle(cornerRadius: 7)
                    .stroke(Color.secondary.opacity(0.3), lineWidth: 1)
            }
        }
        .opacity(isClosing ? 0.68 : 1)
        .animation(.easeInOut(duration: 0.15), value: isClosing)
        .transition(.opacity.combined(with: .scale(scale: 0.94, anchor: .leading)))
    }
}

private struct InstanceSetupView: View {
    @Environment(PierAppModel.self) private var model
    let instance: PierInstance
    let progress: PierCreationProgress

    private var failedMessage: String? {
        guard case .failed(let message) = progress.status else { return nil }
        return message
    }

    var body: some View {
        List {
            Section("Session") {
                LabeledContent("Project", value: progress.project.name)
                LabeledContent("Name", value: progress.name)
                LabeledContent("Base branch", value: progress.baseBranch)
            }

            Section("Setup") {
                ForEach(Array(progress.steps.enumerated()), id: \.element.id) { index, step in
                    HStack(alignment: .top, spacing: 10) {
                        stepIndicator(index: index)
                            .frame(width: 16, height: 18)

                        Text(step.message)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .padding(.vertical, 2)
                }
            }

            if let setup = progress.customSetup {
                Section("Custom setup") {
                    HStack(spacing: 10) {
                        customSetupIndicator(setup.status)
                            .frame(width: 16, height: 18)

                        Text(setup.name)
                            .fontWeight(.medium)

                        Spacer()

                        Text(customSetupLabel(setup.status))
                            .foregroundStyle(customSetupColor(setup.status))
                    }

                    if !setup.output.isEmpty {
                        ScrollView([.horizontal, .vertical]) {
                            Text(setup.output.joined(separator: "\n"))
                                .font(.system(.caption, design: .monospaced))
                                .textSelection(.enabled)
                                .frame(maxWidth: .infinity, alignment: .topLeading)
                                .padding(10)
                        }
                        .frame(minHeight: 80, maxHeight: 260)
                        .background(.black.opacity(0.82), in: RoundedRectangle(cornerRadius: 7))
                        .foregroundStyle(.white.opacity(0.9))
                    }
                }
            }

            if let failedMessage {
                Section {
                    Text(failedMessage)
                        .foregroundStyle(.red)
                        .textSelection(.enabled)

                    Button("Remove from list", role: .destructive) {
                        model.removeFailedCreation(instanceID: instance.id)
                    }
                } header: {
                    Text("Creation failed")
                }
            }
        }
        .pierScrollSurface()
    }

    @ViewBuilder
    private func stepIndicator(index: Int) -> some View {
        let isLast = index == progress.steps.indices.last
        switch progress.status {
        case .provisioning where isLast:
            ProgressView()
                .controlSize(.small)
        case .failed where isLast:
            Image(systemName: "exclamationmark.circle.fill")
                .foregroundStyle(.red)
        case .completed, .provisioning, .failed:
            Image(systemName: "checkmark.circle.fill")
                .foregroundStyle(.green)
        }
    }

    @ViewBuilder
    private func customSetupIndicator(_ status: PierCustomSetupStatus) -> some View {
        switch status {
        case .running:
            ProgressView()
                .controlSize(.small)
        case .completed:
            Image(systemName: "checkmark.circle.fill")
                .foregroundStyle(.green)
        case .failed:
            Image(systemName: "exclamationmark.circle.fill")
                .foregroundStyle(.red)
        }
    }

    private func customSetupLabel(_ status: PierCustomSetupStatus) -> String {
        switch status {
        case .running: "Running"
        case .completed: "Completed"
        case .failed(let exitCode): "Failed (exit \(exitCode))"
        }
    }

    private func customSetupColor(_ status: PierCustomSetupStatus) -> Color {
        switch status {
        case .running: .secondary
        case .completed: .green
        case .failed: .red
        }
    }
}

private struct InstanceInfoView: View {
    @Environment(PierAppModel.self) private var model
    @Environment(\.openURL) private var openURL
    @State private var copiedPort: Int?
    let instance: PierInstance
    let snapshot: PierInstanceSnapshot?
    let reopenSetup: (() -> Void)?

    var body: some View {
        List {
            Section("Instance") {
                LabeledContent("State", value: instance.state.label)
                LabeledContent("Repository", value: instance.repo)
                LabeledContent("Branch", value: instance.branch)
                LabeledContent("Machine", value: instance.instanceType)
                LabeledContent("Cost", value: instance.costNote)
            }

            if instance.state == .parked {
                Section {
                    Button {
                        Task { await model.unpark(instance) }
                    } label: {
                        if model.isUnparkingInstance(instance.id) {
                            HStack(spacing: 8) {
                                ProgressView()
                                    .controlSize(.small)
                                Text("Unparking…")
                            }
                        } else {
                            Label("Unpark Instance", systemImage: "play.fill")
                        }
                    }
                    .disabled(model.isUnparkingInstance(instance.id))
                } footer: {
                    Text("Starts the AWS instance and keeps its disk and session state intact.")
                }
            }

            if let reopenSetup {
                Section {
                    Button("Show Setup", systemImage: "gearshape.2", action: reopenSetup)
                }
            }

            Section("Ports") {
                if let ports = snapshot?.ports, !ports.isEmpty {
                    ForEach(ports) { port in
                        HStack(spacing: 10) {
                            Image(systemName: port.isHTTP ? "globe" : "network")
                                .foregroundStyle(port.isHTTP ? Color.accentColor : Color.secondary)
                                .frame(width: 16)

                            VStack(alignment: .leading, spacing: 2) {
                                Text("Port " + String(port.number))
                                if let process = port.process, !process.isEmpty {
                                    Text(process)
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                            }

                            Spacer()

                            if model.isOpeningPort(instanceID: instance.id, port: port.number) {
                                ProgressView()
                                    .controlSize(.small)
                                    .frame(width: 24, height: 24)
                            } else {
                                Button {
                                    Task { await activate(port) }
                                } label: {
                                    Image(systemName: portActionIcon(port))
                                        .frame(width: 24, height: 24)
                                        .contentShape(Rectangle())
                                }
                                .buttonStyle(.plain)
                                .help(portActionHelp(port))
                            }
                        }
                    }
                } else {
                    Text("No listening ports.").foregroundStyle(.secondary)
                }
            }

        }
        .pierScrollSurface()
    }

    private func activate(_ port: PierPort) async {
        guard let endpoint = await model.openPort(instanceID: instance.id, port: port) else { return }
        if let url = endpoint.browserURL {
            openURL(url)
            return
        }
        #if os(macOS)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(endpoint.address, forType: .string)
        #endif
        copiedPort = port.number
        try? await Task.sleep(for: .seconds(1.5))
        if copiedPort == port.number {
            copiedPort = nil
        }
    }

    private func portActionIcon(_ port: PierPort) -> String {
        if port.isHTTP { return "arrow.up.forward.app" }
        return copiedPort == port.number ? "checkmark" : "doc.on.doc"
    }

    private func portActionHelp(_ port: PierPort) -> String {
        if port.isHTTP {
            return "Open Port \(port.number) through its local Pier tunnel"
        }
        if copiedPort == port.number { return "Tunnel address copied" }
        let host = snapshot?.proxyHost ?? "\(instance.name).pier"
        return "Activate the tunnel and copy \(host):\(port.number)"
    }
}

private struct TerminalTabView: View {
    @Environment(PierAppModel.self) private var model
    let instance: PierInstance
    let tab: PierTab

    var body: some View {
        #if os(macOS)
        PierTerminalSessionView(
            instanceID: instance.id,
            tabID: tab.id,
            onTitleChange: { title in
                model.updateTerminalTitle(instanceID: instance.id, tabID: tab.id, title: title)
            }
        )
            .id("\(instance.id):\(tab.id)")
            .ignoresSafeArea(.container, edges: .bottom)
        #else
        PierMobileTerminalView(
            instanceID: instance.id,
            tabID: tab.id,
            onTitleChange: { title in
                model.updateTerminalTitle(instanceID: instance.id, tabID: tab.id, title: title)
            }
        )
        .id("\(instance.id):\(tab.id)")
        .ignoresSafeArea(.container, edges: .bottom)
        #endif
    }
}
