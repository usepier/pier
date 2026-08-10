import SwiftUI
#if os(macOS)
import AppKit
#endif

struct PierContentView: View {
    @Environment(PierAppModel.self) private var model
    @State private var workspaceSelection = InstanceWorkspaceTab.info

    var body: some View {
        #if os(macOS)
        @Bindable var model = model
        HSplitView {
                VStack(spacing: 0) {
                    MacSidebarTitlebar()
                    Divider()
                    InstanceListView(selection: $model.selectedInstanceID)
                }
                .frame(minWidth: 240, idealWidth: 300, maxWidth: 380)
                .background(MacSidebarVisualEffect())

                Group {
                    if let instance = model.selectedInstance {
                        VStack(spacing: 0) {
                            InstanceTabStrip(
                                instance: instance,
                                tabs: model.snapshots[instance.id]?.tabs ?? [],
                                selection: $workspaceSelection,
                                integrated: true
                            )
                            .frame(height: 43)

                            Divider()

                            InstanceDetailView(
                                instance: instance,
                                selection: $workspaceSelection
                            )
                        }
                        .id(instance.id)
                    } else {
                        VStack(spacing: 0) {
                            Color.clear.frame(height: 43)
                            Divider()
                            ContentUnavailableView("Select an instance", systemImage: "server.rack")
                        }
                    }
                }
                .frame(minWidth: 520, maxWidth: .infinity, maxHeight: .infinity)
        }
        .ignoresSafeArea(.container, edges: .top)
        .onChange(of: model.selectedInstanceID) { _, _ in
            workspaceSelection = .info
        }
        #else
        NavigationStack {
            InstanceListView(selection: .constant(nil))
                .navigationDestination(for: PierInstance.self) { destination in
                    if let instance = model.instances.first(where: { $0.id == destination.id }) {
                        InstanceDetailView(instance: instance, selection: $workspaceSelection)
                    } else {
                        ContentUnavailableView("Instance unavailable", systemImage: "server.rack")
                    }
                }
        }
        #endif
    }
}

#if os(macOS)
private struct MacSidebarTitlebar: View {
    @Environment(PierAppModel.self) private var model
    @State private var showsNewProject = false

    var body: some View {
        HStack(spacing: 10) {
            Spacer(minLength: 76)

            Button {
                Task {
                    await model.refreshInstances()
                    await model.loadProjects()
                }
            } label: {
                Image(systemName: "arrow.clockwise")
                    .frame(width: 24, height: 24)
            }
            .buttonStyle(.plain)
            .help("Refresh instances")

            Button {
                showsNewProject = true
            } label: {
                Image(systemName: "folder.badge.plus")
                    .frame(width: 24, height: 24)
            }
            .buttonStyle(.plain)
            .help("New project")
            .disabled(model.isLoadingProjects)
        }
        .foregroundStyle(.secondary)
        .padding(.leading, 10)
        .padding(.trailing, 8)
        .frame(height: 43)
        .sheet(isPresented: $showsNewProject) {
            NewProjectView()
        }
    }
}

private struct MacSidebarVisualEffect: NSViewRepresentable {
    func makeNSView(context: Context) -> NSVisualEffectView {
        let view = NSVisualEffectView()
        view.material = .sidebar
        view.blendingMode = .behindWindow
        view.state = .active
        view.isEmphasized = false
        return view
    }

    func updateNSView(_ nsView: NSVisualEffectView, context: Context) {
        nsView.material = .sidebar
        nsView.blendingMode = .behindWindow
        nsView.state = .active
    }
}
#endif

private struct SidebarProjectGroup: Identifiable {
    let project: PierProject
    let instances: [PierInstance]

    var id: PierProject.ID { project.id }
}

private struct InstanceListView: View {
    @Environment(PierAppModel.self) private var model
    @Binding var selection: PierInstance.ID?
    @State private var instancePendingRemoval: PierInstance?
    @State private var projectForNewSession: PierProject?
    @State private var showsSignOutConfirmation = false
    @State private var showsSettings = false

