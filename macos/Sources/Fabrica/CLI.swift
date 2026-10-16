import AppKit
import Foundation

/// The fabrica command line tool: the copy inside the app, and the link to
/// it in ~/.local/bin that makes `fabrica` work in Terminal.
///
/// The app installs the CLI as a symlink into its own bundle, so updating
/// the app updates the CLI, and the two can never drift apart. That's also
/// why `fabrica -u` refuses to update a fabrica that lives inside an app.
@MainActor @Observable
final class CLI {
    enum Install: Equatable {
        /// ~/.local/bin/fabrica points into this app.
        case linked
        /// There's no fabrica in ~/.local/bin.
        case missing
        /// ~/.local/bin/fabrica is something else, like one install.sh put there.
        case separate(version: String)
        /// The app wasn't built with a fabrica inside (a `swift run` build).
        case notBundled
    }

    private(set) var install: Install = .missing
    /// The version of the fabrica the app runs, like "fabrica 0.1.11 (7521d9d 2026-10-15)".
    private(set) var version = ""
    /// Whether ~/.local/bin is on your login shell's PATH.
    private(set) var onPath = true
    private(set) var lastError: String?

    nonisolated static let linkFolder = FileManager.default.homeDirectoryForCurrentUser.appending(path: ".local/bin")
    nonisolated static let link = linkFolder.appending(path: "fabrica")

    /// The fabrica inside the app bundle, if there is one.
    let bundled: URL? = {
        let url = Bundle.main.bundleURL.appending(path: "Contents/Helpers/fabrica")
        return FileManager.default.isExecutableFile(atPath: url.path) ? url : nil
    }()

    /// The fabrica the app runs: its own, or else whatever's installed.
    var executable: URL {
        if let bundled { return bundled }
        for dir in (ShellEnvironment.current["PATH"] ?? "").split(separator: ":") {
            let url = URL(fileURLWithPath: String(dir)).appending(path: "fabrica")
            if FileManager.default.isExecutableFile(atPath: url.path) { return url }
        }
        return Self.link
    }

    /// Environment for every fabrica the app runs, so that `apply`
    /// registers the stable ~/.local/bin path with your tools rather than
    /// a path inside the app, which changes when the app moves.
    var environment: [String: String] {
        install == .linked ? ["FABRICA_BIN": Self.link.path] : [:]
    }

    /// Links the CLI in when that's safe: nothing is there, or the link
    /// points into a Fabrica.app (this one or an older copy). A separate
    /// install is left alone until you say so.
    func setUp() async {
        refresh()
        if bundled != nil, install == .missing || needsRelink() {
            do { try linkIn() } catch { lastError = error.localizedDescription }
            refresh()
        }
        let result = await runCommand(executable, ["version"])
        version = result.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
        onPath = await Task.detached { ShellEnvironment.loginPathContains(CLI.linkFolder.path) }.value
        if case .separate = install {
            let other = await runCommand(Self.link, ["version"])
            install = .separate(version: other.stdout.trimmingCharacters(in: .whitespacesAndNewlines))
        }
    }

    func refresh() {
        guard let bundled else {
            install = .notBundled
            return
        }
        let fm = FileManager.default
        guard let attrs = try? fm.attributesOfItem(atPath: Self.link.path) else {
            install = .missing
            return
        }
        if attrs[.type] as? FileAttributeType == .typeSymbolicLink,
           let dest = try? fm.destinationOfSymbolicLink(atPath: Self.link.path),
           URL(fileURLWithPath: dest).standardizedFileURL == bundled.standardizedFileURL {
            install = .linked
        } else if case .separate = install {
            // keep the version we found
        } else {
            install = .separate(version: "")
        }
    }

    /// A link into another (or a deleted) copy of Fabrica.app.
    private func needsRelink() -> Bool {
        guard let dest = try? FileManager.default.destinationOfSymbolicLink(atPath: Self.link.path) else { return false }
        return dest.contains(".app/Contents/Helpers/fabrica") && install != .linked
    }

    /// Replaces a separate fabrica with the app's. The old one goes to the
    /// Trash, so this is easy to undo.
    func replaceSeparate() async {
        lastError = nil
        do {
            try await NSWorkspace.shared.recycle([Self.link])
            try linkIn()
        } catch {
            lastError = error.localizedDescription
        }
        await setUp()
    }

    private func linkIn() throws {
        guard let bundled else { return }
        let fm = FileManager.default
        try fm.createDirectory(at: Self.linkFolder, withIntermediateDirectories: true)
        if (try? fm.destinationOfSymbolicLink(atPath: Self.link.path)) != nil {
            try fm.removeItem(at: Self.link)
        }
        try fm.createSymbolicLink(at: Self.link, withDestinationURL: bundled)
    }

    /// Adds ~/.local/bin to PATH in ~/.zprofile (or ~/.bash_profile).
    func addToPath() async {
        let shell = ProcessInfo.processInfo.environment["SHELL"] ?? "/bin/zsh"
        let file = FileManager.default.homeDirectoryForCurrentUser
            .appending(path: shell.hasSuffix("bash") ? ".bash_profile" : ".zprofile")
        let line = "\n# Added by Fabrica.app\nexport PATH=\"$HOME/.local/bin:$PATH\"\n"
        do {
            if let handle = try? FileHandle(forWritingTo: file) {
                handle.seekToEndOfFile()
                handle.write(Data(line.utf8))
                try handle.close()
            } else {
                try line.write(to: file, atomically: true, encoding: .utf8)
            }
            onPath = true
        } catch {
            lastError = error.localizedDescription
        }
    }

    /// Runs fabrica with the app's environment.
    func run(_ args: [String], cwd: URL? = nil) async -> CommandResult {
        await runCommand(executable, args, cwd: cwd, env: environment)
    }
}
