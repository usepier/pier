import SwiftUI
#if os(macOS)
import AppKit
#endif

struct NewInstanceView: View {
    @Environment(PierAppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    let project: PierProject
    @State private var name = ""
    @State private var selectedBranch = ""

    private var visibleBranchOptions: PierBranchOptions? {
        guard model.branchOptions?.project.id == project.id else { return nil }
        return model.branchOptions
    }

    private var canCreate: Bool {
        isValidSessionName(name) &&
            !selectedBranch.isEmpty &&
            !model.isLoadingBranches &&
            !model.isCreatingInstance
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                SheetHeader(title: "New Session")

                ScrollView {
                    SessionDetailsFields(
                        name: $name,
                        selectedBranch: $selectedBranch,
                        branchOptions: visibleBranchOptions
                    )
                    .padding(20)
                }
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Create") {
                        if model.beginCreateInstance(
                            project: project,
                            name: name,
                            baseBranch: selectedBranch
                        ) {
                            dismiss()
                        }
                    }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!canCreate)
                }
            }
        }
        #if os(macOS)
        .frame(width: 500, height: 240)
        #endif
        .task {
            await model.loadBranches(projectID: project.id)
            if model.branchOptions?.project.id == project.id {
                selectedBranch = model.branchOptions?.defaultBranch ?? ""
            }
        }
    }
}

#if os(macOS)
struct NewProjectView: View {
    @Environment(PierAppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    @State private var selectedFolderURL: URL?
    @State private var project: PierProject?
    @State private var name = ""
    @State private var selectedBranch = ""

    private var visibleBranchOptions: PierBranchOptions? {
        guard let project, model.branchOptions?.project.id == project.id else { return nil }
        return model.branchOptions
    }

    private var canCreate: Bool {
        project != nil &&
            isValidSessionName(name) &&
            !selectedBranch.isEmpty &&
            !model.isLoadingBranches &&
            !model.isCreatingInstance
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                SheetHeader(title: "New Pier Project")

                ScrollView {
                    VStack(alignment: .leading, spacing: 10) {
                        Text("Git project")
                            .font(.headline)

                        VStack(alignment: .leading, spacing: 12) {
                        if let selectedFolderURL {
                            LabeledContent("Repository", value: project?.name ?? selectedFolderURL.lastPathComponent)
                            LabeledContent("Location", value: project?.path ?? selectedFolderURL.path)
                                .foregroundStyle(.secondary)

                            Button("Choose Different Git Folder…", systemImage: "folder") {
                                chooseProjectFolder()
                            }
                            .disabled(model.isLoadingBranches || model.isCreatingInstance)
                        } else {
                            Button("Choose Git Folder…", systemImage: "folder.badge.plus") {
                                chooseProjectFolder()
                            }
                            .disabled(model.isCreatingInstance)
                        }

                        if model.isLoadingBranches {
                            HStack(spacing: 8) {
                                ProgressView()
                                    .controlSize(.small)
                                Text("Reading repository and fetching branches…")
                                    .foregroundStyle(.secondary)
                            }
                        } else if selectedFolderURL != nil, project == nil, let error = model.errorMessage {
                            Label(error, systemImage: "exclamationmark.triangle")
                                .font(.caption)
                                .foregroundStyle(.red)
                        }
                        }
                        .compactFormCard()

                        Text("Initial session")
                            .font(.headline)
                            .padding(.top, 6)

                        SessionDetailsFields(
                            name: $name,
                            selectedBranch: $selectedBranch,
                            branchOptions: visibleBranchOptions
                        )
                        .disabled(project == nil)
                    }
                    .padding(20)
                }
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Create") {
                        guard let project else { return }
                        if model.beginCreateInstance(
                            project: project,
                            name: name,
                            baseBranch: selectedBranch
                        ) {
                            dismiss()
                        }
                    }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!canCreate)
                }
            }
        }
        .frame(width: 540, height: 430)
    }

    private func chooseProjectFolder() {
        let panel = NSOpenPanel()
        panel.title = "Choose a Git Repository"
        panel.message = "Select the local Git folder for this Pier project."
        panel.prompt = "Choose"
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.canCreateDirectories = false
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            selectedFolderURL = url
            project = nil
            selectedBranch = ""
            Task {
                await model.loadBranches(projectID: url.path)
                guard let options = model.branchOptions else { return }
                project = options.project
                selectedBranch = options.defaultBranch
            }
        }
    }
}
#endif

private struct SheetHeader: View {
    let title: String

    var body: some View {
        VStack(spacing: 0) {
            Text(title)
                .font(.title2.weight(.semibold))
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 20)
                .padding(.vertical, 14)

            Divider()
        }
    }
}

private struct SessionDetailsFields: View {
    @Environment(PierAppModel.self) private var model
    @Binding var name: String
    @Binding var selectedBranch: String
    let branchOptions: PierBranchOptions?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            LabeledContent("Session name") {
                TextField("", text: $name, prompt: Text("feature/my-change"))
                    .textFieldStyle(.roundedBorder)
                    .frame(minWidth: 220, idealWidth: 280)
                    .disabled(model.isCreatingInstance)
            }

            if !name.isEmpty && !isValidSessionName(name) {
                Text("Use letters, digits, dots, underscores, dashes, or slashes.")
                    .font(.caption)
                    .foregroundStyle(.red)
            }

            Divider()

            if model.isLoadingBranches {
                LabeledContent("Base branch") {
                    HStack(spacing: 8) {
                        ProgressView()
                            .controlSize(.small)
                        Text("Fetching…")
                            .foregroundStyle(.secondary)
                    }
                }
            } else {
                LabeledContent("Base branch") {
                    Picker("", selection: $selectedBranch) {
                        ForEach(branchOptions?.branches ?? [], id: \.self) { branch in
                            Text(branch).tag(branch)
                        }
                    }
                    .labelsHidden()
                    .frame(minWidth: 180, alignment: .trailing)
                    .disabled(model.isCreatingInstance || branchOptions == nil)
                }
            }

            if let warning = branchOptions?.fetchWarning {
                Label(warning, systemImage: "exclamationmark.triangle")
                    .font(.caption)
                    .foregroundStyle(.orange)
            }
        }
        .compactFormCard()
    }
}

private extension View {
    func compactFormCard() -> some View {
        padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color.primary.opacity(0.055), in: RoundedRectangle(cornerRadius: 12))
    }
}

private func isValidSessionName(_ name: String) -> Bool {
    guard !name.isEmpty, name.count <= 100, let first = name.first else { return false }
    let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-/")
    return name.unicodeScalars.allSatisfy(allowed.contains) &&
        !"-./".contains(first) &&
        !name.hasSuffix("/") &&
        !name.hasSuffix(".") &&
        !name.hasSuffix(".lock") &&
        !name.contains("..") &&
        !name.contains("//")
}
