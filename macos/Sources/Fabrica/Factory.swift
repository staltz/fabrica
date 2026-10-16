import Foundation
import Yams

/// One factory file, factories/<name>.yaml, as the editor sees it.
///
/// The keys mirror Fabrica's own (internal/home/factory.go), which accepts
/// nothing else, so a factory round-trips through the editor without
/// losing anything but comments. The comment block at the top of the file
/// is kept: the editor shows it as the factory's notes.
struct Factory: Equatable, Sendable {
    var name: String
    /// A factory that's off is saved as <name>.off, which Fabrica ignores.
    var enabled = true
    var notes = ""
    var blueprint = Blueprint()
    var server = ""
    var runtimes: [String] = []
    var routines: [Routine] = []
    var targets: [Target] = []
    var params: [Param] = []
    /// `init: false` turns the blueprint's init routine off.
    var runsInit = true

    struct Blueprint: Equatable, Sendable {
        var git = ""
        var ref = "main"
        var path = ""
    }

    struct Routine: Equatable, Sendable, Identifiable {
        var id = UUID()
        var name: String
        /// nil runs it on every runtime of the factory; [] on none (only
        /// allowed next to "*").
        var runtimes: [String]?

        static func == (a: Self, b: Self) -> Bool { a.name == b.name && a.runtimes == b.runtimes }
    }

    struct Target: Equatable, Sendable, Identifiable {
        var id = UUID()
        var repo: String
        var params: [Param] = []

        static func == (a: Self, b: Self) -> Bool { a.repo == b.repo && a.params == b.params }
    }

    struct Param: Equatable, Sendable, Identifiable {
        var id = UUID()
        var key: String
        var value: String

        static func == (a: Self, b: Self) -> Bool { a.key == b.key && a.value == b.value }
    }

    static let wildcard = "*"

    var isLocal: Bool { runtimes.contains { Runtime.local.contains($0) } }
    var isCloud: Bool { !runtimes.isEmpty && !isLocal }
    var coversEveryRepo: Bool { targets.count == 1 && targets[0].repo == Self.wildcard }
    var allRoutines: Routine? { routines.first { $0.name == Self.wildcard } }

    /// The factory's URL on its server, which Fabrica builds the same way.
    var endpoint: String? {
        server.isEmpty ? nil : server.trimmingCharacters(in: CharacterSet(charactersIn: "/")) + "/" + name
    }

    static func isValidName(_ s: String) -> Bool {
        s.range(of: #"^[a-z0-9]+(-[a-z0-9]+)*$"#, options: .regularExpression) != nil && s.count <= 63
    }
}

/// The built-in runtimes, so the editor can offer them as checkboxes.
enum Runtime {
    static let local = ["claude-code-local", "cursor-local", "codex-cli", "copilot-local"]
    static let cloud = ["claude-code-cloud", "cursor-cloud", "codex", "copilot-cloud"]

    static func title(_ name: String) -> String {
        switch name {
        case "claude-code-local": "Claude Code"
        case "cursor-local": "Cursor"
        case "codex-cli": "Codex CLI"
        case "copilot-local": "Copilot CLI"
        case "claude-code-cloud": "Claude routines"
        case "cursor-cloud": "Cursor automations"
        case "codex": "Codex cloud"
        case "copilot-cloud": "Copilot automations"
        default: name
        }
    }
}

// MARK: - Reading

struct FactoryParseError: LocalizedError {
    var message: String
    var errorDescription: String? { message }
}

extension Factory {
    init(name: String, enabled: Bool, yaml text: String) throws {
        self.init(name: name, enabled: enabled)
        notes = Self.leadingComment(text)
        guard let root = try Yams.compose(yaml: text) else { return }
        guard let map = root.mapping else { throw FactoryParseError(message: "The file isn't a YAML mapping.") }

        for (keyNode, value) in map {
            guard let key = keyNode.string else { continue }
            switch key {
            case "blueprint":
                guard let m = value.mapping else { throw FactoryParseError(message: "blueprint: should be a mapping with git and ref.") }
                blueprint.git = m["git"]?.string ?? ""
                blueprint.ref = m["ref"]?.string ?? ""
                blueprint.path = m["path"]?.string ?? ""
            case "server":
                server = value.string ?? ""
            case "runtimes":
                runtimes = try Self.strings(value, key)
            case "routines":
                routines = try (value.sequence ?? []).map { node in
                    if let s = node.scalar?.string { return Routine(name: s) }
                    guard let m = node.mapping, let name = m["routine"]?.string else {
                        throw FactoryParseError(message: "routines: each entry is a name, or { routine, runtimes }.")
                    }
                    return Routine(name: name, runtimes: try m["runtimes"].map { try Self.strings($0, "runtimes") })
                }
            case "targets":
                targets = try (value.sequence ?? []).map { node in
                    if let s = node.scalar?.string { return Target(repo: s) }
                    guard let m = node.mapping, let repo = m["repo"]?.string else {
                        throw FactoryParseError(message: "targets: each entry is a repo, or { repo, params }.")
                    }
                    return Target(repo: repo, params: Self.params(m["params"]))
                }
            case "params":
                params = Self.params(value)
            case "init":
                runsInit = value.bool ?? true
            default:
                throw FactoryParseError(message: "\(key): isn't something a factory can have.")
            }
        }
    }

    private static func strings(_ node: Node, _ key: String) throws -> [String] {
        if node.scalar?.string == "" { return [] }
        guard let seq = node.sequence else { throw FactoryParseError(message: "\(key): should be a list.") }
        return seq.compactMap { $0.string }
    }

