// swift-tools-version: 6.0
//
// Fabrica.app, the macOS companion to the fabrica CLI. `make app` in the
// repo root builds the .app bundle (with the CLI inside it); `swift run`
// here starts the app straight from the build folder, using the fabrica on
// your PATH.

import PackageDescription

let package = Package(
    name: "Fabrica",
    platforms: [.macOS(.v15)],
    dependencies: [
        .package(url: "https://github.com/jpsim/Yams.git", from: "6.0.0"),
    ],
    targets: [
        .executableTarget(
            name: "Fabrica",
            dependencies: ["Yams"],
            path: "Sources/Fabrica",
            linkerSettings: [.linkedLibrary("sqlite3")]
        ),
        .testTarget(
            name: "FabricaTests",
            dependencies: ["Fabrica"],
            path: "Tests/FabricaTests"
        ),
    ]
)
