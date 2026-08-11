#if os(macOS)
import Darwin
import Foundation

struct PierCLIService: PierServicing {
    private let decoder: JSONDecoder
    private let portProxy: PierPortProxyManager

    init() {
        decoder = JSONDecoder()
        portProxy = PierPortProxyManager()
    }

    func setupStatus() async throws -> PierSetupStatus {
        try await request(PierSetupStatus.self, arguments: ["app", "status"])
    }

    func listInstances() async throws -> [PierInstance] {
        try await request([PierInstance].self, arguments: ["app", "list"])
    }

    func listProjects() async throws -> [PierProject] {
        try await request([PierProject].self, arguments: ["app", "projects"])
    }

    func addProject(path: String) async throws -> PierProject {
        try await request(PierProject.self, arguments: ["app", "project-add", path])
    }

    func listBranches(projectID: String) async throws -> PierBranchOptions {
        try await request(PierBranchOptions.self, arguments: ["app", "branches", projectID])
    }

    func createInstance(
        projectID: String,
        name: String,
        baseBranch: String,
        onProgress: @escaping @Sendable (PierCreationEvent) -> Void
    ) async throws -> PierInstance {
        try await request(
            PierInstance.self,
            arguments: ["app", "instance-new", projectID, name, baseBranch],
            onDiagnostic: { line in
                onProgress(PierCLICreationEventParser.parse(line))
            }
        )
    }

    func removeInstance(id: String) async throws {
        _ = try await request(EmptyResponse.self, arguments: ["app", "instance-remove", id])
    }

    func unparkInstance(id: String) async throws {
        _ = try await request(EmptyResponse.self, arguments: ["app", "instance-unpark", id])
    }

    func inspectInstance(id: String) async throws -> PierInstanceSnapshot {
        try await request(PierInstanceSnapshot.self, arguments: ["app", "inspect", id])
    }

    func preparePortProxy() async throws {
        let network = try await request(
            PierProxyNetworkStatus.self,
            arguments: ["app", "proxy-network-status"]
        )
        guard !network.needsSetup else { return }
        try await portProxy.start(
            executable: PierCLIExecutable.locate(),
            environment: PierProcessEnvironment.runtime()
        )
    }

    func openPort(proxyHost host: String, port: PierPort) async throws -> PierPortEndpoint {
        if let url = await portProxy.reachableURL(
            host: host,
            remotePort: port.number,
            scheme: port.scheme ?? "http"
        ) {
            return PierPortEndpoint(
                address: "\(host):\(port.number)",
                browserURL: port.isHTTP ? url : nil
            )
        }

        let network = try await request(
            PierProxyNetworkStatus.self,
            arguments: ["app", "proxy-network-status"]
        )
        let url = try await portProxy.open(
            executable: PierCLIExecutable.locate(),
            environment: PierProcessEnvironment.runtime(),
            host: host,
            remotePort: port.number,
            scheme: port.scheme ?? "http",
            needsNetworkSetup: network.needsSetup
        )
        return PierPortEndpoint(
            address: "\(host):\(port.number)",
            browserURL: port.isHTTP ? url : nil
        )
    }

    func createTab(instanceID: String, name: String, command: [String]) async throws -> PierTab {
        try await request(PierTab.self, arguments: ["app", "tab-new", instanceID, "--name", name, "--"] + command)
    }

    func closeTab(instanceID: String, tabID: String) async throws {
        _ = try await request(EmptyResponse.self, arguments: ["app", "tab-close", instanceID, tabID])
    }

    func signInAWS() async throws {
        _ = try await request(EmptyResponse.self, arguments: ["app", "login"])
    }

    private func request<Value: Decodable & Sendable>(
        _ type: Value.Type,
        arguments: [String],
        onDiagnostic: (@Sendable (String) -> Void)? = nil
    ) async throws -> Value {
        let result = try await PierCLIProcess.run(arguments: arguments, onDiagnostic: onDiagnostic)
        let envelope: PierAPIEnvelope<Value>
        do {
            envelope = try decoder.decode(PierAPIEnvelope<Value>.self, from: result.output)
        } catch {
            let raw = String(decoding: result.output, as: UTF8.self)
            let diagnostics = String(decoding: result.diagnostics, as: UTF8.self)
            throw PierServiceFailure.invalidResponse(
                "Pier returned invalid JSON: \(raw)\(diagnostics.isEmpty ? "" : "\n\(diagnostics)")"
            )
        }
        if let error = envelope.error {
            throw error
        }
        guard result.status == 0, let data = envelope.data else {
            let diagnostics = String(decoding: result.diagnostics, as: UTF8.self)
            throw PierServiceFailure.commandFailed(
                diagnostics.isEmpty ? "Pier exited with status \(result.status)." : diagnostics
            )
        }
        return data
    }
}

enum PierCLICreationEventParser {
    private static let prefix = "PIER_CREATE_EVENT "

    private struct WireEvent: Decodable {
        let kind: String
        let message: String
        let exitCode: Int?
    }

