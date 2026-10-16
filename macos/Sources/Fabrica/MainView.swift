import SwiftUI

/// The main window: your factories down the side, and whatever you picked
/// on the right.
struct MainView: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        NavigationSplitView {
            Sidebar()
                .navigationSplitViewColumnWidth(min: 210, ideal: 240, max: 320)
        } detail: {
            // Each page has its own header, so the toolbar keeps an empty
            // title, which also keeps the actions at its trailing edge.
            detail
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .navigationTitle("")
                .background(WindowsMenuTitle(title: pageTitle))
        }
        .toolbar {
            ToolbarItemGroup(placement: .primaryAction) {
                PlanButtons()
            }
        }
        .sheet(isPresented: $model.showingCommand) {
            CommandSheet()
        }
        .onAppear {
            UserDefaults.standard.set(true, forKey: "ui.launched")
            selectFirstIfNeeded()
        }
        .onChange(of: model.home.entries) { selectFirstIfNeeded() }
    }

    /// An empty detail is a wasted screen: open the first factory instead.
    private func selectFirstIfNeeded() {
        if model.selection == nil, let first = model.home.entries.first {
            model.selection = .factory(first.file)
        }
    }

    /// The window's name in the Window menu and Mission Control. The toolbar
    /// doesn't show it, since each page has its own header.
    private var pageTitle: String {
        switch model.selection {
        case .factory(let file): model.home.entries.first { $0.file == file }?.name ?? "My Factories"
        case .newFactory: "New Factory"
        case .server: "Server"
        case .runs: "Runs"
        case nil: "My Factories"
        }
    }

    @ViewBuilder
    private var detail: some View {
        switch model.selection {
        case .factory(let file):
            if let entry = model.home.entries.first(where: { $0.file == file }) {
                FactoryEditor(entry: entry)
                    .id(file)
            } else {
                EmptyState()
            }
        case .newFactory:
            FactoryEditor(entry: nil)
                .id("new")
        case .server:
            ServerView()
        case .runs:
            RunsView()
        case nil:
            EmptyState()
        }
    }
}

// MARK: - Sidebar

private struct Sidebar: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        let home = model.home
        List(selection: $model.selection) {
            Section("My Factories") {
                ForEach(home.entries) { entry in
                    FactoryRow(entry: entry)
                        .tag(SidebarItem.factory(entry.file))
                        .contextMenu {
                            if let f = entry.factory {
                                Button(f.enabled ? "Turn Off" : "Turn On") {
                                    Task { await home.setEnabled(entry, !f.enabled) }
                                }
                            }
                            Button("Show in Finder") {
                                NSWorkspace.shared.activateFileViewerSelecting([home.factoriesURL.appending(path: entry.file)])
                            }
                        }
                }
                if model.selection == .newFactory {
                    Label("New Factory", systemImage: "plus")
                        .tag(SidebarItem.newFactory)
                }
            }
            Section("This Mac") {
                ServerRow()
                    .tag(SidebarItem.server)
                Label("Runs", systemImage: "clock")
                    .tag(SidebarItem.runs)
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            SidebarFooter()
        }
    }
}

private struct FactoryRow: View {
    @Environment(AppModel.self) private var model
    var entry: HomeRepo.Entry

    var body: some View {
        let health = model.home.health(of: entry)
        let f = entry.factory
        HStack(spacing: 8) {
            Label {
                VStack(alignment: .leading, spacing: 1) {
                    Text(entry.name)
                    Text(subtitle)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            } icon: {
                Image(systemName: f?.isCloud == true ? "cloud" : "laptopcomputer")
            }
            .opacity(health == .off ? 0.5 : 1)
            Spacer(minLength: 4)
            if health == .changes || health == .errors {
                Image(systemName: health.symbol)
                    .font(.caption)
                    .foregroundStyle(health.color)
                    .help(health.label)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityValue(health.label)
    }

    /// Which blueprint it runs: the part of a factory you recognize it by.
    private var subtitle: String {
        guard let f = entry.factory else { return "Can't read this file" }
        if !f.enabled { return "Off" }
        var name = f.blueprint.git.split(separator: "/").last.map(String.init) ?? f.blueprint.git
        for prefix in ["fabrica-blueprint-", "fabrica-"] where name.hasPrefix(prefix) && name.count > prefix.count {
            name = String(name.dropFirst(prefix.count))
            break
        }
        return "\(name) · \(f.blueprint.ref)"
    }
}

private struct ServerRow: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        HStack {
            Label("Server", systemImage: "antenna.radiowaves.left.and.right")
            Spacer()
            if model.server.isRunning {
                Circle()
                    .fill(.green)
                    .frame(width: 7, height: 7)
                    .accessibilityLabel("Running")
            }
        }
    }
}

/// The bottom of the sidebar: add a factory, push your changes, and the
/// one CLI notice worth knowing about.
private struct SidebarFooter: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let home = model.home
        VStack(alignment: .leading, spacing: 0) {
            CLINotice()
            if let error = home.gitError {
                Text(error)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .lineLimit(3)
                    .textSelection(.enabled)
                    .padding(.horizontal, 12)
                    .padding(.bottom, 6)
            }
            Divider()
            HStack(spacing: 8) {
                Button {
                    model.selection = .newFactory
                } label: {
                    Image(systemName: "plus")
                        .frame(width: 22, height: 22)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.borderless)
                .help("New Factory (⌘N)")
                .accessibilityLabel("New Factory")

                Spacer()

                if home.hasRemote {
                    Button {
                        Task { await home.sync() }
                    } label: {
                        HStack(spacing: 4) {
                            if home.syncing {
                                ProgressView().controlSize(.mini)
                            } else {
                                Image(systemName: home.ahead > 0 ? "arrow.up.circle" : "checkmark.circle")
                            }
                            Text(syncText)
                        }
                        .font(.caption)
                        .foregroundStyle(home.ahead > 0 || home.behind > 0 ? AnyShapeStyle(.tint) : AnyShapeStyle(.secondary))
                    }
                    .buttonStyle(.borderless)
                    .disabled(home.syncing)
                    .help("Brings in changes from your home repo's remote, and pushes yours. It never merges.")
                } else {
                    Text("Only on this Mac")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .help("Your factories aren't pushed anywhere. Add a remote to your home repo to back them up.")
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 7)
        }
    }

