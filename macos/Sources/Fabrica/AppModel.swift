import AppKit
import ServiceManagement
import SwiftUI

enum SidebarItem: Hashable {
    /// A factory, by its file name.
    case factory(String)
    case newFactory
    case server
    case runs
}

/// Everything the app knows, shared by the menu bar and the window.
@MainActor @Observable
final class AppModel {
    static let shared = AppModel()

    let cli = CLI()
    let home: HomeRepo
    let server: Server
    let updater = Updater()
    /// The plan or apply shown in the command sheet.
    let command = LiveProcess()
    var commandTitle = ""
    var showingCommand = false

    var selection: SidebarItem? = nil

    var showInMenuBar: Bool {
        didSet {
            UserDefaults.standard.set(showInMenuBar, forKey: "ui.menubar")
            if !showInMenuBar && !showInDock { showInDock = true }
        }
    }
    var showInDock: Bool {
        didSet {
            UserDefaults.standard.set(showInDock, forKey: "ui.dock")
            if !showInMenuBar && !showInDock { showInMenuBar = true }
            applyActivationPolicy()
        }
    }

    private var started = false

    init() {
        home = HomeRepo(cli: cli)
        server = Server(cli: cli, home: home)
        let d = UserDefaults.standard
        showInMenuBar = d.object(forKey: "ui.menubar") as? Bool ?? true
        showInDock = d.object(forKey: "ui.dock") as? Bool ?? false
    }

    var isFirstLaunch: Bool { UserDefaults.standard.object(forKey: "ui.launched") == nil }

    func start() async {
        guard !started else { return }
        started = true
        applyActivationPolicy()
        updater.schedule()
        await ShellEnvironment.prepare()
        home.locate()
        await cli.setUp()
        await home.ensureExists()
        await home.refreshGit()
        // An update relaunches the app with the server running again if it was.
        let resume = UserDefaults.standard.bool(forKey: "server.resume")
        UserDefaults.standard.removeObject(forKey: "server.resume")
        if server.startsWithApp || resume { server.start() }
        await home.plan()
    }

    /// Re-reads what may have changed while you were in another app.
    func refresh() async {
        home.reload()
        await home.refreshGit()
    }

    func applyActivationPolicy() {
        NSApp?.setActivationPolicy(showInDock ? .regular : .accessory)
    }

    // MARK: Commands

    /// Shows `fabrica plan` in the command sheet.
    func showPlan() {
        run(title: "Plan", ["plan", "--home", home.url.path])
    }

    /// Runs `fabrica apply` in the command sheet.
    func apply() {
        run(title: "Apply", ["apply", "--home", home.url.path]) { [weak self] in
            Task { await self?.home.refreshGit(); await self?.home.plan() }
        }
    }

    func run(title: String, _ args: [String], then: (() -> Void)? = nil) {
        guard !command.isRunning else { return }
        commandTitle = title
        command.onExit = { _ in then?() }
        command.start(cli.executable, args, cwd: home.url, env: cli.environment)
        showingCommand = true
    }

    // MARK: Updates

    /// Installs the available update and relaunches into it.
    func installUpdate() {
        Task {
            let serving = server.isRunning
            guard await updater.install() else { return }
            UserDefaults.standard.set(serving, forKey: "server.resume")
            updater.relaunch()
        }
    }

    // MARK: Login item

    var opensAtLogin: Bool {
        get { SMAppService.mainApp.status == .enabled }
        set {
            do {
                if newValue { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            } catch {
                NSAlert(error: error).runModal()
            }
        }
    }
}
