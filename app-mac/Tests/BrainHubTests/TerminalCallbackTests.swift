import AppKit
import Dispatch
import Testing
@testable import BrainHub

// Native C libraries invoke function pointers without Swift's Sendable annotations.
private struct WakeupReference: @unchecked Sendable {
    let call: @convention(c) (UnsafeMutableRawPointer?) -> Void
}

@MainActor
@Suite(.serialized) struct TerminalCallbackTests {
    @Test func repeatedNativeStartExitAndStop() async {
        _ = NSApplication.shared
        let engine = TerminalEngine()
        engine.view.frame = NSRect(x: 0, y: 0, width: 640, height: 360)
        for _ in 0..<30 {
            engine.start(command: "/usr/bin/true")
            try? await Task.sleep(for: .milliseconds(30))
            engine.stop()
            #expect(engine.view.subviews.isEmpty)
        }
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            DispatchQueue.main.async { continuation.resume() }
        }
    }

    @Test func wakeupFromNativeWorkerThreads() async {
        _ = NSApplication.shared
        let wakeup = WakeupReference(call: ghosttyWakeup)
        await withTaskGroup(of: Void.self) { workers in
            for _ in 0..<8 {
                workers.addTask {
                    for _ in 0..<2000 { wakeup.call(nil) }
                }
            }
        }
        // Drain the main-queue work before the test process shuts down.
        await withCheckedContinuation { (continuation: CheckedContinuation<Void, Never>) in
            DispatchQueue.main.async { continuation.resume() }
        }
    }
}
