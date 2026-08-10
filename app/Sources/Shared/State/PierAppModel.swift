import Foundation
import Observation

@MainActor
@Observable
final class PierAppModel {
    private let service: any PierServicing

    var setupStatus: PierSetupStatus?
    var instances: [PierInstance] = []
    var projects: [PierProject] = []
    var branchOptions: PierBranchOptions?
    var selectedInstanceID: PierInstance.ID?
    var snapshots: [PierInstance.ID: PierInstanceSnapshot] = [:]
    private(set) var creationProgress: [PierInstance.ID: PierCreationProgress] = [:]
    private(set) var hiddenSetupTabInstanceIDs: Set<PierInstance.ID> = []
    var isLoading = false
    var errorMessage: String?
    var requiresAWSLogin = false
    var onboardingDismissed = false
    var isLoadingProjects = false
    var isLoadingBranches = false
    var isCreatingInstance = false
    var mobileAuthorization: PierMobileAuthorization?
    var mobileAccounts: [PierAWSAccount] = []
    var mobileRoles: [PierAWSRole] = []
    var isAuthorizingMobile = false
    private(set) var unparkingInstanceIDs: Set<PierInstance.ID> = []
    private(set) var removingInstanceIDs: Set<PierInstance.ID> = []
    private(set) var inspectingInstanceIDs: Set<PierInstance.ID> = []
    private(set) var closingTabKeys: Set<String> = []
    private(set) var openingPortKeys: Set<String> = []
    private(set) var tabOrders: [PierInstance.ID: [PierTab.ID]] = [:]
    private var terminalTitles: [String: String] = [:]
    private var branchRequestID = UUID()
    private var isRefreshingInstances = false
    private var refreshingSnapshotIDs: Set<PierInstance.ID> = []

    init(service: any PierServicing) {
        self.service = service
    }

    var selectedInstance: PierInstance? {
        instances.first { $0.id == selectedInstanceID }
    }

    var showsOnboarding: Bool {
        guard !onboardingDismissed else { return false }
        return setupStatus?.configured != true
    }

    func start() async {
        guard setupStatus == nil else { return }
        await refreshSetup()
        if setupStatus?.configured == true {
            try? await service.preparePortProxy()
            await refreshInstances()
            await loadProjects()
        }
    }

    func refreshSetup() async {
        await perform {
            setupStatus = try await service.setupStatus()
        }
    }

    func refreshInstances() async {
        await updateInstances(reportsActivity: true, reportsErrors: true)
    }

    func syncInstances() async {
        await updateInstances(reportsActivity: false, reportsErrors: false)
    }

    func isInspectingInstance(_ instanceID: PierInstance.ID) -> Bool {
        inspectingInstanceIDs.contains(instanceID)
    }

    func inspect(_ instance: PierInstance) async {
        await updateSnapshot(for: instance, reportsActivity: true, reportsErrors: true)
    }

    func syncInstance(id: PierInstance.ID) async {
        guard let instance = instances.first(where: { $0.id == id }) else { return }
        await updateSnapshot(for: instance, reportsActivity: false, reportsErrors: false)
    }

    private func updateInstances(reportsActivity: Bool, reportsErrors: Bool) async {
        guard !isRefreshingInstances else { return }
        isRefreshingInstances = true
        if reportsActivity { isLoading = true }
        if reportsErrors {
            errorMessage = nil
            requiresAWSLogin = false
        }
        defer {
            isRefreshingInstances = false
            if reportsActivity { isLoading = false }
        }

        do {
            applyInstanceList(try await service.listInstances())
        } catch is CancellationError {
            // Foreground sync tasks are cancelled when the app becomes inactive.
        } catch where reportsErrors {
            errorMessage = error.localizedDescription
            requiresAWSLogin = (error as? PierAPIError)?.code == "aws_login_required"
        } catch {
            // Periodic synchronization is best-effort. Manual refresh still
            // reports errors and offers AWS sign-in when needed.
        }
    }

