// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "BrainHub",
    platforms: [.macOS(.v14)],
    targets: [
        // Everything that is not UI: hub URLs, the API, websocket frames, the saved hub list.
        .target(name: "BrainHubCore"),
        // libghostty, built from the commit in ghostty.commit by CI; ./fetch-ghostty.sh puts it here.
        .binaryTarget(name: "GhosttyKit", path: "Vendor/GhosttyKit.xcframework"),
        .executableTarget(
            name: "BrainHub",
            dependencies: ["BrainHubCore", "GhosttyKit"],
            // Exercise native callback actor boundaries in debug and optimized debug tests.
            swiftSettings: [.unsafeFlags(["-enable-actor-data-race-checks"], .when(configuration: .debug))],
            linkerSettings: [
                .linkedLibrary("c++"),
                .linkedFramework("Carbon"),
                .linkedFramework("Metal"),
                .linkedFramework("QuartzCore"),
                .linkedFramework("CoreText"),
                .linkedFramework("IOSurface"),
            ]),
        .testTarget(name: "BrainHubCoreTests", dependencies: ["BrainHubCore"]),
        .testTarget(name: "BrainHubTests", dependencies: ["BrainHub"]),
    ]
)
