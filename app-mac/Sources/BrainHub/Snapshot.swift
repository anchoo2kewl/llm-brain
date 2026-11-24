import AppKit

/// For docs and PR screenshots without Screen Recording permission: `BRAINHUB_SNAPSHOT=/path/shot.png` makes the app
/// draw its front window (with its toolbar) into that PNG a few seconds after launch, then keep running. The Metal
/// terminal layer is not in the image.
@MainActor
enum Snapshot {
    static func scheduleIfAsked() {
        guard let path = ProcessInfo.processInfo.environment["BRAINHUB_SNAPSHOT"], !path.isEmpty else { return }
        let delay = Double(ProcessInfo.processInfo.environment["BRAINHUB_SNAPSHOT_DELAY"] ?? "") ?? 5
        DispatchQueue.main.asyncAfter(deadline: .now() + delay) { write(to: URL(fileURLWithPath: path)) }
    }

    static func write(to url: URL) {
        guard let window = NSApp.windows.first(where: { $0.isVisible && $0.contentView != nil && $0.level == .normal }),
              let frame = window.contentView?.superview ?? window.contentView,
              let rep = frame.bitmapImageRepForCachingDisplay(in: frame.bounds) else { return NSLog("Brain Hub: no window to snapshot") }
        frame.cacheDisplay(in: frame.bounds, to: rep)
        do { try rep.representation(using: .png, properties: [:])?.write(to: url) } catch { NSLog("Brain Hub: snapshot failed: %@", error.localizedDescription) }
    }
}
