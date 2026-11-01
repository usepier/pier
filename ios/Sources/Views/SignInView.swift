import SafariServices
import SwiftUI

/// Values to start the form with, for development (`make ios` passes them
/// from ios/.env.local).
private enum SignInPreset {
    private static let environment = ProcessInfo.processInfo.environment

    static let startURL = environment["PIER_IOS_START_URL"] ?? ""
    static let ssoRegion = environment["PIER_IOS_SSO_REGION"] ?? "us-east-1"
    static let awsRegion = environment["PIER_IOS_AWS_REGION"] ?? "us-east-1"
    static let accountID = environment["PIER_IOS_ACCOUNT_ID"] ?? ""
    static let roleName = environment["PIER_IOS_ROLE_NAME"] ?? ""
}

/// Signing in with IAM Identity Center: the portal, a code approved in the
/// browser, then the account and permission set pier works as — the ones
/// the Mac's pier uses, so the same sessions show.
struct SignInView: View {
    @Environment(AppModel.self) private var model

    @AppStorage(SignInDefaults.startURLKey) private var startURL = SignInPreset.startURL
    @AppStorage(SignInDefaults.ssoRegionKey) private var ssoRegion = SignInPreset.ssoRegion
    @AppStorage(SignInDefaults.awsRegionKey) private var awsRegion = SignInPreset.awsRegion
    @State private var accountID = ""
    @State private var roleName = ""
    @State private var showsSettings = false
    @State private var browser: BrowserPage?

    private var canSignIn: Bool {
        URL(string: startURL)?.scheme == "https" && !ssoRegion.isEmpty && !awsRegion.isEmpty
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    HStack(spacing: 14) {
                        PierMark(size: 40)
                        Text("pier").font(.largeTitle.bold())
                    }
                    Text(
                        model.reconnecting
                            ? "Your AWS sign-in expired. Sign in again — the account and permission set stay picked."
                            : "Your pier sessions, from your phone. Sign in with the AWS access portal you use for pier on your Mac."
                    )
                    .foregroundStyle(.secondary)
                }

                Section {
                    TextField("Start URL", text: $startURL, prompt: Text(verbatim: "https://your-company.awsapps.com/start"))
                        .textInputAutocapitalization(.never)
                        .keyboardType(.URL)
                        .autocorrectionDisabled()
                    LabeledContent("Identity Center") {
                        TextField("Identity Center region", text: $ssoRegion, prompt: Text(verbatim: "us-east-1"))
                            .multilineTextAlignment(.trailing)
                    }
                    LabeledContent("Sessions") {
                        TextField("Sessions' region", text: $awsRegion, prompt: Text(verbatim: "eu-central-1"))
                            .multilineTextAlignment(.trailing)
                    }
                    Button(model.reconnecting ? "Sign In Again" : "Sign In with AWS") { signIn() }
                        .disabled(!canSignIn || model.authorizing)
                } header: {
                    Text("AWS access portal")
                } footer: {
                    Text("On your Mac, ~/.aws/config has the portal's start URL and region (sso_start_url, sso_region); the sessions' region is aws.region in ~/.config/pier/config.toml.")
                }
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()

                if let authorization = model.authorization, model.accounts.isEmpty {
                    Section("Approve this phone") {
                        LabeledContent("Code", value: authorization.userCode)
                            .textSelection(.enabled)
                        if model.authorizing {
                            HStack {
                                ProgressView()
                                Text("Waiting for the approval…").foregroundStyle(.secondary)
                            }
                        }
                        Button("Open the Approval Page") { open(authorization) }
                    }
                }

                if !model.accounts.isEmpty {
                    Section("AWS account") {
                        Picker("Account", selection: $accountID) {
                            ForEach(model.accounts) { account in
                                Text(account.name.isEmpty ? account.id : "\(account.name) · \(account.id)").tag(account.id)
                            }
                        }
                        .onChange(of: accountID) { _, id in
                            roleName = ""
                            Task {
                                await model.loadRoles(accountID: id)
                                roleName = preferredRole()
                            }
                        }
                        if model.loadingRoles {
                            ProgressView()
                        } else if !model.roles.isEmpty {
                            Picker("Permission set", selection: $roleName) {
                                ForEach(model.roles) { Text($0.name).tag($0.name) }
                            }
                        }
                        Button("Done") {
                            Task { await model.finishSetup(accountID: accountID, roleName: roleName) }
                        }
                        .buttonStyle(.borderedProminent)
                        .disabled(accountID.isEmpty || roleName.isEmpty || model.loadingRoles)
                    }
                }
            }
            .pierScrollSurface()
            .navigationTitle(model.reconnecting ? "Sign In Again" : "Sign In")
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button { showsSettings = true } label: { Label("Settings", systemImage: "gearshape") }
                }
            }
            .sheet(isPresented: $showsSettings) {
                NavigationStack { SettingsView() }
            }
            .sheet(item: $browser) { page in
                Browser(url: page.url) { browser = nil }.ignoresSafeArea()
            }
        }
    }

    private func signIn() {
        Task {
            let request = SignInRequest(
                startURL: startURL.trimmingCharacters(in: .whitespacesAndNewlines),
                ssoRegion: ssoRegion.trimmingCharacters(in: .whitespacesAndNewlines),
                awsRegion: awsRegion.trimmingCharacters(in: .whitespacesAndNewlines)
            )
            guard let authorization = await model.beginSignIn(request) else { return }
            open(authorization)
            let done = await model.completeSignIn()
            browser = nil
            guard !done else { return }
            accountID = model.accounts.contains { $0.id == SignInPreset.accountID }
                ? SignInPreset.accountID : model.accounts.first?.id ?? ""
            if !accountID.isEmpty {
                await model.loadRoles(accountID: accountID)
                roleName = preferredRole()
            }
        }
    }

    private func open(_ authorization: DeviceAuthorization) {
        if let url = URL(string: authorization.verificationURL) { browser = BrowserPage(url: url) }
    }

    private func preferredRole() -> String {
        model.roles.contains { $0.name == SignInPreset.roleName } ? SignInPreset.roleName : model.roles.first?.name ?? ""
    }
}

private struct BrowserPage: Identifiable {
    let url: URL
    var id: String { url.absoluteString }
}

/// The approval page, in the app.
private struct Browser: UIViewControllerRepresentable {
    let url: URL
    let onDone: @MainActor () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onDone: onDone) }

    func makeUIViewController(context: Context) -> SFSafariViewController {
        let controller = SFSafariViewController(url: url)
        controller.dismissButtonStyle = .done
        controller.delegate = context.coordinator
        return controller
    }

    func updateUIViewController(_ controller: SFSafariViewController, context: Context) {}

    @MainActor
    final class Coordinator: NSObject, @MainActor SFSafariViewControllerDelegate {
        private let onDone: @MainActor () -> Void
        init(onDone: @escaping @MainActor () -> Void) { self.onDone = onDone }
        func safariViewControllerDidFinish(_ controller: SFSafariViewController) { onDone() }
    }
}