    static func parse(_ line: String) -> PierCreationEvent {
        guard line.hasPrefix(prefix) else { return .step(line) }
        let payload = String(line.dropFirst(prefix.count))
        guard let data = payload.data(using: .utf8),
              let event = try? JSONDecoder().decode(WireEvent.self, from: data) else {
            return .step(line)
        }
        switch event.kind {
        case "setup_started":
            return .setupStarted(event.message)
        case "setup_output":
            return .setupOutput(event.message)
        case "setup_finished":
            return .setupFinished(name: event.message, exitCode: event.exitCode ?? 0)
        default:
            return .step(event.message)
        }
    }
}

private struct PierProxyNetworkStatus: Decodable, Sendable {
    let needsSetup: Bool
}

private actor PierPortProxyManager {
    private var process: Process?
    private var logHandle: FileHandle?
    private var logURL: URL?

    deinit {
        process?.terminate()
        try? logHandle?.close()
    }

    func reachableURL(host: String, remotePort: Int, scheme: String) -> URL? {
        guard canConnect(host: host, port: remotePort) else { return nil }
        return try? proxyURL(scheme: scheme, host: host, port: remotePort)
    }

    func open(
        executable: URL,
        environment: [String: String],
        host: String,
        remotePort: Int,
        scheme: String,
        needsNetworkSetup: Bool
    ) async throws -> URL {
        if canConnect(host: host, port: remotePort) {
            return try proxyURL(scheme: scheme, host: host, port: remotePort)
        }

        if needsNetworkSetup {
            try authorizeNetworkSetup(executable: executable)
        }
        try start(executable: executable, environment: environment)

        // The proxy deliberately allows up to 45 seconds for an SSH master
        // (a newly resumed instance can take ~20 seconds), then reconciles and
        // retries. Keep the native wait comfortably above that lifecycle.
        let clock = ContinuousClock()
        let deadline = clock.now.advanced(by: .seconds(120))
        while clock.now < deadline {
            if canConnect(host: host, port: remotePort) {
                return try proxyURL(scheme: scheme, host: host, port: remotePort)
            }
            if process?.isRunning == false && !resolves(host: host) {
                process = nil
                throw PierServiceFailure.commandFailed(proxyFailure(
                    fallback: "Pier’s .pier proxy could not start."
                ))
            }
            try await Task.sleep(for: .milliseconds(250))
        }

        throw PierServiceFailure.commandFailed(proxyFailure(
            fallback: "Timed out waiting for \(host):\(remotePort)."
        ))
    }

    func start(executable: URL, environment: [String: String]) throws {
        guard process?.isRunning != true else { return }
        try? logHandle?.close()
        let proxy = Process()
        let (logURL, logHandle) = try makeProxyLog()
        proxy.executableURL = executable
        proxy.arguments = ["proxy"]
        proxy.environment = environment
        proxy.standardInput = FileHandle.nullDevice
        proxy.standardOutput = logHandle
        proxy.standardError = logHandle
        try proxy.run()
        process = proxy
        self.logURL = logURL
        self.logHandle = logHandle
    }

    private func makeProxyLog() throws -> (URL, FileHandle) {
        let directory = FileManager.default.homeDirectoryForCurrentUser
            .appending(path: "Library/Logs/Pier", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let url = directory.appending(path: "proxy.log")
        try Data().write(to: url, options: .atomic)
        return (url, try FileHandle(forWritingTo: url))
    }

    private func proxyFailure(fallback: String) -> String {
        guard let logURL,
              let data = try? Data(contentsOf: logURL),
              let decoded = String(data: data, encoding: .utf8) else {
            return fallback
        }
        let output = decoded.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !output.isEmpty else { return fallback }
        let tail = String(output.suffix(2_000))
        return "\(fallback)\n\nProxy output:\n\(tail)"
    }

    private func authorizeNetworkSetup(executable: URL) throws {
        let shellCommand = shellQuote(executable.path) + " app proxy-network-setup"
        let script = "do shell script \(appleScriptString(shellCommand)) with administrator privileges"
        let authorization = Process()
        let errors = Pipe()
        authorization.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        authorization.arguments = ["-e", script]
        authorization.standardOutput = FileHandle.nullDevice
        authorization.standardError = errors
        try authorization.run()
        authorization.waitUntilExit()
        guard authorization.terminationStatus == 0 else {
            let detail = String(
                decoding: errors.fileHandleForReading.readDataToEndOfFile(),
                as: UTF8.self
            ).trimmingCharacters(in: .whitespacesAndNewlines)
            throw PierServiceFailure.commandFailed(
                detail.isEmpty ? "Pier needs administrator permission to configure .pier domains." : detail
            )
        }
    }

    private func proxyURL(scheme: String, host: String, port: Int) throws -> URL {
        guard let url = URL(string: "\(scheme)://\(host):\(port)/") else {
            throw PierServiceFailure.invalidResponse("Pier produced an invalid .pier URL.")
        }
        return url
    }

    private func resolves(host: String) -> Bool {
        var result: UnsafeMutablePointer<addrinfo>?
        let status = Darwin.getaddrinfo(host, nil, nil, &result)
        if let result { Darwin.freeaddrinfo(result) }
        return status == 0
    }

    private func canConnect(host: String, port: Int) -> Bool {
        var hints = addrinfo()
        hints.ai_family = AF_UNSPEC
        hints.ai_socktype = SOCK_STREAM
        hints.ai_flags = AI_NUMERICSERV
        var result: UnsafeMutablePointer<addrinfo>?
        guard Darwin.getaddrinfo(host, String(port), &hints, &result) == 0 else { return false }
        defer { if let result { Darwin.freeaddrinfo(result) } }

        var current = result
        while let info = current?.pointee {
            let descriptor = Darwin.socket(info.ai_family, info.ai_socktype, info.ai_protocol)
            if descriptor >= 0 {
                let connected = Darwin.connect(descriptor, info.ai_addr, info.ai_addrlen) == 0
                Darwin.close(descriptor)
                if connected { return true }
            }
            current = info.ai_next
        }
        return false
    }

    private func shellQuote(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }

    private func appleScriptString(_ value: String) -> String {
        "\"" + value
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"") + "\""
    }
}

