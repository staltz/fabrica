import Foundation
import Testing
@testable import Fabrica

@Suite struct DashboardLinkTests {
    /// What `fabrica dashboard` prints when it starts.
    static let started = "Run history from /Users/you/.fabrica/runs.db\n\nOpen this link in your browser (it works once):\n\n  http://127.0.0.1:52817/_dashboard?code=9f2c41d07b6e83a5c0d2e1f47a98b3c6\n\nPress Ctrl-C to stop.\n"
    /// What it adds each time it's sent SIGUSR1.
    static let fresh = "\nA fresh link (it works once):\n\n  http://127.0.0.1:52817/_dashboard?code=0123456789abcdef0123456789abcdef\n"

    @Test func findsTheFirstLink() {
        #expect(DashboardLink.all(in: Self.started) == [
            URL(string: "http://127.0.0.1:52817/_dashboard?code=9f2c41d07b6e83a5c0d2e1f47a98b3c6")!,
        ])
    }

    @Test func findsFreshLinksInTheOrderTheyWerePrinted() {
        let second = "\nA fresh link (it works once):\n\n  http://127.0.0.1:52817/_dashboard?code=fedcba9876543210fedcba9876543210\n"
        #expect(DashboardLink.all(in: Self.started + Self.fresh + second).map(\.absoluteString) == [
            "http://127.0.0.1:52817/_dashboard?code=9f2c41d07b6e83a5c0d2e1f47a98b3c6",
            "http://127.0.0.1:52817/_dashboard?code=0123456789abcdef0123456789abcdef",
            "http://127.0.0.1:52817/_dashboard?code=fedcba9876543210fedcba9876543210",
        ])
    }

    @Test func knowsWhichLinksAreNew() {
        let before = Set(DashboardLink.all(in: Self.started))
        let now = DashboardLink.all(in: Self.started + Self.fresh)
        #expect(now.filter { !before.contains($0) }.map(\.absoluteString) == [
            "http://127.0.0.1:52817/_dashboard?code=0123456789abcdef0123456789abcdef",
        ])
    }

    @Test func readsTheLinkOutOfALongerLine() throws {
        let links = DashboardLink.all(in: "Open http://127.0.0.1:8080/_dashboard?code=abc123, please\r\n")
        #expect(links.map(\.absoluteString) == ["http://127.0.0.1:8080/_dashboard?code=abc123"])
        let link = try #require(links.first)
        #expect(link.port == 8080)
        #expect(URLComponents(url: link, resolvingAgainstBaseURL: false)?.queryItems?.first?.value == "abc123")
    }

    @Test func waitsForTheWholeLine() {
        // The link is still arriving: the code could be longer.
        #expect(DashboardLink.all(in: "Open this link:\n\n  http://127.0.0.1:52817/_dashboard?code=9f2c41d0").isEmpty)
        #expect(DashboardLink.all(in: "Open this link:\n\n  http://127.0.0.1:52817/_dashboard?code=9f2c41d0\n").count == 1)
        #expect(DashboardLink.all(in: "").isEmpty)
    }

    @Test func ignoresAnythingElse() {
        let others = [
            "http://127.0.0.1:52817/_dashboard\n",
            "http://127.0.0.1:52817/_dashboard?code=\n",
            "http://127.0.0.1:52817/_dashboard?code=NOPE\n",
            "http://127.0.0.1:52817/_dashboard?code=9F2C41D0\n",
            "http://127.0.0.1:52817/_dashboard?code=9f2c41d0zz\n",
            "http://127.0.0.1:8080/healthz\n",
            "https://127.0.0.1:52817/_dashboard?code=9f2c41d0\n",
            "http://localhost:52817/_dashboard?code=9f2c41d0\n",
            "http://example.com:52817/_dashboard?code=9f2c41d0\n",
            "Press Ctrl-C to stop.\n",
            "fabrica: there's no run history at /Users/you/.fabrica/runs.db yet\n",
        ]
        for output in others {
            #expect(DashboardLink.all(in: output).isEmpty, "found a link in: \(output)")
        }
    }
}
