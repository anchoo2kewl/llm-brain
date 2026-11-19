import AppKit
import SwiftUI

/// Bounded local diagnostics. Never records terminal contents or credentials.
@MainActor
enum AppDiagnostics {
    static let directory = FileManager.default.urls(for: .libraryDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("Logs/BrainHub", isDirectory: true)
    static let log = directory.appendingPathComponent("app.log")

    static func record(_ event: String) {
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            if let size = try? log.resourceValues(forKeys: [.fileSizeKey]).fileSize, size > 1_048_576 {
                let previous = directory.appendingPathComponent("app.previous.log")
                if FileManager.default.fileExists(atPath: previous.path) { try FileManager.default.removeItem(at: previous) }
                try FileManager.default.moveItem(at: log, to: previous)
            }
            if !FileManager.default.fileExists(atPath: log.path) {
                FileManager.default.createFile(atPath: log.path, contents: nil, attributes: [.posixPermissions: 0o600])
            }
            let handle = try FileHandle(forWritingTo: log)
            defer { try? handle.close() }
            try handle.seekToEnd()
            try handle.write(contentsOf: Data("\(Date().ISO8601Format()) \(event)\n".utf8))
        } catch { NSLog("Brain Hub diagnostics write failed: %@", error.localizedDescription) }
    }

    static var reports: [URL] {
        let directories = [
            directory,
            FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/DiagnosticReports"),
            URL(fileURLWithPath: "/Library/Logs/DiagnosticReports"),
            FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".local/state/ghostty/crash")
        ]
        return directories.flatMap { directory in
            (try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: [.contentModificationDateKey])) ?? []
        }.filter { url in
            if url.deletingLastPathComponent() == directory { return url.pathExtension == "log" }
            if url.pathExtension == "ghosttycrash" { return nativeEvent(url) != nil }
            return ["ips", "crash"].contains(url.pathExtension)
                && (url.lastPathComponent.lowercased().hasPrefix("brain") || url.lastPathComponent.hasPrefix("hub-"))
        }.sorted { $0.lastPathComponent > $1.lastPathComponent }
    }

    private static func nativeEvent(_ url: URL) -> [String: Any]? {
        guard let handle = try? FileHandle(forReadingFrom: url) else { return nil }
        defer { try? handle.close() }
        let data = (try? handle.read(upToCount: 1_048_576)) ?? Data()
        let lines = data.split(separator: 10, maxSplits: 3, omittingEmptySubsequences: false)
        guard lines.count >= 3,
              let event = try? JSONSerialization.jsonObject(with: Data(lines[2])) as? [String: Any],
              let debug = event["debug_meta"] as? [String: Any],
              let images = debug["images"] as? [[String: Any]],
              images.contains(where: { ($0["code_file"] as? String)?.hasSuffix("/Brain Hub.app/Contents/MacOS/BrainHub") == true })
        else { return nil }
        return event
    }

    static func read(_ url: URL) -> String {
        guard reports.contains(url), let handle = try? FileHandle(forReadingFrom: url) else { return "Report unavailable." }
        defer { try? handle.close() }
        let data = (try? handle.read(upToCount: 1_048_576)) ?? Data()
        if url.pathExtension == "ghosttycrash" {
            guard let event = nativeEvent(url),
                  let json = try? JSONSerialization.data(withJSONObject: event, options: [.prettyPrinted, .sortedKeys])
            else { return "Native crash report metadata unavailable. Open the original report folder to inspect the dump." }
            return String(decoding: json, as: UTF8.self)
        }
        return String(decoding: data, as: UTF8.self)
    }
}

struct DiagnosticsView: View {
    @ViewState private var reports: [URL] = []
    @ViewState private var selection: URL?
    @ViewState private var contents = ""

    var body: some View {
        VStack(alignment: .leading) {
            HStack {
                Button("Refresh") { reload() }
                Button("Open Log Folder") { NSWorkspace.shared.open(AppDiagnostics.directory) }
                Button("Open Crash Reports") {
                    NSWorkspace.shared.open(FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Logs/DiagnosticReports"))
                }
                Button("Open Native Crash Reports") {
                    NSWorkspace.shared.open(FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".local/state/ghostty/crash"))
                }
                Spacer()
            }
            HSplitView {
                List(reports, id: \.self, selection: $selection) { report in
                    Text(report.lastPathComponent).tag(report)
                }.frame(minWidth: 220)
                ScrollView {
                    Text(contents.isEmpty ? "Select a log or crash report." : contents)
                        .font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading).padding(8)
                }.frame(minWidth: 420)
            }
        }
        .padding(12)
        .onAppear { reload() }
        .onChange(of: selection) { contents = selection.map(AppDiagnostics.read) ?? "" }
        .frame(minWidth: 720, minHeight: 420)
    }

    private func reload() { reports = AppDiagnostics.reports }
}
