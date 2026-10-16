import Foundation

/// What a finished command printed, and how it exited.
struct CommandResult: Sendable {
    var status: Int32
    var stdout: String
    var stderr: String

    var ok: Bool { status == 0 }
    /// Both streams, the way a terminal would show them (roughly).
    var output: String {
        [stdout, stderr].filter { !$0.isEmpty }.joined(separator: stdout.hasSuffix("\n") ? "" : "\n")
    }
}

/// The environment commands run in. Apps started from Finder get a bare
/// PATH, so we ask the login shell for the one you have in Terminal. That
/// way fabrica finds git, claude, gh and the rest just like it does there.
enum ShellEnvironment {
    nonisolated(unsafe) private static var cached: [String: String]?
    private static let lock = NSLock()

    /// The environment, once `prepare()` has asked the login shell. Until
    /// then, on the main thread, the app's own environment (with the usual
    /// folders added): the UI never waits for a shell.
    static var current: [String: String] {
        lock.lock()
        defer { lock.unlock() }
        if let cached { return cached }
        if Thread.isMainThread { return padded(ProcessInfo.processInfo.environment) }
        let env = load()
        cached = env
        return env
    }

    /// Asks the login shell for its environment, off the main thread.
    static func prepare() async {
        await Task.detached { _ = current }.value
    }

    private static func load() -> [String: String] {
        var env = ProcessInfo.processInfo.environment
        let shell = env["SHELL"].flatMap { $0.isEmpty ? nil : $0 } ?? "/bin/zsh"
        let marker = "__FABRICA_ENV__"
        // -i too, since many people set PATH in .zshrc rather than .zprofile.
        let script = "printf '\(marker)%s\(marker)' \"$PATH\"; printf '\(marker)%s\(marker)' \"$FABRICA_HOME\""
        if let out = runSync(URL(fileURLWithPath: shell), ["-ilc", script], env: env, timeout: 5) {
            let parts = out.components(separatedBy: marker)
            // ["…junk", PATH, "", FABRICA_HOME, "…"]
            if parts.count >= 4 {
                if !parts[1].isEmpty {
                    env["PATH"] = parts[1]
                    loginPath = parts[1].split(separator: ":").map(String.init)
                }
                if !parts[3].isEmpty { env["FABRICA_HOME"] = parts[3] }
            }
        }
        return padded(env)
    }

