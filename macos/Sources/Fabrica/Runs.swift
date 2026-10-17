import Foundation
import SQLite3

/// A run from Fabrica's run history (the run_overview view, which Fabrica
/// promises to keep working across versions).
struct Run: Identifiable, Equatable, Sendable {
    var id: String
    var startedAt: Date?
    var intent: String
    var routine: String?
    var runtime: String
    var repo: String
    var factory: String
    var lastStatus: String?
    var lastSummary: String?
    var skills: [String]

    var repoShort: String { repo.split(separator: "/").suffix(2).joined(separator: "/") }
}

/// Where run history comes from: your tools on this Mac, or the cloud
/// runtimes that use the server this Mac runs. The two never mix.
enum RunSource: Hashable {
    case local, server

    /// The runs.db this source reads.
    @MainActor
    func database(in home: HomeRepo) -> URL {
        switch self {
        case .local: home.runsDB
        case .server: Server.database
        }
    }
}

enum RunHistory {
    /// The latest runs in a runs.db, newest first. Opens it read-only, so
    /// a server writing to it at the same time is fine.
    static func latest(in db: URL, limit: Int = 100) async -> [Run] {
        await Task.detached { read(db, limit) }.value
    }

    private static func read(_ url: URL, _ limit: Int) -> [Run] {
        guard FileManager.default.fileExists(atPath: url.path) else { return [] }
        var db: OpaquePointer?
        guard sqlite3_open_v2(url.path, &db, SQLITE_OPEN_READONLY, nil) == SQLITE_OK else {
            sqlite3_close(db)
            return []
        }
        defer { sqlite3_close(db) }
        sqlite3_busy_timeout(db, 2000)
        let sql = """
            SELECT run_id, started_at, COALESCE(intent, ''), routine, COALESCE(runtime, ''), COALESCE(repo, ''),
                   COALESCE(factory, ''), last_status, last_summary, COALESCE(skills_loaded, '')
            FROM run_overview ORDER BY run_id DESC LIMIT ?
            """
        var stmt: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &stmt, nil) == SQLITE_OK else { return [] }
        defer { sqlite3_finalize(stmt) }
        sqlite3_bind_int(stmt, 1, Int32(limit))

        let iso = ISO8601DateFormatter()
        iso.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let isoPlain = ISO8601DateFormatter()
        func text(_ i: Int32) -> String? {
            guard let c = sqlite3_column_text(stmt, i) else { return nil }
            return String(cString: c)
        }
        var runs: [Run] = []
        while sqlite3_step(stmt) == SQLITE_ROW {
            let started = text(1) ?? ""
            runs.append(Run(
                id: text(0) ?? UUID().uuidString,
                startedAt: iso.date(from: started) ?? isoPlain.date(from: started),
                intent: text(2) ?? "",
                routine: text(3),
                runtime: text(4) ?? "",
                repo: text(5) ?? "",
                factory: text(6) ?? "",
                lastStatus: text(7),
                lastSummary: text(8),
                skills: (text(9) ?? "").split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }))
        }
        return runs
    }
}
