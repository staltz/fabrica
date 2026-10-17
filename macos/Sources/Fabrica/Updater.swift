import AppKit
import CryptoKit
import Foundation

/// Keeps Fabrica.app up to date from its GitHub releases.
///
/// It finds the latest release the way `fabrica -u` does, from where GitHub
/// redirects /releases/latest, which unlike its API has no rate limit. An
/// update downloads Fabrica-macos.zip, checks it against the release's
/// SHA-256, unzips it, and checks that it's Fabrica.app at that version
/// with a valid code signature, made by the same Developer ID as this app
/// when this app has one. Only then does it swap the new app in for this
/// one, in the same place, and relaunch. The CLI comes along, since
/// ~/.local/bin/fabrica links into the app.
@MainActor @Observable
final class Updater {
    /// This app's version, like 1.2.0. A `swift run` build has none.
    let current = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
    /// A newer release, if the last check found one.
    private(set) var available: Release?
    private(set) var isChecking = false
    /// What an update is doing right now, like "Downloading Fabrica 1.3.0…".
    private(set) var installing: String?
    private(set) var lastChecked: Date?
    private(set) var problem: String?

    var checksAutomatically: Bool {
        didSet {
            UserDefaults.standard.set(checksAutomatically, forKey: "update.auto")
            schedule()
        }
    }

    private var timer: Task<Void, Never>?

    init() {
        checksAutomatically = UserDefaults.standard.object(forKey: "update.auto") as? Bool ?? true
    }

    /// Why this copy of the app can't replace itself, if it can't. It can
    /// still check, and you can download the update yourself.
    var cantInstall: String? {
        let app = Bundle.main.bundleURL
        let folder = app.deletingLastPathComponent()
        if current == nil || app.pathExtension != "app" {
            return "Only Fabrica.app updates itself, and this build isn't in one."
        }
        if app.path.contains("/AppTranslocation/") {
            return "Move Fabrica to your Applications folder to update it from here. Until then, macOS runs it from a read-only copy."
        }
        if !FileManager.default.isWritableFile(atPath: folder.path) {
            return "Fabrica can't write to \(abbreviatedPath(folder)), where it is, so it can't update itself there."
        }
        return nil
    }

