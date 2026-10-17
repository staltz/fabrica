import AppKit
import Foundation

/// The links `fabrica dashboard` prints.
enum DashboardLink {
    /// Every link in a dashboard's output, in the order it printed them:
    /// `http://127.0.0.1:<port>/_dashboard?code=<hex>`. Only whole lines
    /// count, so a link that's still arriving isn't taken for a short one.
    static func all(in output: String) -> [URL] {
        guard let end = output.lastIndex(where: \.isNewline) else { return [] }
        let text = String(output[..<end])
        let pattern = #"http://127\.0\.0\.1:[0-9]+/_dashboard\?code=[0-9a-f]+(?![0-9A-Za-z])"#
        guard let regex = try? NSRegularExpression(pattern: pattern) else { return [] }
        return regex.matches(in: text, range: NSRange(text.startIndex..., in: text)).compactMap { match in
            Range(match.range, in: text).flatMap { URL(string: String(text[$0])) }
        }
    }
}

/// The run-history dashboard in your browser: `fabrica dashboard`, once for
/// each source of runs, as a child of the app (like the server) that stops
/// when the app quits.
///
/// The dashboard stays locked to anyone without one of its one-time links,
/// and the app only launches it: it asks for a link, and the browser trades
/// the link's code for its own cookie. A dashboard that's already running
/// is asked for a fresh link with SIGUSR1, so every tab you open stays in.
@MainActor @Observable
final class Dashboard {
    private let cli: CLI
    private let home: HomeRepo
    private let localProcess = LiveProcess()
    private let serverProcess = LiveProcess()
    /// The sources whose link we're waiting for.
    private(set) var opening: Set<RunSource> = []

    /// How long to wait for a link before saying something's wrong.
    private let patience: TimeInterval = 5

    init(cli: CLI, home: HomeRepo) {
        self.cli = cli
        self.home = home
    }

    private func process(for source: RunSource) -> LiveProcess {
        switch source {
        case .local: localProcess
        case .server: serverProcess
        }
    }

    /// Opens the dashboard of a source in your browser, starting it if it
    /// isn't running.
    func open(_ source: RunSource) async {
        guard !opening.contains(source) else { return }
        opening.insert(source)
        defer { opening.remove(source) }

        let live = process(for: source)
        if !live.isRunning {
            // Its output starts empty, so any link in it is new.
            live.start(cli.executable,
                       ["dashboard", "--home", home.url.path, "--db", source.database(in: home).path],
                       env: cli.environment)
        }
        let seen = Set(DashboardLink.all(in: live.output))
        // One that's up has printed its first link, and gives a fresh one when
        // asked. One that's still starting prints its first link by itself.
        if live.isRunning && !seen.isEmpty { live.signal(SIGUSR1) }

        let deadline = Date().addingTimeInterval(patience)
        var gone = false
        while Date() < deadline {
            if let link = DashboardLink.all(in: live.output).last(where: { !seen.contains($0) }) {
                if !NSWorkspace.shared.open(link) {
                    problem("The dashboard is ready, but macOS couldn't open its link in a browser.", output: "")
                }
                return
            }
            if gone { break }
            try? await Task.sleep(for: .milliseconds(100))
            // Looked at after waiting: a process that has exited and what it
            // printed reach us separately, and its last words need a moment.
            gone = !live.isRunning
        }
        problem(gone ? "The dashboard stopped." : "The dashboard didn't give a link in time.", output: live.output)
    }

    /// Stops the dashboards when the app quits.
    func stopForQuit() {
        localProcess.stopAndWait()
        serverProcess.stopAndWait()
    }

    private func problem(_ message: String, output: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Couldn't open the dashboard"
        let said = String(output.trimmingCharacters(in: .whitespacesAndNewlines).suffix(1000))
        alert.informativeText = said.isEmpty ? message : "\(message)\n\n\(said)"
        NSApp.activate()
        alert.runModal()
    }
}
