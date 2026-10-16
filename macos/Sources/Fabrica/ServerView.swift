import SwiftUI

/// Start and stop the Fabrica MCP server on this Mac, and see what it's doing.
struct ServerView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var server = model.server
        let home = model.home
        Form {
            Section {
                if server.servedFactories.isEmpty {
                    Text("No cloud factories use this server yet.")
                        .foregroundStyle(.secondary)
                }
                ForEach(server.servedFactories, id: \.name) { f in
                    VStack(alignment: .leading, spacing: 4) {
                        Text(f.name)
                        if let local = server.localEndpoint(for: f) {
                            CopyableURL(label: "Local", url: local.absoluteString)
                        }
                        if let endpoint = f.endpoint {
                            CopyableURL(label: "Public", url: endpoint)
                        }
                    }
                    .padding(.vertical, 2)
                }
            } header: {
                VStack(alignment: .leading, spacing: 0) {
                    header
                    Text("Endpoints")
                }
            } footer: {
                Footnote { Text("Each endpoint needs its factory's token, which apply gives the runtimes.") }
            }

            Section {
                Picker("Serve factories of", selection: $server.serverURL) {
                    ForEach(home.cloudServers, id: \.self) { Text($0).tag($0) }
                    if !server.serverURL.isEmpty && !home.cloudServers.contains(server.serverURL) {
                        Text(server.serverURL).tag(server.serverURL)
                    }
                    Divider()
                    Text("Every server").tag("")
                }
                TextField("Listen on", text: $server.listen, prompt: Text("127.0.0.1:8080"))
                Toggle("Start when Fabrica opens", isOn: $server.startsWithApp)
            } header: {
                Text("Settings")
            } footer: {
                Footnote {
                    Text(server.isRunning
                         ? "Changes take effect when the server restarts."
                         : "The secret comes from fabrica auth login for that server. Every server needs FABRICA_SERVER_SECRET instead.")
                }
            }

            if !server.process.output.isEmpty {
            Section {
                LogView(text: server.process.output, framed: false)
                    .frame(height: 260)
            } header: {
                HStack {
                    Text("Output")
                    Spacer()
                    if server.isRunning {
                        Button("Restart") { server.restart() }
                            .buttonStyle(.borderless)
                            .font(.callout)
                    }
                }
            }
            }
        }
        .formStyle(.grouped)
        .readableColumn()
    }

    private var header: some View {
        let server = model.server
        return PageHeader(symbol: "antenna.radiowaves.left.and.right") {
            Text("Server")
        } subtitle: {
            Text(subtitle)
                .textSelection(.enabled)
        } accessory: {
            if server.health != .stopped {
                StatusPill(text: statusText, color: statusColor, symbol: statusSymbol)
            }
            Toggle("Running", isOn: Binding {
                server.isRunning
            } set: { on in
                on ? server.start() : server.stop()
            })
            .toggleStyle(.switch)
            .labelsHidden()
            .controlSize(.small)
            .help(server.isRunning ? "Stop the server" : "Start the server")
        }
    }

    private var subtitle: String {
        let server = model.server
        let address = server.baseURL.host().map { "\($0):\(server.baseURL.port ?? 0)" } ?? server.listen
        switch server.health {
        case .stopped: return "Serves your cloud factories to cloud runtimes, from this Mac"
        case .starting: return "Reading your factories and their blueprints…"
        case .serving(let commit): return "Listening on \(address), serving My Factories at \(commit.prefix(7))"
        case .unhealthy(let why): return why
        case .exited: return "It stopped. The output below says why."
        }
    }

    private var statusText: String {
        switch model.server.health {
        case .stopped: "Off"
        case .starting: "Starting"
        case .serving: "Running"
        case .unhealthy: "Problem"
        case .exited(let code): "Exited \(code)"
        }
    }

    private var statusColor: Color {
        switch model.server.health {
        case .serving: .green
        case .starting: .orange
        case .unhealthy, .exited: .red
        case .stopped: .secondary
        }
    }

    private var statusSymbol: String {
        switch model.server.health {
        case .serving: "circle.fill"
        case .starting: "circle.dotted"
        case .unhealthy, .exited: "exclamationmark.triangle.fill"
        case .stopped: "circle"
        }
    }
}

private struct CopyableURL: View {
    var label: String
    var url: String
    @State private var copied = false

    var body: some View {
        HStack(spacing: 8) {
            Text(label)
                .font(.caption)
                .foregroundStyle(.tertiary)
                .frame(width: 40, alignment: .leading)
            Text(url)
                .font(.callout.monospaced())
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: 0)
            Button {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(url, forType: .string)
                copied = true
                Task { try? await Task.sleep(for: .seconds(1.2)); copied = false }
            } label: {
                Image(systemName: copied ? "checkmark" : "doc.on.doc")
                    .contentTransition(.symbolEffect(.replace))
                    .frame(width: 16)
            }
            .buttonStyle(.borderless)
            .help("Copy")
        }
    }
}