    private static func params(_ node: Node?) -> [Param] {
        guard let m = node?.mapping else { return [] }
        return m.compactMap { k, v in
            guard let key = k.string else { return nil }
            return Param(key: key, value: v.string ?? "")
        }
    }

    /// The comment block at the very top of the file, without the #s. It's
    /// prose: wrapped lines join into paragraphs, and blank comment lines
    /// separate them.
    static func leadingComment(_ text: String) -> String {
        var lines: [String] = []
        for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("#") {
                var body = trimmed.dropFirst()
                if body.hasPrefix(" ") { body = body.dropFirst() }
                lines.append(String(body))
            } else if trimmed.isEmpty && lines.isEmpty {
                continue
            } else {
                break
            }
        }
        var paragraphs: [String] = []
        var current: [String] = []
        for line in lines {
            if line.trimmingCharacters(in: .whitespaces).isEmpty {
                if !current.isEmpty { paragraphs.append(current.joined(separator: " ")) }
                current = []
            } else {
                current.append(line.trimmingCharacters(in: .whitespaces))
            }
        }
        if !current.isEmpty { paragraphs.append(current.joined(separator: " ")) }
        return paragraphs.joined(separator: "\n")
    }

    /// Wraps prose for a comment block, at 76 columns.
    static func commentLines(_ notes: String) -> [String] {
        var out: [String] = []
        for (i, paragraph) in notes.split(separator: "\n", omittingEmptySubsequences: false).enumerated() {
            if i > 0 { out.append("") }
            var line = ""
            for word in paragraph.split(separator: " ") {
                if !line.isEmpty, line.count + 1 + word.count > 76 {
                    out.append(line)
                    line = ""
                }
                line += line.isEmpty ? String(word) : " " + word
            }
            if !line.isEmpty { out.append(line) }
        }
        return out
    }
}

// MARK: - Writing

extension Factory {
    /// The factory as YAML, in the compact style of Fabrica's own examples.
    var yaml: String {
        var out = ""
        let trimmedNotes = notes.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedNotes.isEmpty {
            for line in Self.commentLines(trimmedNotes) {
                out += line.isEmpty ? "#\n" : "# \(line)\n"
            }
        }

        var bp = ["git: \(Y.flow(blueprint.git))", "ref: \(Y.flow(blueprint.ref))"]
        if !blueprint.path.isEmpty { bp.append("path: \(Y.flow(blueprint.path))") }
        out += "blueprint: { \(bp.joined(separator: ", ")) }\n"

        if !server.isEmpty { out += "server: \(Y.block(server))\n" }
        out += "runtimes: \(Y.list(runtimes))\n"

        if !routines.isEmpty {
            if routines.allSatisfy({ $0.runtimes == nil }) {
                out += "routines: \(Y.list(routines.map(\.name)))\n"
            } else {
                out += "routines:\n"
                for r in routines {
                    if let rts = r.runtimes {
                        out += "  - { routine: \(Y.flow(r.name)), runtimes: \(Y.list(rts)) }\n"
                    } else {
                        out += "  - \(Y.block(r.name))\n"
                    }
                }
            }
        }

        if targets.allSatisfy({ $0.params.isEmpty }) {
            out += "targets: \(Y.list(targets.map(\.repo)))\n"
        } else {
            out += "targets:\n"
            for t in targets {
                if t.params.isEmpty {
                    out += "  - \(Y.block(t.repo))\n"
                } else {
                    out += "  - repo: \(Y.block(t.repo))\n    params: \(Y.map(t.params))\n"
                }
            }
        }

        if !params.isEmpty {
            out += "params:\n"
            for p in params { out += "  \(Y.block(p.key)): \(Y.block(p.value))\n" }
        }
        if !runsInit { out += "init: false\n" }
        return out
    }
}

/// Just enough YAML quoting. Plain scalars where they read back as the same
/// string, double quotes everywhere else.
enum Y {
    private static let reserved: Set<String> = ["true", "false", "yes", "no", "on", "off", "null", "~", "y", "n"]

    static func block(_ s: String) -> String { quoteIfNeeded(s, flow: false) }
    static func flow(_ s: String) -> String { quoteIfNeeded(s, flow: true) }
    static func list(_ items: [String]) -> String { "[" + items.map(flow).joined(separator: ", ") + "]" }
    static func map(_ params: [Factory.Param]) -> String {
        "{ " + params.map { "\(flow($0.key)): \(flow($0.value))" }.joined(separator: ", ") + " }"
    }

    static func quoteIfNeeded(_ s: String, flow: Bool) -> String {
        let plain = flow ? #"^[A-Za-z0-9_/][A-Za-z0-9_/.@+-]*$"# : #"^[A-Za-z0-9_/][A-Za-z0-9_/.@+:-]*$"#
        let looksLikeNumber = Double(s) != nil || s.hasPrefix("0x") || s.hasPrefix("0o")
        if !s.isEmpty, s.range(of: plain, options: .regularExpression) != nil,
           !reserved.contains(s.lowercased()), !looksLikeNumber {
            return s
        }
        var q = "\""
        for ch in s {
            switch ch {
            case "\\": q += "\\\\"
            case "\"": q += "\\\""
            case "\n": q += "\\n"
            case "\t": q += "\\t"
            default: q.append(ch)
            }
        }
        return q + "\""
    }
}
