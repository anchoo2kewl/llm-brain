import AppKit
import Foundation

@MainActor
enum Updates {
    static var checking = false
    static var automatic: Bool { UserDefaults.standard.object(forKey: "automaticUpdates") as? Bool ?? true }

    static func check(manual: Bool) {
        guard !checking, manual || automatic,
              let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String,
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
            if manual || (result.0 && result.1.hasPrefix("Installed ")) {
                let alert = NSAlert()
                alert.messageText = result.0 ? "Brain Hub Updates" : "Update failed"
                alert.informativeText = result.1
                alert.runModal()
            }
        }
    }
}