    private func updateSnapshot(
        for instance: PierInstance,
        reportsActivity: Bool,
        reportsErrors: Bool
    ) async {
        guard instance.state != .parked && instance.state != .creating && instance.state != .dead else { return }
        guard refreshingSnapshotIDs.insert(instance.id).inserted else { return }
        if reportsActivity { inspectingInstanceIDs.insert(instance.id) }
        if reportsErrors {
            errorMessage = nil
            requiresAWSLogin = false
        }
        defer {
            refreshingSnapshotIDs.remove(instance.id)
            if reportsActivity { inspectingInstanceIDs.remove(instance.id) }
        }

        do {
            snapshots[instance.id] = try await service.inspectInstance(id: instance.id)
        } catch is CancellationError {
            // Selecting another session cancels this view task; that is not an error to surface.
        } catch where reportsErrors {
            errorMessage = error.localizedDescription
            requiresAWSLogin = (error as? PierAPIError)?.code == "aws_login_required"
        } catch {
            // Keep the last good snapshot when a background poll fails.
        }
    }

    func loadProjects() async {
        isLoadingProjects = true
        errorMessage = nil
        defer { isLoadingProjects = false }
        do {
            projects = try await service.listProjects()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func addProject(path: String) async {
        isLoadingProjects = true
        errorMessage = nil
        defer { isLoadingProjects = false }
        do {
            let project = try await service.addProject(path: path)
            if !projects.contains(where: { $0.id == project.id }) {
                projects.append(project)
                projects.sort { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
            }
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func loadBranches(projectID: String) async {
        let requestID = UUID()
        branchRequestID = requestID
        isLoadingBranches = true
        branchOptions = nil
        errorMessage = nil
        do {
            let options = try await service.listBranches(projectID: projectID)
            guard branchRequestID == requestID else { return }
            branchOptions = options
        } catch {
            guard branchRequestID == requestID else { return }
            errorMessage = error.localizedDescription
        }
        if branchRequestID == requestID {
            isLoadingBranches = false
        }
    }

    func creation(for instanceID: PierInstance.ID) -> PierCreationProgress? {
        creationProgress[instanceID]
    }

    func isSetupTabVisible(for instanceID: PierInstance.ID) -> Bool {
        creationProgress[instanceID] != nil && !hiddenSetupTabInstanceIDs.contains(instanceID)
    }

    func hideSetupTab(for instanceID: PierInstance.ID) {
        guard creationProgress[instanceID] != nil else { return }
        hiddenSetupTabInstanceIDs.insert(instanceID)
    }

    func showSetupTab(for instanceID: PierInstance.ID) {
        guard creationProgress[instanceID] != nil else { return }
        hiddenSetupTabInstanceIDs.remove(instanceID)
    }

    func orderedTabs(_ tabs: [PierTab], for instanceID: PierInstance.ID) -> [PierTab] {
        let positions = Dictionary(
            uniqueKeysWithValues: (tabOrders[instanceID] ?? []).enumerated().map { ($0.element, $0.offset) }
        )
        return tabs.enumerated().sorted { left, right in
            let leftPosition = positions[left.element.id] ?? (positions.count + left.offset)
            let rightPosition = positions[right.element.id] ?? (positions.count + right.offset)
            return leftPosition < rightPosition
        }.map(\.element)
    }

    func moveTab(
        instanceID: PierInstance.ID,
        tabID: PierTab.ID,
        to targetID: PierTab.ID,
        tabs: [PierTab]
    ) {
        var ids = orderedTabs(tabs, for: instanceID).map(\.id)
        guard let sourceIndex = ids.firstIndex(of: tabID),
              let targetIndex = ids.firstIndex(of: targetID),
              sourceIndex != targetIndex else { return }
        ids.remove(at: sourceIndex)
        let insertionIndex = sourceIndex < targetIndex ? targetIndex : targetIndex
        ids.insert(tabID, at: min(insertionIndex, ids.count))
        tabOrders[instanceID] = ids
    }

    @discardableResult
    func beginCreateInstance(project: PierProject, name: String, baseBranch: String) -> Bool {
        guard !isCreatingInstance else { return false }

        if !projects.contains(where: { $0.id == project.id }) {
            projects.append(project)
            projects.sort { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
        }

        let placeholderID = "pending:\(UUID().uuidString)"
        let placeholder = PierInstance(
            id: placeholderID,
            name: name,
            repo: project.name,
            branch: name,
            user: "",
            driver: "aws-ec2",
            state: .creating,
            strained: false,
            setup: "running",
            instanceType: "Preparing…",
            createdAt: ISO8601DateFormatter().string(from: Date()),
            costNote: "Pending",
            localPath: project.path,
            projectID: project.id
        )
        creationProgress[placeholderID] = PierCreationProgress(
            instanceID: placeholderID,
            project: project,
            name: name,
            baseBranch: baseBranch,
            steps: [PierCreationStep(message: "Preparing the project and checking AWS…")],
            status: .provisioning
        )
        hiddenSetupTabInstanceIDs.remove(placeholderID)
        instances.append(placeholder)
        selectedInstanceID = placeholderID
        isCreatingInstance = true
        errorMessage = nil

        Task {
            await finishCreatingInstance(
                placeholderID: placeholderID,
                projectID: project.id,
                name: name,
                baseBranch: baseBranch
            )
        }
        return true
    }

    func removeFailedCreation(instanceID: PierInstance.ID) {
        guard let progress = creationProgress[instanceID], case .failed = progress.status else { return }
        creationProgress.removeValue(forKey: instanceID)
        hiddenSetupTabInstanceIDs.remove(instanceID)
        instances.removeAll { $0.id == instanceID }
        snapshots.removeValue(forKey: instanceID)
        if selectedInstanceID == instanceID {
            selectedInstanceID = instances.first?.id
        }
    }

    func isRemovingInstance(_ instanceID: PierInstance.ID) -> Bool {
        removingInstanceIDs.contains(instanceID)
    }

    func isUnparkingInstance(_ instanceID: PierInstance.ID) -> Bool {
        unparkingInstanceIDs.contains(instanceID)
    }

    @discardableResult
    func unpark(_ instance: PierInstance) async -> Bool {
        guard instance.state == .parked else { return false }
        guard unparkingInstanceIDs.insert(instance.id).inserted else { return false }
        errorMessage = nil
        requiresAWSLogin = false
        defer { unparkingInstanceIDs.remove(instance.id) }

        do {
            try await service.unparkInstance(id: instance.id)
            applyInstanceList(try await service.listInstances())
            if let resumed = instances.first(where: { $0.id == instance.id }) {
                await updateSnapshot(for: resumed, reportsActivity: false, reportsErrors: false)
            }
            return true
        } catch {
            errorMessage = error.localizedDescription
            requiresAWSLogin = (error as? PierAPIError)?.code == "aws_login_required"
            return false
        }
    }

    @discardableResult
    func removeInstance(_ instance: PierInstance) async -> Bool {
        if let progress = creationProgress[instance.id], progress.status != .completed {
            return false
        }
        guard removingInstanceIDs.insert(instance.id).inserted else { return false }
        errorMessage = nil
        requiresAWSLogin = false
        defer { removingInstanceIDs.remove(instance.id) }

        let removedIndex = instances.firstIndex(where: { $0.id == instance.id })
        do {
            try await service.removeInstance(id: instance.id)
            instances.removeAll { $0.id == instance.id }
            snapshots.removeValue(forKey: instance.id)
            creationProgress.removeValue(forKey: instance.id)
            hiddenSetupTabInstanceIDs.remove(instance.id)
            for key in Array(terminalTitles.keys) where key.hasPrefix("\(instance.id):") {
                terminalTitles.removeValue(forKey: key)
            }
            if selectedInstanceID == instance.id {
                if let removedIndex, instances.indices.contains(removedIndex) {
                    selectedInstanceID = instances[removedIndex].id
                } else {
                    selectedInstanceID = instances.last?.id
                }
            }
            return true
        } catch {
            errorMessage = error.localizedDescription
            requiresAWSLogin = (error as? PierAPIError)?.code == "aws_login_required"
            return false
        }
    }

    private func finishCreatingInstance(
        placeholderID: PierInstance.ID,
        projectID: String,
        name: String,
        baseBranch: String
    ) async {
        defer { isCreatingInstance = false }
        do {
            let created = try await service.createInstance(
                projectID: projectID,
                name: name,
                baseBranch: baseBranch,
                onProgress: { [weak self] event in
                    Task { @MainActor [weak self] in
                        self?.handleCreationEvent(event, instanceID: placeholderID)
                    }
                }
            )
            if let progress = creationProgress.removeValue(forKey: placeholderID) {
                creationProgress[created.id] = PierCreationProgress(
                    instanceID: created.id,
                    project: progress.project,
                    name: progress.name,
                    baseBranch: progress.baseBranch,
                    steps: progress.steps,
                    status: .completed,
                    customSetup: progress.customSetup
                )
            }
            if hiddenSetupTabInstanceIDs.remove(placeholderID) != nil {
                hiddenSetupTabInstanceIDs.insert(created.id)
            }
            if let index = instances.firstIndex(where: { $0.id == placeholderID }) {
                instances[index] = created
            } else {
                instances.append(created)
            }
            snapshots.removeValue(forKey: placeholderID)
            if selectedInstanceID == placeholderID {
                selectedInstanceID = created.id
            }
        } catch {
            let message = creationFailureMessage(error)
            appendCreationStep("Creation failed: \(message)", instanceID: placeholderID)
            if var progress = creationProgress[placeholderID] {
                progress.status = .failed(message)
                creationProgress[placeholderID] = progress
            }
            markCreationFailed(instanceID: placeholderID)
            errorMessage = message
            requiresAWSLogin = (error as? PierAPIError)?.code == "aws_login_required"
        }
    }

    private func appendCreationStep(_ message: String, instanceID: PierInstance.ID) {
        let message = message.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !message.isEmpty, var progress = creationProgress[instanceID] else { return }
        guard progress.steps.last?.message != message else { return }
        progress.steps.append(PierCreationStep(message: message))
        creationProgress[instanceID] = progress
    }

    private func handleCreationEvent(_ event: PierCreationEvent, instanceID: PierInstance.ID) {
        switch event {
        case .step(let message):
            appendCreationStep(message, instanceID: instanceID)
        case .setupStarted(let name):
            guard var progress = creationProgress[instanceID] else { return }
            progress.customSetup = PierCustomSetupProgress(name: name, output: [], status: .running)
            creationProgress[instanceID] = progress
        case .setupOutput(let line):
            guard var progress = creationProgress[instanceID], var setup = progress.customSetup else { return }
            setup.output.append(line)
            if setup.output.count > 2_000 {
                setup.output.removeFirst(setup.output.count - 2_000)
            }
            progress.customSetup = setup
            creationProgress[instanceID] = progress
        case .setupFinished(let name, let exitCode):
            guard var progress = creationProgress[instanceID] else { return }
            var setup = progress.customSetup ?? PierCustomSetupProgress(name: name, output: [], status: .running)
            setup.status = exitCode == 0 ? .completed : .failed(exitCode)
            progress.customSetup = setup
            creationProgress[instanceID] = progress
        }
    }

    private func markCreationFailed(instanceID: PierInstance.ID) {
        guard let index = instances.firstIndex(where: { $0.id == instanceID }) else { return }
        let instance = instances[index]
        instances[index] = PierInstance(
            id: instance.id,
            name: instance.name,
            repo: instance.repo,
            branch: instance.branch,
            user: instance.user,
            driver: instance.driver,
            state: .dead,
            strained: false,
            setup: "failed",
            instanceType: instance.instanceType,
            createdAt: instance.createdAt,
            costNote: instance.costNote,
            localPath: instance.localPath,
            projectID: instance.projectID
        )
    }

    private func mergingPendingCreations(into remoteInstances: [PierInstance]) -> [PierInstance] {
        let unfinishedProgress = creationProgress.filter { $0.value.status != .completed }
        let pendingInstances = instances.filter { unfinishedProgress[$0.id] != nil }
        let remoteWithoutPendingDuplicates = remoteInstances.filter { remote in
            !unfinishedProgress.values.contains { progress in
                remote.name == progress.name && remote.projectName == progress.project.name
            }
        }
        return remoteWithoutPendingDuplicates + pendingInstances
    }

    private func applyInstanceList(_ remoteInstances: [PierInstance]) {
        instances = mergingPendingCreations(into: remoteInstances)
        if selectedInstanceID == nil || !instances.contains(where: { $0.id == selectedInstanceID }) {
            selectedInstanceID = instances.first?.id
        }
    }

    private func creationFailureMessage(_ error: Error) -> String {
        let lines = error.localizedDescription
            .split(whereSeparator: \.isNewline)
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
        return lines.last ?? "Pier could not create the session."
    }

    func createTab(instanceID: String, name: String, command: [String]) async -> PierTab? {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }
        do {
            let tab = try await service.createTab(instanceID: instanceID, name: name, command: command)
            snapshots[instanceID] = try await service.inspectInstance(id: instanceID)
            return tab
        } catch {
            errorMessage = error.localizedDescription
            return nil
        }
    }

    func isClosingTab(instanceID: String, tabID: String) -> Bool {
        closingTabKeys.contains(tabKey(instanceID: instanceID, tabID: tabID))
    }

    func terminalTitle(instanceID: String, tab: PierTab) -> String {
        terminalTitles[tabKey(instanceID: instanceID, tabID: tab.id)] ?? tab.command
    }

    func isOpeningPort(instanceID: String, port: Int) -> Bool {
        openingPortKeys.contains("\(instanceID):port:\(port)")
    }

    func openPort(instanceID: String, port: PierPort) async -> PierPortEndpoint? {
        let key = "\(instanceID):port:\(port.number)"
        guard openingPortKeys.insert(key).inserted else { return nil }
        errorMessage = nil
        defer { openingPortKeys.remove(key) }
        guard let proxyHost = snapshots[instanceID]?.proxyHost, !proxyHost.isEmpty else {
            errorMessage = "Pier did not report a .pier hostname for this session."
            return nil
        }
        do {
            return try await service.openPort(proxyHost: proxyHost, port: port)
        } catch {
            errorMessage = error.localizedDescription
            return nil
        }
    }

    func updateTerminalTitle(instanceID: String, tabID: String, title: String) {
        let title = title.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return }
        terminalTitles[tabKey(instanceID: instanceID, tabID: tabID)] = title
    }

    @discardableResult
    func closeTab(instanceID: String, tabID: String) async -> Bool {
        let key = tabKey(instanceID: instanceID, tabID: tabID)
        guard closingTabKeys.insert(key).inserted else { return false }
        errorMessage = nil
        defer { closingTabKeys.remove(key) }

        do {
            try await service.closeTab(instanceID: instanceID, tabID: tabID)
            snapshots[instanceID] = try await service.inspectInstance(id: instanceID)
            terminalTitles.removeValue(forKey: key)
            return true
        } catch {
            errorMessage = error.localizedDescription
            return false
        }
    }

    func continueWithoutSetup() async {
        onboardingDismissed = true
        await refreshInstances()
    }

    func signInAWS() async {
        await perform {
            try await service.signInAWS()
            instances = try await service.listInstances()
            selectedInstanceID = instances.first?.id
        }
    }

    func beginMobileSignIn(_ request: PierMobileSignInRequest) async -> PierMobileAuthorization? {
        isAuthorizingMobile = true
        mobileAccounts = []
        mobileRoles = []
        errorMessage = nil
        do {
            let authorization = try await service.beginMobileSignIn(request)
            mobileAuthorization = authorization
            return authorization
        } catch {
            isAuthorizingMobile = false
            errorMessage = error.localizedDescription
            return nil
        }
    }

    func completeMobileSignIn() async {
        do {
            mobileAccounts = try await service.completeMobileSignIn()
        } catch {
            errorMessage = error.localizedDescription
        }
        isAuthorizingMobile = false
    }

    func loadMobileRoles(accountID: String) async {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }
        do {
            mobileRoles = try await service.listMobileRoles(accountID: accountID)
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func finishMobileSetup(accountID: String, roleName: String) async -> Bool {
        isLoading = true
        errorMessage = nil
        defer { isLoading = false }
        do {
            try await service.finishMobileSetup(accountID: accountID, roleName: roleName)
            setupStatus = try await service.setupStatus()
            await refreshInstances()
            await loadProjects()
            return true
        } catch {
            errorMessage = error.localizedDescription
            return false
        }
    }

    @discardableResult
    func signOutMobile() async -> Bool {
        isLoading = true
        errorMessage = nil
        requiresAWSLogin = false
        defer { isLoading = false }
        do {
            try await service.signOutMobile()
            setupStatus = try await service.setupStatus()
            onboardingDismissed = false
            instances = []
            projects = []
            branchOptions = nil
            selectedInstanceID = nil
            snapshots = [:]
            mobileAuthorization = nil
            mobileAccounts = []
            mobileRoles = []
            terminalTitles = [:]
            return true
        } catch {
            errorMessage = error.localizedDescription
            return false
        }
    }

    func dismissError() {
        errorMessage = nil
        requiresAWSLogin = false
    }

    private func perform(_ operation: () async throws -> Void) async {
        isLoading = true
        errorMessage = nil
        requiresAWSLogin = false
        defer { isLoading = false }
        do {
            try await operation()
        } catch {
            errorMessage = error.localizedDescription
            requiresAWSLogin = (error as? PierAPIError)?.code == "aws_login_required"
        }
    }

    private func tabKey(instanceID: String, tabID: String) -> String {
        "\(instanceID):\(tabID)"
    }
}
