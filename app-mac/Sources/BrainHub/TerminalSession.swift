import AppKit
import BrainHubCore
import Observation

/// One hub terminal: a Ghostty surface running `brain-attach`, which holds the hub's websocket (the hub bridges it to
/// `tmux attach` on the machine) and reconnects by itself after a network drop. When brain-attach exits, this asks
/// the hub why: the terminal ended, or brain-attach could not connect and starts again after a pause.
@MainActor @Observable
final class TerminalSession {
    let ref: TerminalRef
    @ObservationIgnored let engine = TerminalEngine()
    /// What the pane shows over the terminal (offline, ended), or nil.
    private(set) var message: String?
    private(set) var ended = false
    /// A short note at the bottom of the pane: a paste being copied to the machine, or why it failed.
    private(set) var note: String?

    @ObservationIgnored private weak var model: AppModel?
    @ObservationIgnored private var retry: Task<Void, Never>?
    @ObservationIgnored private var tries = 0
    @ObservationIgnored private var startedAt = Date()
    @ObservationIgnored private var wanted = true

    init(ref: TerminalRef, model: AppModel) {
        self.ref = ref
        self.model = model
        engine.onBell = { [weak self] in if let self { self.model?.bell(self.ref) } }
        engine.onExit = { [weak self] in Task { await self?.exited() } }
        engine.onFiles = { [weak self] files in self?.upload(files) }
        connect()
    }

    /// `brain-attach --hub <url> <id>`, from the app bundle (Contents/MacOS), else from PATH (`swift run`).
    static func command(_ ref: TerminalRef) -> String {
        let bundled = Bundle.main.executableURL?.deletingLastPathComponent().appendingPathComponent("brain-attach").path
        let exe = bundled.flatMap { FileManager.default.isExecutableFile(atPath: $0) ? $0 : nil } ?? "brain-attach"
        return [exe, "--hub", ref.hub, String(ref.id)].map(shellQuote).joined(separator: " ")
    }

    func connect() {
        retry?.cancel()
        guard wanted else { return }
        message = nil
        startedAt = Date()
        engine.start(command: Self.command(ref))
    }

    private func exited() async {
        guard wanted else { return }
        if Date().timeIntervalSince(startedAt) > 10 { tries = 0 } // it ran: start the backoff again
        let host = model?.terminal(ref)?.host ?? "The machine"
        let status = try? await HubClient(base: ref.hub).get(Paths.status(ref.id), as: TerminalStatus.self, timeout: 5)
        guard wanted else { return }
        switch Wire.afterClose(code: 1000, reason: "", status: status, host: host, tries: tries) {
        case .ended(let text):
            ended = true
            message = text
        case .retry(let text, let after):
            message = text
            tries += 1
            retry = Task { [weak self] in
                try? await Task.sleep(for: .seconds(after))
                if !Task.isCancelled { self?.connect() }
            }
        }
    }

    /// Reconnect now (the Retry button, Reconnect, or after the Mac wakes): a new brain-attach, and tmux redraws.
    func reconnect() {
        guard !ended else { return }
        tries = 0
        connect()
    }

    func stop() {
        wanted = false
        retry?.cancel()
        engine.stop()
    }

    /// Put pasted or dropped files on the terminal's machine (the hub's uploads folder for this terminal), then paste
    /// their paths there: Claude Code and Codex attach an image when its path is pasted.
    private func upload(_ files: [PastedFile]) {
        guard let host = model?.terminal(ref)?.host else {
            show("The hub does not say which machine runs this terminal.")
            return
        }
        note = files.count == 1 ? "Copying \(files[0].name) to \(host)…" : "Copying \(files.count) files to \(host)…"
        Task {
            do {
                let client = try HubClient(base: ref.hub)
                var paths: [String] = []
                for f in files { paths.append(try await client.upload(Paths.upload(host: host, terminal: ref.id), name: f.name, data: f.data)) }
                note = nil
                engine.paste(pastedPaths(paths))
            } catch {
                show("Could not copy to \(host): \(error.localizedDescription)")
            }
        }
    }

    private func show(_ text: String) {
        note = text
        Task { [weak self] in
            try? await Task.sleep(for: .seconds(6))
            if self?.note == text { self?.note = nil }
        }
    }

    /// Bytes as if typed: send-prefix, copy-mode.
    func send(_ d: Data) { engine.send(d) }
}
