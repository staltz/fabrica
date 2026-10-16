import SwiftUI

struct SettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var opensAtLogin = false

    var body: some View {
        @Bindable var model = model
        let cli = model.cli
        Form {
            Section {
                Toggle("Show in the menu bar", isOn: $model.showInMenuBar)
                Toggle("Show in the Dock", isOn: $model.showInDock)
                Toggle("Open at login", isOn: Binding {
                    opensAtLogin
                } set: {
                    model.opensAtLogin = $0
                    opensAtLogin = model.opensAtLogin
                })
            } header: {
                HStack(spacing: 12) {
                    MarkView(size: 26)
                    VStack(alignment: .leading, spacing: 1) {
                        Text("Fabrica").font(.headline)
                        Text(versionText).font(.caption).foregroundStyle(.secondary)
                    }
                }
                .padding(.bottom, 10)
                .textCase(nil)
                .foregroundStyle(.primary)
            }

            Section {
                LabeledContent("fabrica") {
                    StatusPill(text: installText, color: installColor, symbol: installSymbol)
                }
                switch cli.install {
                case .separate:
                    Button("Use the App's fabrica") { Task { await cli.replaceSeparate() } }
                case .missing:
                    Button("Install") { Task { await cli.setUp() } }
                case .linked where !cli.onPath:
                    Button("Add ~/.local/bin to PATH") { Task { await cli.addToPath() } }
                default:
                    EmptyView()
                }
                if let error = cli.lastError {
                    Text(error).foregroundStyle(.red).font(.caption)
                }
            } header: {
                Text("Command line tool")
            } footer: {
                Footnote { Text("~/.local/bin/fabrica links to the app's own copy, so updating the app updates the CLI.") }
            }

            Section {
                LabeledContent("Home repo") {
                    HStack(spacing: 6) {
                        Text(abbreviatedPath(model.home.url))
                            .font(.callout.monospaced())
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .help(model.home.url.path(percentEncoded: false))
                        Button {
                            NSWorkspace.shared.activateFileViewerSelecting([model.home.url])
                        } label: {
                            Image(systemName: "folder")
                        }
                        .buttonStyle(.borderless)
                        .help("Show in Finder")
                    }
                }
            } header: {
                Text("My Factories")
            } footer: {
                Footnote { Text("Set FABRICA_HOME in your shell profile to keep it somewhere else.") }
            }
        }
        .formStyle(.grouped)
        .frame(width: 460)
        .fixedSize(horizontal: false, vertical: true)
        .onAppear { opensAtLogin = model.opensAtLogin }
    }

    private var versionText: String {
        let app = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
        let cli = model.cli.version.replacingOccurrences(of: "fabrica ", with: "")
        return [app.map { "Version \($0)" }, cli.isEmpty ? nil : "CLI \(cli)"].compactMap { $0 }.joined(separator: " · ")
    }

    private var installText: String {
        switch model.cli.install {
        case .linked: model.cli.onPath ? "Installed" : "Not on PATH"
        case .missing: "Not installed"
        case .separate: "Separate copy"
        case .notBundled: "Not bundled"
        }
    }

    private var installColor: Color {
        switch model.cli.install {
        case .linked: model.cli.onPath ? .green : .orange
        case .separate: .orange
        case .missing, .notBundled: .secondary
        }
    }

    private var installSymbol: String {
        switch model.cli.install {
        case .linked: model.cli.onPath ? "checkmark.circle.fill" : "exclamationmark.triangle.fill"
        case .separate: "exclamationmark.triangle.fill"
        case .missing, .notBundled: "circle"
        }
    }
}