private struct EmptyResponse: Decodable, Sendable {}

private enum PierCLIProcess {
    struct Result: Sendable {
        let status: Int32
        let output: Data
        let diagnostics: Data
    }

    static func run(
        arguments: [String],
        onDiagnostic: (@Sendable (String) -> Void)? = nil
    ) async throws -> Result {
        let executable = try locateExecutable()
        return try await Task.detached(priority: .userInitiated) {
            let process = Process()
            let output = Pipe()
            let diagnostics = Pipe()
            process.executableURL = executable
            process.arguments = arguments
            process.environment = PierProcessEnvironment.runtime()
            process.standardOutput = output
            process.standardError = diagnostics
            try process.run()
            async let outputData = Task.detached {
                output.fileHandleForReading.readDataToEndOfFile()
            }.value
            async let diagnosticData = Task.detached {
                let handle = diagnostics.fileHandleForReading
                var allData = Data()
                var pending = Data()
                while true {
                    let chunk = handle.availableData
                    guard !chunk.isEmpty else { break }
                    allData.append(chunk)
                    pending.append(chunk)
                    while let newline = pending.firstIndex(of: 0x0A) {
                        let line = String(decoding: pending[..<newline], as: UTF8.self)
                        pending.removeSubrange(...newline)
                        if let line = progressLine(line) {
                            onDiagnostic?(line)
                        }
                    }
                }
                if let line = progressLine(String(decoding: pending, as: UTF8.self)) {
                    onDiagnostic?(line)
                }
                return allData
            }.value
            process.waitUntilExit()
            return await Result(
                status: process.terminationStatus,
                output: outputData,
                diagnostics: diagnosticData
            )
        }.value
    }

    private static func progressLine(_ raw: String) -> String? {
        let withoutANSI = raw.replacingOccurrences(
            of: "\u{001B}\\[[0-?]*[ -/]*[@-~]",
            with: "",
            options: .regularExpression
        )
        var line = withoutANSI.trimmingCharacters(in: .whitespacesAndNewlines)
        if line.hasPrefix("▸") {
            line.removeFirst()
            line = line.trimmingCharacters(in: .whitespaces)
        }
        return line.isEmpty ? nil : line
    }

    private static func locateExecutable() throws -> URL { try PierCLIExecutable.locate() }
}

enum PierProcessEnvironment {
    private static let requiredPaths = [
        "/opt/homebrew/bin",
        "/usr/local/bin",
        "/usr/bin",
        "/bin",
        "/usr/sbin",
        "/sbin",
    ]

    static func runtime(from base: [String: String] = ProcessInfo.processInfo.environment) -> [String: String] {
        var environment = base
        var paths = environment["PATH"]?.split(separator: ":").map(String.init) ?? []
        for path in requiredPaths where !paths.contains(path) {
            paths.append(path)
        }
        environment["PATH"] = paths.joined(separator: ":")
        return environment
    }

    static func terminal() -> [String] {
        var environment = runtime()
        environment["TERM"] = "xterm-256color"
        environment["COLORTERM"] = "truecolor"
        if environment["LANG"]?.isEmpty != false {
            environment["LANG"] = "en_US.UTF-8"
        }
        return environment
            .map { "\($0.key)=\($0.value)" }
            .sorted()
    }
}

enum PierCLIExecutable {
    static func locate() throws -> URL {
        if let override = ProcessInfo.processInfo.environment["PIER_CLI_PATH"], !override.isEmpty {
            return URL(fileURLWithPath: override)
        }
        if let bundled = Bundle.main.url(forResource: "pier", withExtension: nil) {
            return bundled
        }
        throw PierServiceFailure.unavailable("The bundled Pier CLI is missing from the application.")
    }
}
#endif
