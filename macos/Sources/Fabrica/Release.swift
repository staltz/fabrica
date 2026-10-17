import Foundation

/// A Fabrica release on GitHub with the app in it: release.yml adds
/// Fabrica-macos.zip and its .sha256 to each release, a few minutes after
/// the CLI binaries.
struct Release: Equatable, Sendable {
    /// The release's tag, like v1.3.0.
    var tag: String
    /// The SHA-256 the release lists for Fabrica-macos.zip.
    var checksum: String

    static let repo = URL(string: "https://github.com/staltz/fabrica")!
    static let asset = "Fabrica-macos.zip"

    /// Like 1.3.0, the way the app's Info.plist has it.
    var version: String { tag.hasPrefix("v") ? String(tag.dropFirst()) : tag }
    var page: URL { Self.page(tag) }
    var download: URL { Self.files(tag).appending(path: Self.asset) }

    static func page(_ tag: String) -> URL { repo.appending(path: "releases/tag/\(tag)") }
    static func files(_ tag: String) -> URL { repo.appending(path: "releases/download/\(tag)") }

    /// The tag in a release page's URL, like …/releases/tag/v1.3.0, which
    /// is where GitHub sends you from /releases/latest.
    static func tag(inPage url: URL) -> String? {
        let parts = url.pathComponents
        guard parts.count >= 3, parts[parts.count - 3] == "releases", parts[parts.count - 2] == "tag" else { return nil }
        return parts[parts.count - 1]
    }

    /// A file's SHA-256 in shasum's output, like "3c9e…  Fabrica-macos.zip".
    static func checksum(in text: String, for file: String) -> String? {
        for line in text.split(whereSeparator: \.isNewline) {
            let fields = line.split(whereSeparator: \.isWhitespace)
            // shasum marks binary-mode files with a leading *.
            guard fields.count == 2, fields[1].drop(while: { $0 == "*" }) == file else { continue }
            let sum = fields[0].lowercased()
            return sum.count == 64 && sum.allSatisfy(\.isHexDigit) ? sum : nil
        }
        return nil
    }

    /// Whether version comes after current. Both are like 1.3.0 or v1.3.0,
    /// and a pre-release like 1.3.0-rc.1 comes before its release.
    static func isNewer(_ version: String, than current: String) -> Bool {
        func parts(_ v: String) -> (numbers: [Int], prerelease: Bool) {
            let core = (v.hasPrefix("v") ? v.dropFirst() : v[...]).split(separator: "-", maxSplits: 1, omittingEmptySubsequences: false)
            return ((core.first ?? "").split(separator: ".").map { Int($0) ?? 0 }, core.count > 1)
        }
        let new = parts(version), old = parts(current)
        for i in 0..<max(new.numbers.count, old.numbers.count) {
            let a = i < new.numbers.count ? new.numbers[i] : 0
            let b = i < old.numbers.count ? old.numbers[i] : 0
            if a != b { return a > b }
        }
        return old.prerelease && !new.prerelease
    }
}
