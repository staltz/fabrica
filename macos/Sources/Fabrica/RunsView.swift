import SwiftUI

/// Fabrica's run history, which the CLI has no command for yet: what your
/// agents did, newest first.
struct RunsView: View {
    @Environment(AppModel.self) private var model
    @State private var source: Source = .local
    @State private var runs: [Run] = []
    @State private var selection: Run.ID?
    @State private var loaded = false

    enum Source: Hashable { case local, server }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Picker("Source", selection: $source) {
                    Text("This Mac").tag(Source.local)
                    Text("Server").tag(Source.server)
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .fixedSize()
                .help("Runs from your tools on this Mac, or from cloud runtimes on this Mac's server")
                Spacer()
                if loaded && !runs.isEmpty {
                    Text("\(runs.count) latest run\(runs.count == 1 ? "" : "s")")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            Divider()
            if runs.isEmpty && loaded {
                ContentUnavailableView("No runs yet",
                                       systemImage: "clock",
                                       description: Text(source == .local
                                           ? "Runs show up here once an agent on this Mac starts one with Fabrica's MCP server."
                                           : "Runs show up here once a cloud runtime starts one on this Mac's server."))
            } else {
                Table(runs, selection: $selection) {
                    TableColumn("") { run in RunStatus(status: run.lastStatus) }
                        .width(18)
                    TableColumn("Run") { run in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(run.intent.isEmpty ? (run.routine ?? "—") : run.intent)
                                .lineLimit(1)
                            if let summary = run.lastSummary, !summary.isEmpty {
                                Text(summary).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                        .padding(.vertical, 3)
                        .help([run.intent, run.lastSummary ?? ""].filter { !$0.isEmpty }.joined(separator: "\n"))
                    }
                    .width(min: 220, ideal: 420)
                    TableColumn("Repo") { run in
                        Text(run.repoShort).foregroundStyle(.secondary)
                            .help("\(run.repo)\nFactory \(run.factory)" + (run.routine.map { ", routine \($0)" } ?? ""))
                    }
                    .width(min: 80, ideal: 150)
                    TableColumn("Tool") { run in
                        Text(Runtime.title(run.runtime)).foregroundStyle(.secondary).help(run.runtime)
                    }
                    .width(min: 70, ideal: 110)
                    TableColumn("When") { run in
                        Text(run.startedAt.map { $0.formatted(.relative(presentation: .named, unitsStyle: .abbreviated)) } ?? "")
                            .foregroundStyle(.secondary)
                            .monospacedDigit()
                            .frame(maxWidth: .infinity, alignment: .trailing)
                            .help(run.startedAt?.formatted(date: .abbreviated, time: .standard) ?? "")
                    }
                    .width(min: 60, ideal: 80, max: 110)
                }
                .alternatingRowBackgrounds(.disabled)
            }
        }
        .task(id: source) {
            // Keep it fresh while it's on screen.
            while !Task.isCancelled {
                let db = source == .local ? model.home.runsDB : Server.database
                runs = await RunHistory.latest(in: db)
                loaded = true
                try? await Task.sleep(for: .seconds(10))
            }
        }
    }
}

/// What the run's latest checkpoint says, as a glyph whose shape and color
/// both carry it.
private struct RunStatus: View {
    var status: String?

    var body: some View {
        let (symbol, color, label): (String, Color, String) = switch status {
        case "succeeded": ("checkmark.circle.fill", .green, "Succeeded")
        case "failed": ("xmark.circle.fill", .red, "Failed")
        case "progress": ("circle.lefthalf.filled", .blue, "In progress")
        default: ("circle", Color.secondary, "No report yet")
        }
        Image(systemName: symbol)
            .foregroundStyle(color)
            .help(label)
            .accessibilityLabel(label)
    }
}
