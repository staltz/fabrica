import Foundation

/// Your personal home repo, which the app calls "My Factories": a git repo
/// (~/.fabrica by default) with one file per factory in factories/.
///
/// Every save is committed right away, because `fabrica apply` refuses to
/// run with uncommitted factory changes, and the MCP server only serves
/// what's committed. Git keeps every version, so nothing the editor does
/// is hard to undo.
@MainActor @Observable
final class HomeRepo {
    struct Entry: Identifiable, Equatable {
        /// The file name, like exis.yaml or dogfood.off. Stable while the app runs.
        var id: String { file }
        var file: String
        var factory: Factory?
        /// Why the editor can't show this file, if it can't.
        var problem: String?
        var raw: String

        var name: String { factory?.name ?? (file as NSString).deletingPathExtension }
    }

    struct Diagnostic: Identifiable, Equatable {
        var id = UUID()
        var isError: Bool
        var code: String
        var whereText: String
        var message: String
        var hint: String?
    }

    /// What the last `fabrica plan` said, per factory.
    struct FactoryPlan: Equatable {
        var changes: Int
        var follows: Bool
        /// The blueprint commit the factory's ref points to now.
        var commit: String?
        var notes: [String]
    }

    enum PlanState: Equatable {
        case unknown, planning
        case done(exitCode: Int)
        case failed(String)
    }

    private(set) var entries: [Entry] = []
    private(set) var diagnostics: [Diagnostic] = []
    private(set) var plans: [String: FactoryPlan] = [:]
    private(set) var localChanges = 0
    private(set) var planState: PlanState = .unknown
    private(set) var lastPlannedAt: Date?
    /// Commits that aren't pushed yet, and commits on the remote you don't have.
    private(set) var ahead = 0
    private(set) var behind = 0
    private(set) var hasRemote = false
    private(set) var gitError: String?
    private(set) var syncing = false

    let cli: CLI

    init(cli: CLI) {
        self.cli = cli
    }

    /// Where the home repo is: FABRICA_HOME, or ~/.fabrica, like the CLI.
    private(set) var url = HomeRepo.location(ProcessInfo.processInfo.environment)

    /// Picks up a FABRICA_HOME set in your shell profile.
    func locate() {
        url = Self.location(ShellEnvironment.current)
    }

    private static func location(_ env: [String: String]) -> URL {
        if let custom = env["FABRICA_HOME"], !custom.isEmpty {
            return URL(fileURLWithPath: (custom as NSString).expandingTildeInPath)
        }
        return FileManager.default.homeDirectoryForCurrentUser.appending(path: ".fabrica")
    }

    var factoriesURL: URL { url.appending(path: "factories") }
    var runsDB: URL { url.appending(path: "runs.db") }

    var cloudServers: [String] {
        Array(Set(entries.compactMap { $0.factory }.filter { $0.enabled && $0.isCloud && !$0.server.isEmpty }.map(\.server))).sorted()
    }

    // MARK: Files

    func reload() {
        let fm = FileManager.default
        if !fm.fileExists(atPath: factoriesURL.path) {
            entries = []
            return
        }
        let files = (try? fm.contentsOfDirectory(atPath: factoriesURL.path)) ?? []
        entries = files
            .filter { [".yaml", ".yml", ".off"].contains(where: $0.hasSuffix) && !$0.hasPrefix(".") }
            .sorted()
            .map { file in
                let text = (try? String(contentsOf: factoriesURL.appending(path: file), encoding: .utf8)) ?? ""
                let name = (file as NSString).deletingPathExtension
                do {
                    let f = try Factory(name: name, enabled: !file.hasSuffix(".off"), yaml: text)
                    return Entry(file: file, factory: f, raw: text)
                } catch {
                    return Entry(file: file, factory: nil, problem: error.localizedDescription, raw: text)
                }
            }
    }

    /// Makes sure the home repo exists. Any fabrica command creates it.
    func ensureExists() async {
        if !FileManager.default.fileExists(atPath: factoriesURL.path) {
            _ = await cli.run(["version"])
        }
        reload()
    }

    private func fileName(for f: Factory, previous: String?) -> String {
        let ext = f.enabled ? (previous?.hasSuffix(".yml") == true ? "yml" : "yaml") : "off"
        return "\(f.name).\(ext)"
    }

    /// Saves a factory (renaming its file if the name or on/off changed),
    /// commits it, and re-plans. Returns the new file name.
    @discardableResult
    func save(_ f: Factory, replacing previous: String?) async throws -> String {
        let file = fileName(for: f, previous: previous)
        let fm = FileManager.default
        if file != previous, fm.fileExists(atPath: factoriesURL.appending(path: file).path) {
            throw FactoryParseError(message: "There's already a factory called \(f.name).")
        }
        try fm.createDirectory(at: factoriesURL, withIntermediateDirectories: true)
        try f.yaml.write(to: factoriesURL.appending(path: file), atomically: true, encoding: .utf8)
        if let previous, previous != file {
            try? fm.removeItem(at: factoriesURL.appending(path: previous))
        }
        let verb = previous == nil ? "Add" : (previous != file && !f.enabled ? "Turn off" : (previous?.hasSuffix(".off") == true && f.enabled ? "Turn on" : "Edit"))
        await commit("\(verb) factory \(f.name)")
        reload()
        Task { await plan() }
        return file
    }

