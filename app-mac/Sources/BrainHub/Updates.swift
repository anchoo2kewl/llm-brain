import AppKit
import BrainHubCore
import Foundation

@MainActor
enum Updates {
    static var checking = false
    static var automatic: Bool { UserDefaults.standard.object(forKey: "automaticUpdates") as? Bool ?? true }
    static let running = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String

    static func check(manual: Bool) {
        guard !checking, manual || automatic, let version = running,
              let helper = Bundle.main.url(forAuxiliaryExecutable: "brain-update") else { return }
        checking = true
        let path = Bundle.main.bundleURL.path
        Task {
            let result = await Task.detached(priority: .utility) {
                let process = Process(), pipe = Pipe()
                process.executableURL = helper
                process.arguments = [version, path]
                process.standardOutput = pipe
                process.standardError = pipe
                do {
                    try process.run()
                    let data = pipe.fileHandleForReading.readDataToEndOfFile()
                    process.waitUntilExit()
                    return (process.terminationStatus == 0, String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines))
                } catch { return (false, error.localizedDescription) }
            }.value
            checking = false
            refreshRestart()
            // A background install shows only the toolbar's restart button, never a dialog.
            if manual {
                let alert = NSAlert()
                alert.messageText = result.0 ? "Brain Hub Updates" : "Update failed"
                alert.informativeText = result.1
                alert.runModal()
            }
        }
    }

    /// Compare the version on disk with the running one. Bundle.main caches its Info.plist, so read the file.
    static func refreshRestart() {
        let plist = Bundle.main.bundleURL.appendingPathComponent("Contents/Info.plist")
        let installed = NSDictionary(contentsOf: plist)?["CFBundleShortVersionString"] as? String
        let next = Versions.restart(running: running, installed: installed)
        if AppModel.shared.restartVersion != next { AppModel.shared.restartVersion = next }
    }

    /// Open the new version once this process has ended: the single-instance check sends a second copy away while
    /// this one runs. The path and the process id go to the shell as arguments, never as script text.
    static func restart() {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/sh")
        process.arguments = ["-c", #"while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec /usr/bin/open "$2""#, "sh",
                             String(ProcessInfo.processInfo.processIdentifier), Bundle.main.bundleURL.path]
        do {
            try process.run()
            NSApp.terminate(nil)
        } catch {
            let alert = NSAlert()
            alert.messageText = "Restart failed"
            alert.informativeText = error.localizedDescription
            alert.runModal()
        }
    }
}
