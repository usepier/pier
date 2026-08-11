import XCTest
@testable import PierMacOS

final class PierModelsTests: XCTestCase {
    func testParsesStructuredCreationEvents() {
        XCTAssertEqual(
            PierCLICreationEventParser.parse(
                #"PIER_CREATE_EVENT {"kind":"setup_started","message":".pier-setup.sh"}"#
            ),
            .setupStarted(".pier-setup.sh")
        )
        XCTAssertEqual(
            PierCLICreationEventParser.parse(
                #"PIER_CREATE_EVENT {"kind":"setup_output","message":"make build"}"#
            ),
            .setupOutput("make build")
        )
        XCTAssertEqual(
            PierCLICreationEventParser.parse(
                #"PIER_CREATE_EVENT {"kind":"setup_finished","message":".pier-setup.sh","exitCode":2}"#
            ),
            .setupFinished(name: ".pier-setup.sh", exitCode: 2)
        )
        XCTAssertEqual(PierCLICreationEventParser.parse("launched i-123"), .step("launched i-123"))
    }

    func testProcessEnvironmentIncludesGUIExecutablePaths() {
        let environment = PierProcessEnvironment.runtime(from: ["PATH": "/custom/bin"])

        XCTAssertEqual(
            environment["PATH"],
            "/custom/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        )
    }

    func testInstanceSubtitleIncludesDifferentBranch() {
        let instance = PierInstance(
            id: "i-123",
            name: "fix-login",
            repo: "pier",
            branch: "fix-login",
            user: "developer",
            driver: "aws-ec2",
            state: .working,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-09T10:00:00Z",
            costNote: "$0.034/h",
            localPath: "~/Documents/pier",
            projectID: "/Users/developer/Documents/pier"
        )

        XCTAssertEqual(instance.subtitle, "pier · fix-login")
        XCTAssertEqual(instance.displayBranch, "fix-login")
        XCTAssertEqual(instance.displayLocalPath, "~/Documents/pier")
    }

    func testDecodesVersionedCLIEnvelope() throws {
        let response = #"""
        {"version":1,"data":[{"id":"i-123","name":"fix-login","repo":"pier","branch":"fix-login","user":"developer","driver":"aws-ec2","state":"working","strained":false,"setup":"","instanceType":"t4g.medium","createdAt":"2026-08-09T10:00:00Z","costNote":"$0.034/h"}]}
        """#.data(using: .utf8)!

        let envelope = try JSONDecoder().decode(PierAPIEnvelope<[PierInstance]>.self, from: response)

        XCTAssertEqual(envelope.version, 1)
        XCTAssertEqual(envelope.data?.first?.state, .working)
        XCTAssertNil(envelope.error)
    }

    func testDecodesDiscoveredPortDetails() throws {
        let response = #"""
        {"number":4200,"process":"frontend (Docker)","isHTTP":true,"scheme":"http"}
        """#.data(using: .utf8)!

        let port = try JSONDecoder().decode(PierPort.self, from: response)

        XCTAssertEqual(port.number, 4200)
        XCTAssertEqual(port.process, "frontend (Docker)")
        XCTAssertTrue(port.isHTTP)
        XCTAssertEqual(port.scheme, "http")
    }

    @MainActor
    func testCreationAppearsImmediatelyAndReceivesProgress() async {
        let project = PierProject(id: "/tmp/pier", name: "pier", path: "~/Documents/pier")
        let created = PierInstance(
            id: "i-created",
            name: "feature/setup-view",
            repo: "pier",
            branch: "feature/setup-view",
            user: "developer",
            driver: "aws-ec2",
            state: .running,
            strained: false,
            setup: "running",
            instanceType: "t4g.medium",
            createdAt: "2026-08-09T10:00:00Z",
            costNote: "$0.034/h",
            localPath: "~/Documents/pier",
            projectID: "/tmp/pier"
        )
        let service = ControlledCreationService(project: project, created: created)
        let model = PierAppModel(service: service)

        XCTAssertTrue(model.beginCreateInstance(
            project: project,
            name: created.name,
            baseBranch: "origin/main"
        ))

        let placeholderID = try! XCTUnwrap(model.selectedInstanceID)
        XCTAssertEqual(model.projects, [project])
        XCTAssertEqual(model.selectedInstance?.state, .creating)
        XCTAssertEqual(model.creation(for: placeholderID)?.name, created.name)
        XCTAssertTrue(model.isSetupTabVisible(for: placeholderID))

        while !(await service.isWaitingForCompletion()) {
            await Task.yield()
        }
        for _ in 0..<100 where (model.creation(for: placeholderID)?.steps.count ?? 0) < 2 {
            await Task.yield()
        }
        XCTAssertEqual(model.creation(for: placeholderID)?.steps.last?.message, "launched i-created")

        await service.emitSetupProgress()
        for _ in 0..<100 where model.creation(for: placeholderID)?.customSetup?.status != .completed {
            await Task.yield()
        }
        XCTAssertEqual(model.creation(for: placeholderID)?.customSetup?.name, ".pier-setup.sh")
        XCTAssertEqual(model.creation(for: placeholderID)?.customSetup?.output, ["make install", "make build"])
        XCTAssertEqual(model.creation(for: placeholderID)?.customSetup?.status, .completed)

        await service.complete()
        for _ in 0..<100 where model.selectedInstanceID != created.id {
            await Task.yield()
        }
        XCTAssertEqual(model.selectedInstanceID, created.id)
        XCTAssertEqual(model.selectedInstance, created)
        XCTAssertNil(model.creation(for: placeholderID))
        XCTAssertEqual(model.creation(for: created.id)?.status, .completed)
        XCTAssertTrue(model.isSetupTabVisible(for: created.id))

        model.hideSetupTab(for: created.id)
        XCTAssertFalse(model.isSetupTabVisible(for: created.id))
        XCTAssertNotNil(model.creation(for: created.id))
        model.showSetupTab(for: created.id)
        XCTAssertTrue(model.isSetupTabVisible(for: created.id))
    }

    @MainActor
    func testRemovalClearsInstanceAndSelection() async {
        let project = PierProject(id: "/tmp/pier", name: "pier", path: "~/Documents/pier")
        let instance = PierInstance(
            id: "i-remove",
            name: "old-session",
            repo: "pier",
            branch: "old-session",
            user: "developer",
            driver: "aws-ec2",
            state: .parked,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-09T10:00:00Z",
            costNote: "$0.034/h",
            localPath: "~/Documents/pier",
            projectID: "/tmp/pier"
        )
        let service = ControlledCreationService(project: project, created: instance)
        let model = PierAppModel(service: service)
        model.instances = [instance]
        model.selectedInstanceID = instance.id

        let removed = await model.removeInstance(instance)
        let removedIDs = await service.removedIDs()

        XCTAssertTrue(removed)
        XCTAssertEqual(removedIDs, [instance.id])
        XCTAssertTrue(model.instances.isEmpty)
        XCTAssertNil(model.selectedInstanceID)
        XCTAssertFalse(model.isRemovingInstance(instance.id))
    }

    @MainActor
    func testUnparkUsesPerInstanceLoadingState() async {
        let instance = PierInstance(
            id: "i-parked",
            name: "parked-session",
            repo: "pier",
            branch: "parked-session",
            user: "developer",
            driver: "aws-ec2",
            state: .parked,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-09T10:00:00Z",
            costNote: "~$3-4/mo",
            localPath: "~/Documents/pier",
            projectID: "/tmp/pier"
        )
        let service = SynchronizationService(instance: instance)
        let model = PierAppModel(service: service)
        model.instances = [instance]

        let unpark = Task { await model.unpark(instance) }
        while !(await service.isWaitingForUnpark()) {
            await Task.yield()
        }

        XCTAssertTrue(model.isUnparkingInstance(instance.id))
        XCTAssertFalse(model.isLoading)

        await service.completeUnpark()
        let succeeded = await unpark.value
        let unparkedIDs = await service.unparkedIDs()
        XCTAssertTrue(succeeded)
        XCTAssertEqual(unparkedIDs, [instance.id])
        XCTAssertFalse(model.isUnparkingInstance(instance.id))
    }

    @MainActor
    func testInspectionHasANonBlockingPerInstanceLoadingState() async {
        let project = PierProject(id: "/tmp/pier", name: "pier", path: "~/Documents/pier")
        let instance = PierInstance(
            id: "i-inspect",
            name: "feature/loading-state",
            repo: "pier",
            branch: "feature/loading-state",
            user: "developer",
            driver: "aws-ec2",
            state: .working,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-09T10:00:00Z",
            costNote: "$0.034/h",
            localPath: "~/Documents/pier",
            projectID: project.id
        )
        let snapshot = PierInstanceSnapshot(
            instance: instance,
            proxyHost: nil,
            tabs: [],
            ports: []
        )
        let service = ControlledCreationService(project: project, created: instance)
        let model = PierAppModel(service: service)

        let inspection = Task { await model.inspect(instance) }
        while !(await service.isWaitingForInspection()) {
            await Task.yield()
        }

        XCTAssertTrue(model.isInspectingInstance(instance.id))
        XCTAssertFalse(model.isLoading)

        await service.completeInspection(with: snapshot)
        await inspection.value

        XCTAssertFalse(model.isInspectingInstance(instance.id))
        XCTAssertEqual(model.snapshots[instance.id], snapshot)
    }

    @MainActor
    func testSynchronizationFindsRemoteInstancesAndTabsWithoutLoadingUI() async {
        let first = PierInstance(
            id: "i-first",
            name: "first-session",
            repo: "pier",
            branch: "first-session",
            user: "developer",
            driver: "aws-ec2",
            state: .running,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-09T10:00:00Z",
            costNote: "$0.034/h",
            localPath: "~/Documents/pier",
            projectID: "/tmp/pier"
        )
        let second = PierInstance(
            id: "i-second",
            name: "opened-on-ios",
            repo: "pier",
            branch: "opened-on-ios",
            user: "developer",
            driver: "aws-ec2",
            state: .running,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-10T10:00:00Z",
            costNote: "$0.034/h",
            localPath: "~/Documents/pier",
            projectID: "/tmp/pier"
        )
        let iosTab = PierTab(
            id: "ios-tab",
            name: "Codex",
            active: true,
            panes: 1,
            command: "codex",
            workingDirectory: "~/Documents/pier"
        )
        let service = SynchronizationService(instance: first)
        let model = PierAppModel(service: service)

        await model.refreshInstances()
        await service.update(instances: [first, second], tabs: [iosTab])
        await model.syncInstances()
        await model.syncInstance(id: first.id)

        XCTAssertEqual(model.instances.map(\.id), [first.id, second.id])
        XCTAssertEqual(model.snapshots[first.id]?.tabs, [iosTab])
        XCTAssertFalse(model.isLoading)
        XCTAssertFalse(model.isInspectingInstance(first.id))
    }

    @MainActor
    func testMobileSignOutClearsVisibleSessionState() async {
        let instance = PierInstance(
            id: "i-signed-out",
            name: "wrong-role-session",
            repo: "pier",
            branch: "main",
            user: "developer",
            driver: "aws-ec2",
            state: .running,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-10T10:00:00Z",
            costNote: "$0.034/h",
            localPath: "~/Documents/pier",
            projectID: "/tmp/pier"
        )
        let service = SynchronizationService(instance: instance)
        let model = PierAppModel(service: service)
        model.setupStatus = try! await service.setupStatus()
        model.instances = [instance]
        model.projects = [PierProject(id: "/tmp/pier", name: "pier", path: "~/Documents/pier")]
        model.selectedInstanceID = instance.id

        let signedOut = await model.signOutMobile()
        XCTAssertTrue(signedOut)
        XCTAssertFalse(model.setupStatus?.configured ?? true)
        XCTAssertTrue(model.instances.isEmpty)
        XCTAssertTrue(model.projects.isEmpty)
        XCTAssertNil(model.selectedInstanceID)
        XCTAssertTrue(model.showsOnboarding)
    }

    @MainActor
    func testExpiredMobileAuthenticationReturnsToOnboardingWithoutRawError() async {
        let instance = PierInstance(
            id: "i-expired-auth",
            name: "expired-auth",
            repo: "pier",
            branch: "main",
            user: "developer",
            driver: "aws-ec2",
            state: .running,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-10T10:00:00Z",
            costNote: "$0.034/h",
            localPath: nil,
            projectID: "aws:pier"
        )
        let service = SynchronizationService(instance: instance)
        let model = PierAppModel(service: service)
        model.setupStatus = try! await service.setupStatus()
        await service.expireAuthentication()

        await model.refreshInstances()

        XCTAssertTrue(model.requiresMobileReauthentication)
        XCTAssertTrue(model.showsOnboarding)
        XCTAssertNil(model.errorMessage)
    }

    @MainActor
    func testMacLaunchUsesInstanceLoadForAuthAndSignInReloadsWorkspace() async {
        let instance = PierInstance(
            id: "i-launch-auth",
            name: "launch-auth",
            repo: "pier",
            branch: "main",
            user: "developer",
            driver: "aws-ec2",
            state: .running,
            strained: false,
            setup: "",
            instanceType: "t4g.medium",
            createdAt: "2026-08-10T10:00:00Z",
            costNote: "$0.034/h",
            localPath: "~/Documents/pier",
            projectID: "/tmp/pier"
        )
        let service = SynchronizationService(instance: instance)
        await service.requireLoginAtLaunch()
        let model = PierAppModel(service: service)

        await model.start()

        let blockedCounts = await service.workspaceRequestCounts()
        XCTAssertTrue(model.requiresAWSLogin)
        XCTAssertTrue(model.instances.isEmpty)
        XCTAssertEqual(blockedCounts.instances, 1)
        XCTAssertEqual(blockedCounts.projects, 0)

        await model.signInAWS()

        let signedInCounts = await service.workspaceRequestCounts()
        XCTAssertFalse(model.requiresAWSLogin)
        XCTAssertNil(model.errorMessage)
        XCTAssertEqual(model.instances.map(\.id), [instance.id])
        XCTAssertEqual(signedInCounts.instances, 2)
        XCTAssertEqual(signedInCounts.projects, 1)
    }
}

private actor SynchronizationService: PierServicing {
    private var instances: [PierInstance]
    private var tabs: [PierTab] = []
    private var unparkContinuation: CheckedContinuation<Void, any Error>?
    private var requestedUnparkIDs: [String] = []
    private var signedOut = false
    private var authenticationExpired = false
    private var launchRequiresLogin = false
    private var instanceRequestCount = 0
    private var projectRequestCount = 0

    init(instance: PierInstance) {
        instances = [instance]
    }

    func update(instances: [PierInstance], tabs: [PierTab]) {
        self.instances = instances
        self.tabs = tabs
    }

    func expireAuthentication() {
        authenticationExpired = true
    }

    func requireLoginAtLaunch() {
        launchRequiresLogin = true
    }

    func workspaceRequestCounts() -> (instances: Int, projects: Int) {
        (instanceRequestCount, projectRequestCount)
    }

    func setupStatus() async throws -> PierSetupStatus {
        PierSetupStatus(configured: !signedOut, configPath: "", cliVersion: "test", profiles: [], dependencies: [])
    }

    func listInstances() async throws -> [PierInstance] {
        instanceRequestCount += 1
        if launchRequiresLogin {
            throw PierAPIError(
                code: "aws_login_required",
                message: "Your AWS session has expired. Sign in again."
            )
        }
        if authenticationExpired {
            throw PierServiceFailure.authenticationRequired(
                "Your AWS session has expired. Sign in again to reconnect Pier."
            )
        }
        return instances
    }
    func listProjects() async throws -> [PierProject] {
        projectRequestCount += 1
        return []
    }
    func addProject(path: String) async throws -> PierProject {
        throw PierServiceFailure.unavailable("Not used in this test")
    }
    func listBranches(projectID: String) async throws -> PierBranchOptions {
        throw PierServiceFailure.unavailable("Not used in this test")
    }
    func createInstance(
        projectID: String,
        name: String,
        baseBranch: String,
        onProgress: @escaping @Sendable (PierCreationEvent) -> Void
    ) async throws -> PierInstance {
        throw PierServiceFailure.unavailable("Not used in this test")
    }
    func removeInstance(id: String) async throws {}
    func unparkInstance(id: String) async throws {
        requestedUnparkIDs.append(id)
        try await withCheckedThrowingContinuation { unparkContinuation = $0 }
    }
    func isWaitingForUnpark() -> Bool { unparkContinuation != nil }
    func completeUnpark() {
        unparkContinuation?.resume()
        unparkContinuation = nil
    }
    func unparkedIDs() -> [String] { requestedUnparkIDs }
    func inspectInstance(id: String) async throws -> PierInstanceSnapshot {
        guard let instance = instances.first(where: { $0.id == id }) else {
            throw PierServiceFailure.unavailable("Missing test instance")
        }
        return PierInstanceSnapshot(
            instance: instance,
            proxyHost: nil,
            tabs: tabs,
            ports: []
        )
    }
    func createTab(instanceID: String, name: String, command: [String]) async throws -> PierTab {
        throw PierServiceFailure.unavailable("Not used in this test")
    }
    func closeTab(instanceID: String, tabID: String) async throws {}
    func signInAWS() async throws { launchRequiresLogin = false }
    func signOutMobile() async throws { signedOut = true }
}

private actor ControlledCreationService: PierServicing {
    let project: PierProject
    let created: PierInstance
    private var continuation: CheckedContinuation<PierInstance, any Error>?
    private var inspectionContinuation: CheckedContinuation<PierInstanceSnapshot, any Error>?
    private var removedInstanceIDs: [String] = []

    init(project: PierProject, created: PierInstance) {
        self.project = project
        self.created = created
    }

    func setupStatus() async throws -> PierSetupStatus {
        PierSetupStatus(configured: true, configPath: "", cliVersion: "test", profiles: [], dependencies: [])
    }

    func listInstances() async throws -> [PierInstance] { [] }
    func listProjects() async throws -> [PierProject] { [project] }
    func addProject(path: String) async throws -> PierProject { project }

    func listBranches(projectID: String) async throws -> PierBranchOptions {
        PierBranchOptions(project: project, branches: ["origin/main"], defaultBranch: "origin/main", fetchWarning: nil)
    }

    func createInstance(
        projectID: String,
        name: String,
        baseBranch: String,
        onProgress: @escaping @Sendable (PierCreationEvent) -> Void
    ) async throws -> PierInstance {
        onProgress(.step("launched i-created"))
        self.onProgress = onProgress
        return try await withCheckedThrowingContinuation { continuation = $0 }
    }

    private var onProgress: (@Sendable (PierCreationEvent) -> Void)?

    func emitSetupProgress() {
        onProgress?(.setupStarted(".pier-setup.sh"))
        onProgress?(.setupOutput("make install"))
        onProgress?(.setupOutput("make build"))
        onProgress?(.setupFinished(name: ".pier-setup.sh", exitCode: 0))
    }

    func isWaitingForCompletion() -> Bool { continuation != nil }

    func removeInstance(id: String) {
        removedInstanceIDs.append(id)
    }

    func unparkInstance(id: String) {}

    func removedIDs() -> [String] { removedInstanceIDs }

    func complete() {
        continuation?.resume(returning: created)
        continuation = nil
    }

    func inspectInstance(id: String) async throws -> PierInstanceSnapshot {
        try await withCheckedThrowingContinuation { inspectionContinuation = $0 }
    }

    func isWaitingForInspection() -> Bool { inspectionContinuation != nil }

    func completeInspection(with snapshot: PierInstanceSnapshot) {
        inspectionContinuation?.resume(returning: snapshot)
        inspectionContinuation = nil
    }

    func createTab(instanceID: String, name: String, command: [String]) async throws -> PierTab {
        throw PierServiceFailure.unavailable("Not used in this test")
    }

    func closeTab(instanceID: String, tabID: String) async throws {}
    func signInAWS() async throws {}
}
