import SwiftUI

/// The menu bar menu: the server switch, your factories, and the usual
/// way out. Status is shown by symbol shape, since menus draw symbols in
/// one color.
struct MenuContent: View {
    @Environment(AppModel.self) private var model
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        let server = model.server
        let home = model.home

        Section(serverLine) {
            if server.isRunning {
                Button("Stop Server", systemImage: "stop.circle") { server.stop() }
            } else {
                Button("Start Server", systemImage: "play.circle") { server.start() }
            }
        }

        Divider()

        Section("My Factories") {
            ForEach(home.entries) { entry in
                let health = home.health(of: entry)
                Button(entry.name, systemImage: health.symbol) { open(.factory(entry.file)) }
            }
            Button("New Factory…", systemImage: "plus") { open(.newFactory) }
        }

        Divider()

        Button(planLine, systemImage: "list.bullet.rectangle") {
            bringForward(openWindow)
            model.showPlan()
        }
        if home.hasRemote, home.ahead > 0 {
            Button("Push \(home.ahead) Change\(home.ahead == 1 ? "" : "s")", systemImage: "arrow.up.circle") {
                Task { await home.sync() }
            }
        }
        Button("Runs…", systemImage: "clock") { open(.runs) }
        Button("Dashboard…", systemImage: "chart.bar.xaxis") {
            Task { await model.dashboard.open(.local) }
        }

        Divider()

        Button("Open Fabrica", systemImage: "macwindow") { bringForward(openWindow) }
        SettingsLink { Label("Settings…", systemImage: "gearshape") }
            .keyboardShortcut(",")
        Button("Quit Fabrica", systemImage: "power") { NSApp.terminate(nil) }
            .keyboardShortcut("q")
    }

    private func open(_ item: SidebarItem) {
        model.selection = item
        bringForward(openWindow)
    }

    private var serverLine: String {
        let url = model.server.baseURL
        switch model.server.health {
        case .stopped: return "Server Off"
        case .starting: return "Server Starting…"
        case .serving: return "Server On · \(url.host() ?? ""):\(url.port ?? 0)"
        case .unhealthy: return "Server On · Needs Attention"
        case .exited(let code): return "Server Stopped · Exit \(code)"
        }
    }

    private var planLine: String {
        let home = model.home
        switch home.planState {
        case .planning: return "Plan…  Checking"
        case .done where home.errorCount > 0: return "Plan…  \(home.errorCount) to fix"
        case .done where home.totalChanges > 0: return "Plan…  \(home.totalChanges) to apply"
        case .done: return "Plan…  In sync"
        default: return "Plan…"
        }
    }
}