    private var syncText: String {
        let home = model.home
        if home.syncing { return "Syncing" }
        if home.ahead > 0 { return "Push \(home.ahead)" }
        if home.behind > 0 { return "Pull \(home.behind)" }
        return "Up to date"
    }
}

/// The CLI's install state, when it needs your attention: small, at the
/// bottom of the sidebar, never in the way.
private struct CLINotice: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let cli = model.cli
        switch cli.install {
        case .separate:
            notice("Another fabrica CLI is in ~/.local/bin", action: "Use the App's") {
                Task { await cli.replaceSeparate() }
            }
            .help("The app runs its own fabrica, which may be a different version. This moves the other one to the Trash, and links ~/.local/bin/fabrica to the app's.")
        case .linked where !cli.onPath:
            notice("~/.local/bin isn't on your PATH", action: "Add") {
                Task { await cli.addToPath() }
            }
        default:
            EmptyView()
        }
    }

    private func notice(_ text: String, action: String, perform: @escaping () -> Void) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Label(text, systemImage: "terminal")
                .font(.caption)
                .foregroundStyle(.secondary)
            Button(action, action: perform)
                .buttonStyle(.link)
                .font(.caption)
                .padding(.leading, 20)
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .padding(.horizontal, 10)
        .padding(.bottom, 8)
    }
}

// MARK: - Toolbar

private struct PlanButtons: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let home = model.home
        Button("Plan") { model.showPlan() }
            .help("Shows what apply would change (⇧⌘P)")

        if home.totalChanges > 0 {
            Button("Apply \(home.totalChanges)") { model.apply() }
                .buttonStyle(.borderedProminent)
                .help("Makes your tools and runtimes match your factories (⇧⌘A)")
        } else {
            Button("Apply") { model.apply() }
                .help("Makes your tools and runtimes match your factories (⇧⌘A)")
        }
    }
}

// MARK: - Empty state

private struct EmptyState: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        VStack(spacing: 14) {
            MarkView(size: 40)
                .padding(.bottom, 6)
            Text("No factories yet")
                .font(.title2.weight(.semibold))
            Text("A factory runs a blueprint on your repos,\nin the tools you pick.")
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            Button("New Factory") { model.selection = .newFactory }
                .buttonStyle(.borderedProminent)
                .controlSize(.large)
                .padding(.top, 4)
            Text(abbreviatedPath(model.home.url))
                .font(.caption.monospaced())
                .foregroundStyle(.tertiary)
                .padding(.top, 8)
        }
        .padding(40)
    }
}

// MARK: - Plan and apply

/// The output of a plan or apply, as it's printed.
struct CommandSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss

    private var isPlan: Bool { model.commandTitle == "Plan" }

    var body: some View {
        let command = model.command
        VStack(alignment: .leading, spacing: 14) {
            HStack(alignment: .firstTextBaseline) {
                Text(model.commandTitle)
                    .font(.title3.weight(.semibold))
                Spacer()
                if command.isRunning {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small)
                        Text(isPlan ? "Planning…" : "Applying…").foregroundStyle(.secondary)
                    }
                    .font(.callout)
                } else if let status = command.exitStatus {
                    let (text, color, symbol) = outcome(status)
                    StatusPill(text: text, color: color, symbol: symbol)
                }
            }
            LogView(text: command.output, placeholder: "Starting…")
            HStack {
                if isPlan, command.exitStatus == 2, model.home.ahead > 0 {
                    Label("Push your changes first: apply only uses what's on the remote.", systemImage: "info.circle")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if command.isRunning {
                    Button("Stop") { command.stop() }
                }
                if isPlan, command.exitStatus == 2 {
                    Button("Apply") { model.apply() }
                }
                Button("Done") { dismiss() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(command.isRunning)
            }
        }
        .padding(20)
        .frame(minWidth: 680, idealWidth: 780, minHeight: 440, idealHeight: 560)
    }

    private func outcome(_ status: Int32) -> (String, Color, String) {
        if isPlan {
            switch status {
            case 0: return ("In sync", .green, "checkmark.circle.fill")
            case 2: return ("Changes to apply", .orange, "arrow.triangle.2.circlepath.circle.fill")
            default: return ("Needs attention", .red, "exclamationmark.triangle.fill")
            }
        }
        return status == 0 ? ("Done", .green, "checkmark.circle.fill") : ("Failed", .red, "xmark.octagon.fill")
    }
}

/// Names the window in the Window menu. Its toolbar shows no title, since
/// each page has its own header (and an empty title keeps the toolbar's
/// actions at its trailing edge).
private struct WindowsMenuTitle: NSViewRepresentable {
    var title: String

    func makeNSView(context: Context) -> NSView { NSView() }

    func updateNSView(_ view: NSView, context: Context) {
        DispatchQueue.main.async {
            guard let window = view.window else { return }
            NSApp.changeWindowsItem(window, title: title, filename: false)
            window.setAccessibilityTitle(title)
        }
    }
}
