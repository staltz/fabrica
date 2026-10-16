import Foundation

/// The Fabrica MCP server, `fabrica mcp --http`, run on this Mac.
///
/// Local tools don't need it: they start their own `fabrica mcp` over
/// stdio. This is the server cloud runtimes talk to, for cloud factories,
/// which you can run here (behind a tunnel, say) instead of on a VM. It
/// serves what's committed in My Factories, and re-reads it every minute.
///
/// It runs as a child of the app, so quitting the app stops it.
@MainActor @Observable
final class Server {
    enum Health: Equatable {
        case stopped
        case starting
        /// Serving, at this home repo commit.
        case serving(commit: String)
        /// Up, but its latest read of the home repo failed.
        case unhealthy(String)
        case exited(Int32)
    }

    let process = LiveProcess()
    private(set) var health: Health = .stopped
    private var poller: Task<Void, Never>?
    private let cli: CLI
    private let home: HomeRepo

    /// The `server:` URL to serve factories of. Fabrica uses the secret
    /// `fabrica auth login <url>` saved for it.
    var serverURL: String {
        didSet { UserDefaults.standard.set(serverURL, forKey: "server.url") }
    }
    var listen: String {
        didSet { UserDefaults.standard.set(listen, forKey: "server.listen") }
    }
    var startsWithApp: Bool {
        didSet { UserDefaults.standard.set(startsWithApp, forKey: "server.autostart") }
    }

    init(cli: CLI, home: HomeRepo) {
        self.cli = cli
        self.home = home
        let d = UserDefaults.standard
        serverURL = d.string(forKey: "server.url") ?? ""
        listen = d.string(forKey: "server.listen") ?? "127.0.0.1:8080"
        startsWithApp = d.bool(forKey: "server.autostart")
        process.onExit = { [weak self] status in self?.exited(status) }
    }

    var isRunning: Bool { process.isRunning }

    static var dataFolder: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return base.appending(path: "Fabrica")
    }

    /// The server's run history. Remote runs never mix with your local ones.
    static var database: URL { dataFolder.appending(path: "server-runs.db") }

    var baseURL: URL {
        var hostPort = listen
        if hostPort.hasPrefix(":") { hostPort = "127.0.0.1" + hostPort }
        hostPort = hostPort.replacingOccurrences(of: "0.0.0.0", with: "127.0.0.1")
        return URL(string: "http://\(hostPort)") ?? URL(string: "http://127.0.0.1:8080")!
    }

    /// Where a factory's endpoint is on this server: the path of its
    /// `server:` URL plus its name, like the public one.
    func localEndpoint(for f: Factory) -> URL? {
        guard !f.server.isEmpty, let server = URL(string: f.server) else { return nil }
        let path = server.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        return baseURL.appending(path: path.isEmpty ? f.name : "\(path)/\(f.name)")
    }

    /// The factories this server serves.
    var servedFactories: [Factory] {
        home.entries.compactMap(\.factory).filter {
            $0.enabled && $0.isCloud && (serverURL.isEmpty || $0.server == serverURL)
        }
    }

    func start() {
        guard !process.isRunning else { return }
        if serverURL.isEmpty, let only = home.cloudServers.first, home.cloudServers.count == 1 {
            serverURL = only
        }
        try? FileManager.default.createDirectory(at: Self.dataFolder, withIntermediateDirectories: true)
        var args = ["mcp", "--http", "--home", home.url.path, "--listen", listen, "--db", Self.database.path]
        if !serverURL.isEmpty { args += ["--url", serverURL] }
        health = .starting
        process.start(cli.executable, args, env: cli.environment)
        poll()
    }

    func stop() {
        poller?.cancel()
        process.stop()
    }

    func restart() {
        guard process.isRunning else { return start() }
        let previous = process.onExit
        process.onExit = { [weak self] status in
            previous?(status)
            self?.process.onExit = previous
            self?.start()
        }
        stop()
    }

    func stopForQuit() {
        poller?.cancel()
        process.stopAndWait()
    }

    private func exited(_ status: Int32) {
        poller?.cancel()
        // SIGTERM from Stop is a normal way to end.
        health = (status == 0 || status == SIGTERM || status == 143) ? .stopped : .exited(status)
    }

    private func poll() {
        poller?.cancel()
        poller = Task { [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                await self.checkHealth()
                try? await Task.sleep(for: .seconds(self.health == .starting ? 1 : 10))
            }
        }
    }

    private func checkHealth() async {
        guard process.isRunning else { return }
        var request = URLRequest(url: baseURL.appending(path: "healthz"))
        request.timeoutInterval = 3
        guard let (data, response) = try? await URLSession.shared.data(for: request),
              let http = response as? HTTPURLResponse else {
            if case .serving = health { health = .unhealthy("It stopped answering.") }
            return
        }
        // {"home_commit": "9ab5e8f", "last_refresh": "…", "ok": true}, plus an error when not ok.
        let json = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
        let commit = json["home_commit"] as? String ?? ""
        let problem = (json["error"] as? String) ?? String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        if http.statusCode == 200, json["ok"] as? Bool ?? true {
            health = .serving(commit: commit)
        } else if http.statusCode == 503, health == .starting, commit.isEmpty {
            // The first read of the home repo isn't done yet.
        } else {
            health = .unhealthy(problem.isEmpty ? "It answered \(http.statusCode)." : problem)
        }
    }
}
