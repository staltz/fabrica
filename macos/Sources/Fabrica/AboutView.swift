import SwiftUI

/// The About window: which Fabrica this is, and whether there's a newer one.
struct AboutView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let updater = model.updater
        let cli = model.cli.version.replacingOccurrences(of: "fabrica ", with: "")
        VStack(spacing: 0) {
            MarkView(size: 56)
                .padding(.bottom, 14)
            Text("Fabrica")
                .font(.title.weight(.semibold))
            Text(updater.current.map { "Version \($0)" } ?? "Development build")
                .font(.callout)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
                .padding(.top, 2)
            if !cli.isEmpty {
                Text("CLI \(cli)")
                    .font(.caption.monospaced())
                    .foregroundStyle(.tertiary)
                    .textSelection(.enabled)
                    .padding(.top, 3)
            }

            UpdateRow()
                .padding(12)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .padding(.top, 22)

            HStack(spacing: 18) {
                Link("Release Notes", destination: updater.available?.page ?? updater.current.map { Release.page("v\($0)") } ?? Release.repo.appending(path: "releases"))
                Link("GitHub", destination: Release.repo)
            }
            .font(.callout)
            .padding(.top, 16)
        }
        .padding(.horizontal, 28)
        .padding(.top, 34)
        .padding(.bottom, 24)
        .frame(width: 400)
    }
}

/// Where updates are at, with the button for the next step: check, update
/// and relaunch, or download it yourself when the app can't update itself.
struct UpdateRow: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let updater = model.updater
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 10) {
                if updater.isChecking || updater.installing != nil {
                    ProgressView().controlSize(.small)
                } else {
                    Image(systemName: symbol)
                        .foregroundStyle(color)
                }
                Text(text)
                    .font(.callout)
                    .foregroundStyle(updater.problem == nil ? Color.primary : Color.red)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                button
            }
            if updater.available != nil, updater.installing == nil, let reason = updater.cantInstall {
                Text(reason)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    @ViewBuilder
    private var button: some View {
        let updater = model.updater
        if updater.isChecking || updater.installing != nil || updater.current == nil {
            EmptyView()
        } else if let release = updater.available {
            if updater.cantInstall == nil {
                Button("Update and Relaunch") { model.installUpdate() }
                    .buttonStyle(.borderedProminent)
                    .help("Downloads Fabrica \(release.version), checks it, puts it in place of this app, and opens it.")
            } else {
                Link("Download", destination: release.download)
            }
        } else {
            Button("Check Now") { Task { await updater.check() } }
        }
    }

    private var text: String {
        let updater = model.updater
        if let step = updater.installing { return step }
        if updater.isChecking { return "Checking for updates…" }
        if let problem = updater.problem { return problem }
        if let release = updater.available { return "Fabrica \(release.version) is available." }
        if updater.current == nil { return "This build doesn't update itself." }
        if updater.lastChecked != nil { return "Fabrica is up to date." }
        return "Fabrica looks for updates on GitHub."
    }

    private var symbol: String {
        let updater = model.updater
        if updater.problem != nil { return "exclamationmark.triangle.fill" }
        if updater.available != nil { return "arrow.down.circle.fill" }
        if updater.lastChecked != nil { return "checkmark.circle.fill" }
        return "arrow.triangle.2.circlepath"
    }

    private var color: Color {
        let updater = model.updater
        if updater.problem != nil { return .red }
        if updater.available != nil { return .accentColor }
        if updater.lastChecked != nil { return .green }
        return .secondary
    }
}