    private static func padded(_ env: [String: String]) -> [String: String] {
        var env = env
        let localBin = FileManager.default.homeDirectoryForCurrentUser.appending(path: ".local/bin").path
        var path = (env["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin").split(separator: ":").map(String.init)
        for extra in [localBin, "/opt/homebrew/bin", "/usr/local/bin"] where !path.contains(extra) {
            path.append(extra)
        }
        env["PATH"] = path.joined(separator: ":")
        return env
    }

    /// Whether the login shell's own PATH (before we padded it) has a folder.
    static func loginPathContains(_ folder: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return loginPath.contains { $0 == folder || $0 == folder + "/" }
    }

    nonisolated(unsafe) private static var loginPath: [String] = []

    private static func runSync(_ url: URL, _ args: [String], env: [String: String], timeout: TimeInterval) -> String? {
        let p = Process()
        p.executableURL = url
        p.arguments = args
        p.environment = env
        p.standardInput = FileHandle.nullDevice
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        let exited = DispatchSemaphore(value: 0)
        p.terminationHandler = { _ in exited.signal() }
        do { try p.run() } catch { return nil }
        let deadline = DispatchTime.now() + timeout
        let done = DispatchSemaphore(value: 0)
        let box = DataBox()
        DispatchQueue.global().async {
            box.data = out.fileHandleForReading.readDataToEndOfFile()
            done.signal()
        }
        if done.wait(timeout: deadline) == .timedOut {
            p.terminate()
            return nil
        }
        // Not waitUntilExit, which spins the run loop it's called on.
        _ = exited.wait(timeout: .now() + 1)
        return String(decoding: box.data, as: UTF8.self)
    }
}

private final class DataBox: @unchecked Sendable {
    var data = Data()
}

/// Runs a command to completion, off the main thread.
func runCommand(_ executable: URL, _ args: [String], cwd: URL? = nil, env extra: [String: String] = [:]) async -> CommandResult {
    await withCheckedContinuation { cont in
        DispatchQueue.global(qos: .userInitiated).async {
            let p = Process()
            p.executableURL = executable
            p.arguments = args
            p.environment = ShellEnvironment.current.merging(extra) { $1 }
            if let cwd { p.currentDirectoryURL = cwd }
            p.standardInput = FileHandle.nullDevice
            let out = Pipe(), err = Pipe()
            p.standardOutput = out
            p.standardError = err
            do {
                try p.run()
            } catch {
                cont.resume(returning: CommandResult(status: -1, stdout: "", stderr: error.localizedDescription))
                return
            }
            let o = DataBox(), e = DataBox()
            let group = DispatchGroup()
            group.enter()
            DispatchQueue.global().async {
                o.data = out.fileHandleForReading.readDataToEndOfFile()
                group.leave()
            }
            e.data = err.fileHandleForReading.readDataToEndOfFile()
            group.wait()
            p.waitUntilExit()
            cont.resume(returning: CommandResult(
                status: p.terminationStatus,
                stdout: String(decoding: o.data, as: UTF8.self),
                stderr: String(decoding: e.data, as: UTF8.self)))
        }
    }
}

/// A long-running command whose output shows up as it's printed: the MCP
/// server, or an apply.
@MainActor @Observable
final class LiveProcess {
    private(set) var output = ""
    private(set) var isRunning = false
    private(set) var exitStatus: Int32?
    private(set) var startedAt: Date?
    private var process: Process?
    /// Called on the main actor when the process exits.
    var onExit: ((Int32) -> Void)?

    /// How much output to keep. The server can run for weeks.
    private let limit = 256 * 1024

    func start(_ executable: URL, _ args: [String], cwd: URL? = nil, env extra: [String: String] = [:]) {
        guard !isRunning else { return }
        output = ""
        exitStatus = nil
        let p = Process()
        p.executableURL = executable
        p.arguments = args
        p.environment = ShellEnvironment.current.merging(extra) { $1 }
        if let cwd { p.currentDirectoryURL = cwd }
        p.standardInput = FileHandle.nullDevice
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = pipe
        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty else {
                handle.readabilityHandler = nil
                return
            }
            let text = String(decoding: data, as: UTF8.self)
            Task { @MainActor in self?.append(text) }
        }
        p.terminationHandler = { [weak self] proc in
            let status = proc.terminationStatus
            Task { @MainActor in self?.finished(status) }
        }
        do {
            try p.run()
            process = p
            isRunning = true
            startedAt = Date()
        } catch {
            append("Couldn't start \(executable.lastPathComponent): \(error.localizedDescription)\n")
            exitStatus = -1
        }
    }

    /// Asks the process to stop, and insists after a few seconds.
    func stop() {
        guard let p = process, p.isRunning else { return }
        p.terminate()
        let pid = p.processIdentifier
        DispatchQueue.global().asyncAfter(deadline: .now() + 4) {
            if p.isRunning { kill(pid, SIGKILL) }
        }
    }

    /// Stops the process and waits for it, for when the app quits.
    func stopAndWait() {
        guard let p = process, p.isRunning else { return }
        p.terminate()
        let deadline = Date().addingTimeInterval(3)
        while p.isRunning && Date() < deadline { usleep(50_000) }
        if p.isRunning { kill(p.processIdentifier, SIGKILL) }
    }

    func append(_ text: String) {
        output += text
        if output.utf8.count > limit {
            output = String(output.suffix(limit * 3 / 4))
        }
    }

    private func finished(_ status: Int32) {
        isRunning = false
        exitStatus = status
        process = nil
        onExit?(status)
    }
}
