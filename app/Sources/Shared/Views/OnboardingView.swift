import SwiftUI

struct OnboardingView: View {
    @Environment(PierAppModel.self) private var model

    var body: some View {
        #if os(iOS)
        MobileOnboardingView()
        #else
        ScrollView {
            VStack(alignment: .leading, spacing: 28) {
                VStack(alignment: .leading, spacing: 10) {
                    Image(systemName: "sailboat.fill")
                        .font(.system(size: 44))
                        .foregroundStyle(.tint)
                    Text("Set up Pier")
                        .font(.largeTitle.bold())
                    Text("Pier creates isolated development instances in your AWS account. Credentials stay on this device.")
                        .font(.title3)
                        .foregroundStyle(.secondary)
                }

                if let status = model.setupStatus {
                    VStack(alignment: .leading, spacing: 14) {
                        Label("Device readiness", systemImage: "checklist")
                            .font(.headline)

                        ForEach(status.dependencies) { dependency in
                            HStack(alignment: .firstTextBaseline) {
                                Image(systemName: dependency.available ? "checkmark.circle.fill" : "xmark.circle.fill")
                                    .foregroundStyle(dependency.available ? .green : .orange)
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(dependency.name)
                                    Text(dependency.detail)
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                            }
                        }

                        LabeledContent("Pier", value: status.cliVersion)
                        LabeledContent("Configuration", value: status.configPath)
                    }
                    .padding()
                    .background(.quaternary.opacity(0.35), in: RoundedRectangle(cornerRadius: 16))
                }

                VStack(alignment: .leading, spacing: 8) {
                    Text("AWS onboarding")
                        .font(.headline)
                    Text("The native flow will select an AWS identity, create the Pier role and security group, import optional agent credentials, and finish with the doctor checks.")
                        .foregroundStyle(.secondary)
                }

                HStack {
                    Button("Check again") {
                        Task { await model.refreshSetup() }
                    }
                    .buttonStyle(.bordered)

                    Button("Continue to instances") {
                        Task { await model.continueWithoutSetup() }
                    }
                    .buttonStyle(.borderedProminent)
                }
            }
            .frame(maxWidth: 620, alignment: .leading)
            .padding(32)
        }
        #endif
    }
}

#if os(iOS)
private enum MobileOnboardingDefaults {
    private static let environment = ProcessInfo.processInfo.environment

    static let startURL = environment["PIER_IOS_START_URL"] ?? ""
    static let ssoRegion = environment["PIER_IOS_SSO_REGION"] ?? "us-east-1"
    static let awsRegion = environment["PIER_IOS_AWS_REGION"] ?? "us-east-1"
    static let accountID = environment["PIER_IOS_ACCOUNT_ID"] ?? ""
    static let roleName = environment["PIER_IOS_ROLE_NAME"] ?? ""
}

private struct MobileOnboardingView: View {
    @Environment(PierAppModel.self) private var model
    @Environment(\.openURL) private var openURL

    @State private var startURL = MobileOnboardingDefaults.startURL
    @State private var ssoRegion = MobileOnboardingDefaults.ssoRegion
    @State private var awsRegion = MobileOnboardingDefaults.awsRegion
    @State private var accountID = ""
    @State private var roleName = ""
    @State private var showsSettings = false

    private var canSignIn: Bool {
        URL(string: startURL)?.scheme == "https" && !ssoRegion.isEmpty && !awsRegion.isEmpty
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Label("Pier", systemImage: "sailboat.fill")
                        .font(.largeTitle.bold())
                    Text("Connect Pier to AWS IAM Identity Center. Tokens are stored in this device's Keychain.")
                        .foregroundStyle(.secondary)
                }

                Section("AWS access portal") {
                    TextField("Start URL", text: $startURL, prompt: Text("https://your-company.awsapps.com/start"))
                        .textInputAutocapitalization(.never)
                        .keyboardType(.URL)
                        .autocorrectionDisabled()
                    TextField("Identity Center region", text: $ssoRegion, prompt: Text("us-east-1"))
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    TextField("Pier instances region", text: $awsRegion, prompt: Text("us-east-1"))
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()

                    Button("Sign in with AWS") {
                        signIn()
                    }
                    .disabled(!canSignIn || model.isAuthorizingMobile)
                }

                if let authorization = model.mobileAuthorization, model.mobileAccounts.isEmpty {
                    Section("Authorize this device") {
                        LabeledContent("Code", value: authorization.userCode)
                            .textSelection(.enabled)
                        if model.isAuthorizingMobile {
                            HStack {
                                ProgressView()
                                Text("Waiting for AWS authorization…")
                                    .foregroundStyle(.secondary)
                            }
                        }
                        Button("Open AWS again") {
                            if let url = URL(string: authorization.verificationURL) {
                                openURL(url)
                            }
                        }
                    }
                }

                if !model.mobileAccounts.isEmpty {
                    Section("AWS account") {
                        Picker("Account", selection: $accountID) {
                            ForEach(model.mobileAccounts) { account in
                                Text(account.name.isEmpty ? account.id : "\(account.name) · \(account.id)")
                                    .tag(account.id)
                            }
                        }
                        .onChange(of: accountID) { _, value in
                            roleName = ""
                            Task {
                                await model.loadMobileRoles(accountID: value)
                                roleName = preferredRoleName()
                            }
                        }

                        if model.isLoading {
                            ProgressView("Loading roles…")
                        } else if !model.mobileRoles.isEmpty {
                            Picker("Permission set", selection: $roleName) {
                                ForEach(model.mobileRoles) { role in
                                    Text(role.name).tag(role.name)
                                }
                            }
                        }

                        Button("Finish setup") {
                            Task { _ = await model.finishMobileSetup(accountID: accountID, roleName: roleName) }
                        }
                        .buttonStyle(.borderedProminent)
                        .disabled(accountID.isEmpty || roleName.isEmpty || model.isLoading)
                    }
                }
            }
            .pierScrollSurface()
            .navigationTitle("Set up Pier")
            .toolbar {
                ToolbarItem(placement: .primaryAction) {
                    Button {
                        showsSettings = true
                    } label: {
                        Label("Settings", systemImage: "gearshape")
                    }
                }
            }
            .sheet(isPresented: $showsSettings) {
                NavigationStack {
                    PierSettingsView(showsDoneButton: true)
                }
            }
        }
    }

    private func signIn() {
        Task {
            let request = PierMobileSignInRequest(
                startURL: startURL.trimmingCharacters(in: .whitespacesAndNewlines),
                ssoRegion: ssoRegion.trimmingCharacters(in: .whitespacesAndNewlines),
                awsRegion: awsRegion.trimmingCharacters(in: .whitespacesAndNewlines)
            )
            guard let authorization = await model.beginMobileSignIn(request),
                  let url = URL(string: authorization.verificationURL) else { return }
            openURL(url)
            await model.completeMobileSignIn()
            accountID = preferredAccountID()
            if !accountID.isEmpty {
                await model.loadMobileRoles(accountID: accountID)
                roleName = preferredRoleName()
            }
        }
    }

    private func preferredAccountID() -> String {
        if model.mobileAccounts.contains(where: { $0.id == MobileOnboardingDefaults.accountID }) {
            return MobileOnboardingDefaults.accountID
        }
        return model.mobileAccounts.first?.id ?? ""
    }

    private func preferredRoleName() -> String {
        if model.mobileRoles.contains(where: { $0.name == MobileOnboardingDefaults.roleName }) {
            return MobileOnboardingDefaults.roleName
        }
        return model.mobileRoles.first?.name ?? ""
    }
}
#endif