    /// Checks a few seconds after launch, and every few hours after that,
    /// while automatic checks are on.
    func schedule() {
        timer?.cancel()
        timer = nil
        guard checksAutomatically, current != nil else { return }
        timer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(5))
            while !Task.isCancelled {
                await self?.check(quietly: true)
                try? await Task.sleep(for: .seconds(6 * 60 * 60))
            }
        }
    }

    /// Asks GitHub for the latest release. A quiet check, the automatic
    /// kind, keeps its problems to itself, like being offline.
    func check(quietly: Bool = false) async {
        guard let current, !isChecking, installing == nil else { return }
        isChecking = true
        defer { isChecking = false }
        if !quietly { problem = nil }
        do {
            available = try await Self.latest(newerThan: current)
            lastChecked = Date()
            problem = nil
        } catch {
            if !quietly { problem = "Couldn't check for updates: \(error.localizedDescription)" }
        }
    }

    /// Downloads the available release, checks it, and puts it in this
    /// app's place. Returns whether it did, so the app can relaunch.
    func install() async -> Bool {
        guard let release = available, installing == nil, !isChecking else { return false }
        if let reason = cantInstall {
            problem = reason
            return false
        }
        problem = nil
        defer { installing = nil }
        do {
            try await install(release, over: Bundle.main.bundleURL)
            return true
        } catch {
            problem = error.localizedDescription
            return false
        }
    }

    /// Quits, and opens the app again once this process is gone. What
    /// opens is whatever is at this app's path, which is the new version
    /// by now.
    func relaunch() {
        let wait = Process()
        wait.executableURL = URL(fileURLWithPath: "/bin/sh")
        wait.arguments = [
            "-c", "while /bin/kill -0 \"$1\" 2>/dev/null; do /bin/sleep 0.2; done; exec /usr/bin/open \"$2\"",
            "fabrica-relaunch", String(ProcessInfo.processInfo.processIdentifier), Bundle.main.bundleURL.path,
        ]
        do {
            try wait.run()
        } catch {
            problem = "Fabrica is updated. Quit it and open it again to start the new version."
            return
        }
        NSApp.terminate(nil)
    }

    // MARK: Checking

    /// The latest release, if it's newer than current and has the app in
    /// it. A release whose app isn't up yet doesn't count: it will soon.
    nonisolated private static func latest(newerThan current: String) async throws -> Release? {
        var request = URLRequest(url: Release.repo.appending(path: "releases/latest"))
        request.httpMethod = "HEAD"
        let (_, response) = try await URLSession.shared.data(for: request)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 else { throw UpdateError("GitHub answered \(status).") }
        guard let page = response.url, let tag = Release.tag(inPage: page) else {
            throw UpdateError("There's no release on GitHub yet.")
        }
        guard Release.isNewer(tag, than: current) else { return nil }

        let sums = Release.files(tag).appending(path: Release.asset + ".sha256")
        let (data, sumsResponse) = try await URLSession.shared.data(from: sums)
        switch (sumsResponse as? HTTPURLResponse)?.statusCode ?? 0 {
        case 200:
            guard let sum = Release.checksum(in: String(decoding: data, as: UTF8.self), for: Release.asset) else {
                throw UpdateError("Release \(tag) has a \(sums.lastPathComponent) without a checksum for \(Release.asset).")
            }
            return Release(tag: tag, checksum: sum)
        case 404:
            return nil
        case let code:
            throw UpdateError("GitHub answered \(code) for \(sums.lastPathComponent).")
        }
    }

    // MARK: Installing

    private func install(_ release: Release, over app: URL) async throws {
        let fm = FileManager.default
        // On the same volume as the app, so the swap at the end is a rename.
        let work = try fm.url(for: .itemReplacementDirectory, in: .userDomainMask, appropriateFor: app, create: true)
        defer { try? fm.removeItem(at: work) }

        installing = "Downloading Fabrica \(release.version)…"
        let zip = try await Self.download(release.download, to: work.appending(path: Release.asset))

        installing = "Checking Fabrica \(release.version)…"
        let sum = try await Self.sha256(of: zip)
        guard sum == release.checksum else {
            throw UpdateError("The download's checksum doesn't match the release's (expected \(release.checksum), got \(sum)). Nothing was changed.")
        }
        let unzipped = work.appending(path: "unzipped")
        let ditto = await runCommand(URL(fileURLWithPath: "/usr/bin/ditto"), ["-x", "-k", zip.path, unzipped.path])
        guard ditto.ok else { throw UpdateError("Couldn't unzip the download: \(ditto.output)") }
        let new = unzipped.appending(path: "Fabrica.app")
        try await Self.verify(new, is: release, signedLike: app)

        installing = "Installing Fabrica \(release.version)…"
        do {
            _ = try fm.replaceItemAt(app, withItemAt: new, backupItemName: nil, options: .usingNewMetadataOnly)
        } catch {
            var message = "Couldn't put the new version in place of \(abbreviatedPath(app)): \(error.localizedDescription)"
            if (error as? CocoaError)?.code == .fileWriteNoPermission {
                message += " If macOS asked, let Fabrica update apps in System Settings → Privacy & Security → App Management."
            }
            throw UpdateError(message)
        }
    }

    nonisolated private static func download(_ url: URL, to file: URL) async throws -> URL {
        let (temp, response) = try await URLSession.shared.download(from: url)
        defer { try? FileManager.default.removeItem(at: temp) }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 else { throw UpdateError("Couldn't download \(url.lastPathComponent): GitHub answered \(status).") }
        try FileManager.default.moveItem(at: temp, to: file)
        return file
    }

    nonisolated private static func sha256(of file: URL) async throws -> String {
        try await Task.detached(priority: .userInitiated) {
            let data = try Data(contentsOf: file, options: .mappedIfSafe)
            return SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        }.value
    }

    /// Checks that new is Fabrica.app at the release's version, with a
    /// valid signature. When app is signed with a Developer ID, new has to
    /// be signed by the same team: an update never downgrades to a
    /// signature anyone could make.
    nonisolated private static func verify(_ new: URL, is release: Release, signedLike app: URL) async throws {
        let plist = (try? Data(contentsOf: new.appending(path: "Contents/Info.plist"))) ?? Data()
        let info = (try? PropertyListSerialization.propertyList(from: plist, format: nil)) as? [String: Any] ?? [:]
        guard let id = info["CFBundleIdentifier"] as? String, id == Bundle.main.bundleIdentifier else {
            throw UpdateError("The download doesn't have Fabrica.app in it. Nothing was changed.")
        }
        let version = info["CFBundleShortVersionString"] as? String ?? "?"
        guard version == release.version else {
            throw UpdateError("The download is Fabrica \(version), not \(release.version). Nothing was changed.")
        }
        var args = ["--verify", "--deep", "--strict"]
        if let team = await teamIdentifier(of: app) {
            args += ["-R", "=anchor apple generic and certificate leaf[subject.OU] = \"\(team)\""]
        }
        let result = await runCommand(codesign, args + [new.path])
        guard result.ok else {
            throw UpdateError("The download's code signature doesn't check out, so nothing was changed: \(result.output.trimmingCharacters(in: .whitespacesAndNewlines))")
        }
    }

    /// The team of the Developer ID that signed app, or nil when it's
    /// signed ad hoc.
    nonisolated private static func teamIdentifier(of app: URL) async -> String? {
        // codesign -d prints what it finds on stderr, like TeamIdentifier=ABCDE12345.
        let result = await runCommand(codesign, ["-dv", app.path])
        for line in result.stderr.split(whereSeparator: \.isNewline) where line.hasPrefix("TeamIdentifier=") {
            let team = line.dropFirst("TeamIdentifier=".count)
            return team.isEmpty || team == "not set" ? nil : String(team)
        }
        return nil
    }

    nonisolated private static let codesign = URL(fileURLWithPath: "/usr/bin/codesign")
}

struct UpdateError: LocalizedError {
    var message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}