    func delete(_ entry: Entry) async {
        try? FileManager.default.removeItem(at: factoriesURL.appending(path: entry.file))
        await commit("Remove factory \(entry.name)")
        reload()
        Task { await plan() }
    }

    func setEnabled(_ entry: Entry, _ on: Bool) async {
        guard var f = entry.factory, f.enabled != on else { return }
        f.enabled = on
        _ = try? await save(f, replacing: entry.file)
    }

    // MARK: Git

    private func git(_ args: [String]) async -> CommandResult {
        await runCommand(URL(fileURLWithPath: "/usr/bin/git"), ["-C", url.path] + args)
    }

    private func commit(_ message: String) async {
        _ = await git(["add", "-A", "--", "factories"])
        let staged = await git(["diff", "--cached", "--quiet", "--", "factories"])
        guard staged.status == 1 else { return }
        let result = await git(["commit", "-q", "-m", message, "--", "factories"])
        gitError = result.ok ? nil : result.output
        await refreshGit()
    }

    func refreshGit() async {
        let remotes = await git(["remote"])
        hasRemote = !remotes.stdout.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let counts = await git(["rev-list", "--left-right", "--count", "@{upstream}...HEAD"])
        if counts.ok {
            let parts = counts.stdout.split(whereSeparator: \.isWhitespace).compactMap { Int($0) }
            behind = parts.first ?? 0
            ahead = parts.count > 1 ? parts[1] : 0
        } else {
            // No upstream yet: the first sync sets one.
            behind = 0
            ahead = 0
        }
        let status = await git(["status", "--porcelain", "--", "factories"])
        localChanges = status.stdout.split(separator: "\n").count
    }

    /// Fetches, fast-forwards, and pushes. It never merges or rebases: if
    /// the remote and your factories have diverged, it says so and stops.
    func sync() async {
        guard hasRemote else { return }
        syncing = true
        defer { syncing = false }
        gitError = nil
        let pull = await git(["pull", "--ff-only", "-q"])
        if !pull.ok {
            gitError = "Couldn't bring in changes from the remote:\n" + pull.output
            await refreshGit()
            return
        }
        let upstream = await git(["rev-parse", "--abbrev-ref", "@{upstream}"])
        let push = upstream.ok ? await git(["push", "-q"]) : await git(["push", "-q", "-u", "origin", "HEAD"])
        if !push.ok { gitError = "Couldn't push:\n" + push.output }
        await refreshGit()
        reload()
    }

    // MARK: Plan

    /// Runs `fabrica plan --json` for the badge next to each factory.
    func plan() async {
        guard planState != .planning else { return }
        planState = .planning
        let result = await cli.run(["plan", "--json", "--no-refresh", "--home", url.path])
        lastPlannedAt = Date()
        guard let data = result.stdout.data(using: .utf8),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            planState = .failed(result.output.isEmpty ? "fabrica plan printed nothing" : result.output)
            return
        }
        diagnostics = ((json["diagnostics"] as? [[String: Any]]) ?? []).compactMap { d in
            let code = d["code"] as? String ?? ""
            let whereText = d["where"] as? String ?? ""
            // The editor's own "off" files, which Fabrica rightly ignores.
            if code == "home.stray-file", whereText.hasSuffix(".off") { return nil }
            return Diagnostic(isError: (d["severity"] as? Int ?? 0) == 0, code: code, whereText: whereText,
                              message: d["message"] as? String ?? "", hint: d["hint"] as? String)
        }
        var plans: [String: FactoryPlan] = [:]
        for f in (json["factories"] as? [[String: Any]]) ?? [] {
            guard let name = f["name"] as? String else { continue }
            plans[name] = FactoryPlan(changes: (f["changes"] as? [Any])?.count ?? 0,
                                      follows: f["follows"] as? Bool ?? false,
                                      commit: f["blueprint_commit"] as? String,
                                      notes: f["notes"] as? [String] ?? [])
        }
        let toolChanges = ((json["local"] as? [[String: Any]]) ?? []).reduce(0) { $0 + (($1["changes"] as? [Any])?.count ?? 0) }
        self.plans = plans
        self.localToolChanges = toolChanges
        planState = .done(exitCode: json["exit_code"] as? Int ?? Int(result.status))
    }

    /// Changes `apply` would make to your local tools (Claude Code, Cursor…).
    private(set) var localToolChanges = 0

    func diagnostics(for name: String) -> [Diagnostic] {
        diagnostics.filter { d in
            let w = d.whereText
            return w.hasPrefix("factories/\(name).") || w == name || w.hasPrefix("\(name) ") || w.hasPrefix("\(name):") || w.hasPrefix("\(name)/")
        }
    }

    enum Health { case unknown, inSync, changes, errors, off }

    func health(of entry: Entry) -> Health {
        guard let f = entry.factory else { return .errors }
        if !f.enabled { return .off }
        if diagnostics(for: f.name).contains(where: \.isError) { return .errors }
        guard let p = plans[f.name] else { return .unknown }
        return p.changes > 0 ? .changes : .inSync
    }

    var totalChanges: Int { plans.values.reduce(0) { $0 + $1.changes } + localToolChanges }
    var errorCount: Int { diagnostics.filter(\.isError).count }
}
