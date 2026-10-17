import AppKit
import SwiftUI

@main
struct FabricaApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @State private var model = AppModel.shared

    var body: some Scene {
        Window("My Factories", id: "main") {
            MainView()
                .environment(model)
                .frame(minWidth: 760, minHeight: 480)
        }
        .defaultSize(width: 1000, height: 680)
        .defaultLaunchBehavior(model.showInDock || model.isFirstLaunch ? .presented : .suppressed)
        .commands {
            CommandGroup(replacing: .appInfo) {
                AboutButtons(updater: model.updater)
            }
            CommandGroup(replacing: .newItem) {
                Button("New Factory") { model.selection = .newFactory }
                    .keyboardShortcut("n")
            }
            CommandMenu("Factories") {
                Button("Plan") { model.showPlan() }
                    .keyboardShortcut("p", modifiers: [.command, .shift])
                Button("Apply") { model.apply() }
                    .keyboardShortcut("a", modifiers: [.command, .shift])
                Divider()
                Button("Push Changes") { Task { await model.home.sync() } }
                    .disabled(!model.home.hasRemote)
            }
        }

        MenuBarExtra(isInserted: Bindable(model).showInMenuBar) {
            MenuContent()
                .environment(model)
        } label: {
            Image(nsImage: Mark.menuBarImage(serverRunning: model.server.isRunning))
                .accessibilityLabel(model.server.isRunning ? "Fabrica, server running" : "Fabrica")
        }

        Settings {
            SettingsView()
                .environment(model)
        }

        Window("About Fabrica", id: "about") {
            AboutView()
                .environment(model)
        }
        .windowStyle(.hiddenTitleBar)
        .windowResizability(.contentSize)
        .windowMinimizeBehavior(.disabled)
        .restorationBehavior(.disabled)
        .defaultLaunchBehavior(.suppressed)
    }
}

/// About Fabrica and Check for Updates…, in the app menu. Both open the
/// About window, which shows the version and how updates are going.
private struct AboutButtons: View {
    var updater: Updater
    @Environment(\.openWindow) private var openWindow

    var body: some View {
        Button("About Fabrica") { bringForward(openWindow, id: "about") }
        Button("Check for Updates…") {
            bringForward(openWindow, id: "about")
            Task { await updater.check() }
        }
        .disabled(updater.current == nil)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        MainActor.assumeIsolated {
            let model = AppModel.shared
            model.applyActivationPolicy()
            Task { await model.start() }
        }
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    func applicationDidBecomeActive(_ notification: Notification) {
        MainActor.assumeIsolated {
            Task { await AppModel.shared.refresh() }
            return
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        MainActor.assumeIsolated {
            AppModel.shared.server.stopForQuit()
            AppModel.shared.dashboard.stopForQuit()
        }
    }
}

/// Opens a window, the main one unless you say, and brings the app
/// forward, which an app without a Dock icon has to do by hand.
@MainActor
func bringForward(_ openWindow: OpenWindowAction, id: String = "main") {
    openWindow(id: id)
    NSApp.activate()
}
