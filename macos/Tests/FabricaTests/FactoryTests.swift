import Testing
import Yams
@testable import Fabrica

@Suite struct FactoryTests {
    @Test func readsTheCompactStyle() throws {
        let f = try Factory(name: "exis", enabled: true, yaml: """
            blueprint:
              git: github.com/staltz/fabrica-blueprint-exis
              ref: master
            server: https://mcp.example.dev/fabrica
            runtimes: [cursor-cloud, claude-code-cloud]
            targets: [github.com/staltz/exis]
            routines:
              - { routine: "*", runtimes: [cursor-cloud] }
              - { routine: grind, runtimes: [claude-code-cloud] }
            """)
        #expect(f.blueprint.git == "github.com/staltz/fabrica-blueprint-exis")
        #expect(f.blueprint.ref == "master")
        #expect(f.isCloud)
        #expect(f.routines.map(\.name) == ["*", "grind"])
        #expect(f.routines[0].runtimes == ["cursor-cloud"])
        #expect(f.endpoint == "https://mcp.example.dev/fabrica/exis")
    }

    @Test func keepsTheLeadingCommentAsNotes() throws {
        let f = try Factory(name: "genesis", enabled: true, yaml: """
            # Genesis on this laptop: writing and interview discipline
            # from the genesis blueprint.
            blueprint: { git: /Users/me/blueprint, ref: master }
            runtimes: [claude-code-local, codex-cli]
            targets: ["*"]
            """)
        #expect(f.notes == "Genesis on this laptop: writing and interview discipline from the genesis blueprint.")
        #expect(f.coversEveryRepo)
        #expect(f.isLocal)
        #expect(f.yaml.hasPrefix("# Genesis on this laptop"))
    }

    @Test func wrapsNotesAsParagraphs() throws {
        var f = Factory(name: "x", runtimes: ["claude-code-local"], targets: [.init(repo: "*")])
        f.blueprint = .init(git: "github.com/a/b", ref: "main")
        f.notes = String(repeating: "word ", count: 30).trimmingCharacters(in: .whitespaces) + "\nSecond paragraph."
        let yaml = f.yaml
        let comments = yaml.split(separator: "\n").filter { $0.hasPrefix("#") }
        #expect(comments.allSatisfy { $0.count <= 78 })
        #expect(comments.contains("#"))
        let back = try Factory(name: "x", enabled: true, yaml: yaml)
        #expect(back.notes == f.notes)
    }

    @Test func roundTrips() throws {
        var f = Factory(name: "pr-review")
        f.notes = "Reviews every PR."
        f.blueprint = .init(git: "github.com/acme/blueprint", ref: "stable", path: "fabrica")
        f.server = "https://fabrica.acme.dev"
        f.alias = "review"
        f.runtimes = ["claude-code-cloud", "cursor-cloud"]
        f.routines = [.init(name: "*", runtimes: ["claude-code-cloud"]), .init(name: "release-notes", runtimes: [])]
        f.targets = [.init(repo: "github.com/acme/app"),
                     .init(repo: "github.com/acme/api", params: [.init(key: "slack_channel", value: "#api")])]
        f.params = [.init(key: "slack_channel", value: "#eng"), .init(key: "strict", value: "true")]
        f.runsInit = false

        let yaml = f.yaml
        let back = try Factory(name: "pr-review", enabled: true, yaml: yaml)
        #expect(back == f, "\(yaml)")
        // And it's YAML that any parser reads the same way.
        let loaded = try Yams.load(yaml: yaml) as? [String: Any]
        #expect((loaded?["init"] as? Bool) == false)
        #expect((loaded?["params"] as? [String: Any])?["strict"] as? String == "true")
    }

    @Test func quotesOnlyWhenNeeded() {
        #expect(Y.flow("github.com/acme/app") == "github.com/acme/app")
        #expect(Y.flow("*") == "\"*\"")
        #expect(Y.flow("#eng") == "\"#eng\"")
        #expect(Y.block("https://x.dev/a") == "https://x.dev/a")
        #expect(Y.flow("https://x.dev/a") == "\"https://x.dev/a\"")
        #expect(Y.block("yes") == "\"yes\"")
        #expect(Y.block("1.5") == "\"1.5\"")
        #expect(Y.block("") == "\"\"")
    }

    @Test func canonicalizesRepoNames() {
        #expect(canonicalRepo("git@github.com:Staltz/Fabrica.git") == "github.com/staltz/fabrica")
        #expect(canonicalRepo("https://github.com/acme/app.git") == "github.com/acme/app")
        #expect(canonicalRepo("ssh://git@github.com:22/acme/app") == "github.com/acme/app")
        #expect(canonicalRepo("*") == "*")
    }
}