    private var projectGroups: [SidebarProjectGroup] {
        var projectsByID = Dictionary(uniqueKeysWithValues: model.projects.map { ($0.id, $0) })
        var instancesByProject: [String: [PierInstance]] = [:]

        for instance in model.instances {
            let projectID: String
            if let localID = instance.projectID, !localID.isEmpty {
                projectID = localID
                if projectsByID[localID] == nil {
                    projectsByID[localID] = PierProject(
                        id: localID,
                        name: instance.projectName,
                        path: instance.displayLocalPath,
                        host: "AWS"
                    )
                }
            } else if let registered = model.projects.first(where: { $0.name == instance.projectName }) {
                projectID = registered.id
            } else {
                projectID = "remote:\(instance.projectName)"
                projectsByID[projectID] = PierProject(
                    id: projectID,
                    name: instance.projectName,
                    path: instance.displayLocalPath,
                    host: "AWS"
                )
            }
            instancesByProject[projectID, default: []].append(instance)
        }

        return projectsByID.values
            .map { project in
                SidebarProjectGroup(
                    project: project,
                    instances: (instancesByProject[project.id] ?? []).sorted {
                        $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending
                    }
                )
            }
            .sorted { $0.project.name.localizedCaseInsensitiveCompare($1.project.name) == .orderedAscending }
    }

