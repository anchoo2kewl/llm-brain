// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "BrainHub",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(url: "https://github.com/migueldeicaza/SwiftTerm.git", exact: "1.99.3"),
    ],
    targets: [
        // Everything that is not UI: hub URLs, the API, websocket frames, the saved hub list.
        .target(name: "BrainHubCore"),
        .executableTarget(name: "BrainHub", dependencies: ["BrainHubCore", .product(name: "SwiftTerm", package: "SwiftTerm")]),
        .testTarget(name: "BrainHubCoreTests", dependencies: ["BrainHubCore"]),
    ]
)
