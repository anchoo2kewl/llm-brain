import AppKit
import BrainHubCore
import Observation

/// One hub terminal: a terminal engine on the hub's websocket, which the hub bridges to `tmux attach` on the machine.
/// It reconnects by itself when the network or the machine comes back, and stops when the terminal ends.
@MainActor @Observable
final class TerminalSession {
    let ref: TerminalRef
    @ObservationIgnored let engine: TerminalEngine
    /// What the pane shows over the terminal (connecting, offline, ended), or nil.
    private(set) var message: String?
    private(set) var ended = false

    @ObservationIgnored private weak var model: AppModel?
    @ObservationIgnored private var socket: URLSessionWebSocketTask?
    @ObservationIgnored private var tasks: [Task<Void, Never>] = []
    @ObservationIgnored private var retry: Task<Void, Never>?
    @ObservationIgnored private var tries = 0
    @ObservationIgnored private var wanted = true
    @ObservationIgnored private var queued: [Data] = []
    @ObservationIgnored private var lastPong = Date()
    /// The grid size the engine last reported (sizeChanged); the hub's default until the view is laid out.
    @ObservationIgnored private var cols = 120
    @ObservationIgnored private var rows = 40

    init(ref: TerminalRef, model: AppModel) {
        self.ref = ref
        self.model = model
        engine = SwiftTermEngine()
        engine.onInput = { [weak self] in self?.send($0) }
        engine.onResize = { [weak self] c, r in
            guard let self else { return }
            (cols, rows) = (max(c, 2), max(r, 2))
            sendResize()
        }
        engine.onBell = { [weak self] in if let self { self.model?.bell(self.ref) } }
        connect()
    }

    // MARK: connection

    func connect() {
        retry?.cancel()
        guard wanted else { return }
        message = tries == 0 ? "Connecting…" : message
        guard let url = try? HubURL.terminalSocket(ref.hub, id: ref.id, cols: cols, rows: rows) else {
            message = "This hub address is not allowed."
            return
        }
        // URLSession sends no Origin header, which the hub accepts from a native client.
        let ws = HubClient.session.webSocketTask(with: URLRequest(url: url))
        ws.maximumMessageSize = 32 << 20
        socket = ws
        lastPong = Date()
        ws.resume()
        tasks.forEach { $0.cancel() }
        tasks = [Task { await self.read(ws) }, Task { await self.keepAlive(ws) }]
    }

    private func read(_ ws: URLSessionWebSocketTask) async {
        var first = true
        while true {
            let msg: URLSessionWebSocketTask.Message
            do { msg = try await ws.receive() } catch { break }
            guard ws === socket else { return }
            if first {
                first = false
                tries = 0
                message = nil
                sendResize()
                let q = queued
                queued = []
                for d in q { ws.send(.data(d)) { _ in } }
            }
            if case .data(let d) = msg { engine.feed(d) } // text frames are control: ignored
        }
        guard ws === socket else { return }
        let reason = ws.closeReason.flatMap { String(data: $0, encoding: .utf8) } ?? ""
        // CloseCode has no case for 4000; the hub sends a reason only with 4000.
        await closed(code: reason.isEmpty ? ws.closeCode.rawValue : Wire.reasonCode, reason: reason)
    }

    /// A dropped network often leaves the socket open with nothing on it: ping every 15 s, give up when no pong came.
    private func keepAlive(_ ws: URLSessionWebSocketTask) async {
        while !Task.isCancelled {
            let sent = Date()
            ws.sendPing { [weak self] err in
                if err == nil { Task { @MainActor in self?.lastPong = Date() } }
            }
            try? await Task.sleep(for: .seconds(15))
            guard ws === socket, !Task.isCancelled else { return }
            if lastPong < sent { ws.cancel(with: .goingAway, reason: nil) }
        }
    }

    private func closed(code: Int, reason: String) async {
        socket = nil
        guard wanted else { return }
        let host = model?.terminal(ref)?.host ?? "The machine"
        let status = try? await HubClient(base: ref.hub).get(Paths.status(ref.id), as: TerminalStatus.self, timeout: 5)
        guard wanted, socket == nil else { return }
        switch Wire.afterClose(code: code, reason: reason, status: status, host: host, tries: tries) {
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

    /// Reconnect now (the Retry button, or after the Mac wakes).
    func reconnect() {
        guard !ended else { return }
        tries = 0
        socket?.cancel(with: .goingAway, reason: nil)
        socket = nil
        connect()
    }

    func stop() {
        wanted = false
        retry?.cancel()
        tasks.forEach { $0.cancel() }
        socket?.cancel(with: .normalClosure, reason: nil)
        socket = nil
    }

    private func sendResize() {
        socket?.send(.string(Wire.resize(cols: cols, rows: rows))) { _ in }
    }

    /// Bytes as if typed: the engine's input, send-prefix, copy-mode.
    func send(_ d: Data) {
        if let socket, message == nil { socket.send(.data(d)) { _ in } } else if queued.count < 500 { queued.append(d) }
    }
}