    var body: some View {
        List(selection: $selection) {
            if projectGroups.isEmpty && !model.isLoading && !model.isLoadingProjects {
                #if os(macOS)
                ContentUnavailableView(
                    "No Pier projects",
                    systemImage: "folder.badge.plus",
                    description: Text("Add a Git repository with the + button to get started.")
                )
                #else
                ContentUnavailableView(
                    "No Pier projects",
                    systemImage: "folder",
                    description: Text("Create a session from Pier on your Mac to make its project available here.")
                )
                #endif
            } else {
                ForEach(projectGroups) { group in
                    Section {
                        if group.instances.isEmpty {
                            Text("No sessions")
                                .font(.caption)
                                .foregroundStyle(.tertiary)
                                .padding(.vertical, 2)
                        }

                        ForEach(group.instances) { instance in
                            #if os(macOS)
                            InstanceRow(
                                instance: instance,
                                isRemoving: model.isRemovingInstance(instance.id),
                                isLoading: model.isInspectingInstance(instance.id) ||
                                    model.isUnparkingInstance(instance.id)
                            )
                                .tag(instance.id)
                                .contextMenu {
                                    if isFailedCreation(instance) {
                                        Button("Remove from List", role: .destructive) {
                                            model.removeFailedCreation(instanceID: instance.id)
                                        }
                                    } else {
                                        if instance.state == .parked {
                                            Button {
                                                Task { await model.unpark(instance) }
                                            } label: {
                                                Label("Unpark Instance", systemImage: "play.fill")
                                            }
                                            .disabled(model.isUnparkingInstance(instance.id))
                                        }

                                        Button(role: .destructive) {
                                            instancePendingRemoval = instance
                                        } label: {
                                            Label("Remove Instance…", systemImage: "trash")
                                        }
                                        .disabled(
                                            instance.state == .creating ||
                                                model.isRemovingInstance(instance.id)
                                        )
                                    }
                                }
                            #else
                            NavigationLink(value: instance) {
                                InstanceRow(
                                    instance: instance,
                                    isLoading: model.isInspectingInstance(instance.id) ||
                                        model.isUnparkingInstance(instance.id)
                                )
                            }
                            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                                if instance.state == .parked {
                                    Button {
                                        Task { await model.unpark(instance) }
                                    } label: {
                                        Label("Unpark", systemImage: "play.fill")
                                    }
                                    .tint(.accentColor)
                                    .disabled(model.isUnparkingInstance(instance.id))
                                }
                            }
                            #endif
                        }
                    } header: {
                        ProjectSectionHeader(project: group.project) {
                            projectForNewSession = group.project
                        }
                    }
                    #if os(macOS)
                    .collapsible(false)
                    #endif
                }
            }
        }
        #if os(macOS)
        .scrollContentBackground(.hidden)
        .background(Color.clear)
        #else
        .pierScrollSurface()
        #endif
        #if !os(macOS)
        .navigationTitle("Instances")
        .refreshable {
            await model.refreshInstances()
            await model.loadProjects()
        }
        #endif
        .overlay {
            if (model.isLoading || model.isLoadingProjects) && projectGroups.isEmpty {
                ProgressView()
            }
        }
        #if !os(macOS)
        .toolbar {
            ToolbarItemGroup {
                Button {
                    showsSettings = true
                } label: {
                    Label("Settings", systemImage: "gearshape")
                }

                Menu {
                    Button(role: .destructive) {
                        showsSignOutConfirmation = true
                    } label: {
                        Label("Sign Out of AWS", systemImage: "rectangle.portrait.and.arrow.right")
                    }
                } label: {
                    Label("AWS Account", systemImage: "person.crop.circle")
                }
            }
        }
        .confirmationDialog(
            "Sign out of AWS?",
            isPresented: $showsSignOutConfirmation,
            titleVisibility: .visible
        ) {
            Button("Sign Out", role: .destructive) {
                Task { await model.signOutMobile() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Pier will remove its AWS session from this device. You can sign in again and choose another account or permission set.")
        }
        .sheet(isPresented: $showsSettings) {
            NavigationStack {
                PierSettingsView(showsDoneButton: true)
            }
        }
        #endif
        .task {
            if model.instances.isEmpty {
                await model.refreshInstances()
            }
            if model.projects.isEmpty {
                await model.loadProjects()
            }
        }
        .sheet(item: $projectForNewSession) { project in
            NewInstanceView(project: project)
        }
        #if os(macOS)
        .confirmationDialog(
            "Remove \(instancePendingRemoval?.displayBranch ?? "instance")?",
            isPresented: Binding(
                get: { instancePendingRemoval != nil },
                set: { if !$0 { instancePendingRemoval = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button("Remove Instance", role: .destructive) {
                guard let instance = instancePendingRemoval else { return }
                instancePendingRemoval = nil
                Task { await model.removeInstance(instance) }
            }
            Button("Cancel", role: .cancel) {
                instancePendingRemoval = nil
            }
        } message: {
            Text("This permanently destroys the AWS instance and its attached disk. This cannot be undone.")
        }
        #endif
    }

    private func isFailedCreation(_ instance: PierInstance) -> Bool {
        guard let creation = model.creation(for: instance.id) else { return false }
        if case .failed = creation.status {
            return true
        }
        return false
    }
}

private struct ProjectSectionHeader: View {
    let project: PierProject
    let createSession: () -> Void

    private var canCreateSession: Bool {
        #if os(macOS)
        project.id.hasPrefix("/")
        #else
        project.host == "AWS"
        #endif
    }

    var body: some View {
        HStack(spacing: 5) {
            if let repository = project.repository, !repository.isEmpty {
                Text(repository)
                    .fontWeight(.semibold)
                Text("·")
                    .foregroundStyle(.tertiary)
            }
            Text(project.name)
                .fontWeight(.semibold)
            Text("·")
                .foregroundStyle(.tertiary)
            Text(project.host ?? "AWS")
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)

            Spacer(minLength: 6)

            Button(action: createSession) {
                Image(systemName: "plus")
                    .frame(width: 18, height: 18)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .help(canCreateSession ? "New session for \(project.name)" : "Session creation unavailable")
            .disabled(!canCreateSession)
        }
        .textCase(nil)
    }
}

private struct InstanceRow: View {
    let instance: PierInstance
    var isRemoving = false
    var isLoading = false

    var body: some View {
        HStack(spacing: 10) {
            Group {
                if isRemoving {
                    ProgressView()
                        .controlSize(.mini)
                        .accessibilityLabel("Removing \(instance.displayBranch)")
                } else if isLoading {
                    ProgressView()
                        .controlSize(.mini)
                        .accessibilityLabel("Loading \(instance.displayBranch)")
                } else {
                    Circle()
                        .fill(statusColor)
                        .shadow(color: statusColor.opacity(0.45), radius: instance.state == .running || instance.state == .working ? 3 : 0)
                        .accessibilityLabel(instance.state.label)
                }
            }
            .frame(width: 8, height: 8)

            VStack(alignment: .leading, spacing: 3) {
                Text(instance.name)
                    .font(.headline)

                if !instance.branch.isEmpty {
                    Text(instance.branch)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
        }
        .padding(.vertical, 4)
        .opacity(isRemoving ? 0.65 : 1)
        .animation(.easeInOut(duration: 0.15), value: isRemoving)
        .animation(.easeInOut(duration: 0.15), value: isLoading)
    }

    private var statusColor: Color {
        if instance.strained || instance.setup == "failed" {
            return .orange
        }
        switch instance.state {
        case .creating: return Color.orange
        case .running, .working: return Color.green
        case .idle: return Color.yellow
        case .parked: return Color.secondary
        case .dead: return Color.red
        }
    }
}
