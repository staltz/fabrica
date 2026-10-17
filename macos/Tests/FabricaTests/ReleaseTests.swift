import Foundation
import Testing
@testable import Fabrica

@Suite struct ReleaseTests {
    @Test func findsTheTagInTheReleasePage() {
        #expect(Release.tag(inPage: URL(string: "https://github.com/staltz/fabrica/releases/tag/v1.2.0")!) == "v1.2.0")
        // Where /releases/latest goes when there's no release yet.
        #expect(Release.tag(inPage: URL(string: "https://github.com/staltz/fabrica/releases")!) == nil)
        #expect(Release.tag(inPage: URL(string: "https://github.com/staltz/fabrica/releases/latest")!) == nil)
    }

    @Test func readsTheChecksumFile() {
        let sum = "3c9ec03f22e190c65048febb943f0e144ead4bfc3e449f9ec9bbde3c0a9b12d5"
        #expect(Release.checksum(in: "\(sum)  Fabrica-macos.zip\n", for: "Fabrica-macos.zip") == sum)
        #expect(Release.checksum(in: "\(sum.uppercased()) *Fabrica-macos.zip", for: "Fabrica-macos.zip") == sum)
        #expect(Release.checksum(in: "\(sum)  fabrica-darwin-arm64\n", for: "Fabrica-macos.zip") == nil)
        #expect(Release.checksum(in: "not-a-sum  Fabrica-macos.zip", for: "Fabrica-macos.zip") == nil)
        #expect(Release.checksum(in: "", for: "Fabrica-macos.zip") == nil)
    }

    @Test func comparesVersions() {
        #expect(Release.isNewer("v1.3.0", than: "1.2.0"))
        #expect(Release.isNewer("1.10.0", than: "1.9.3"))
        #expect(Release.isNewer("2.0", than: "1.99.99"))
        #expect(Release.isNewer("1.2.1", than: "1.2"))
        #expect(Release.isNewer("1.3.0", than: "1.3.0-rc.1"))
        // A build made before any tag says 0.0.0.
        #expect(Release.isNewer("v0.1.0", than: "0.0.0"))
        #expect(!Release.isNewer("v1.2.0", than: "1.2.0"))
        #expect(!Release.isNewer("1.2", than: "1.2.0"))
        #expect(!Release.isNewer("1.2.0", than: "1.3.0"))
        #expect(!Release.isNewer("1.3.0-rc.1", than: "1.3.0"))
    }

    @Test func pointsAtTheReleaseFiles() {
        let r = Release(tag: "v1.3.0", checksum: "")
        #expect(r.version == "1.3.0")
        #expect(r.page.absoluteString == "https://github.com/staltz/fabrica/releases/tag/v1.3.0")
        #expect(r.download.absoluteString == "https://github.com/staltz/fabrica/releases/download/v1.3.0/Fabrica-macos.zip")
    }
}
