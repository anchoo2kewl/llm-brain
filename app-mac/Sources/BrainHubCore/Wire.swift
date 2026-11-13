import Foundation

/// The terminal websocket protocol (hub/internal/web/terminals.go): binary frames are terminal bytes both ways,
/// text frames are JSON control, and close code 4000 carries a reason to show.
public enum Wire {
    public static let reasonCode = 4000

    /// The text frame that tells tmux the window size.
    public static func resize(cols: Int, rows: Int) -> String { #"{"t":"resize","cols":\#(cols),"rows":\#(rows)}"# }

    /// What to do after the socket closed, from the close reason and `/terminals/{id}/status`
    /// (nil when the hub did not answer). Mirrors the hub's own terminal page.
    public enum Next: Equatable, Sendable {
        case retry(message: String, after: Double)
        case ended(message: String)
    }

    public static func afterClose(code: Int, reason: String, status: TerminalStatus?, host: String, tries: Int) -> Next {
        let why = code == reasonCode ? reason : ""
        let wait = Double(min(max(tries, 1), 10))
        guard let status else { return .retry(message: "The hub is unreachable. Retrying…", after: wait) }
        if status.status == "starting" { return .retry(message: "Starting…", after: 1.5) }
        if status.status != "live" {
            let detail = [why.isEmpty ? "The terminal has \(status.status)." : why, status.error ?? ""]
            return .ended(message: detail.filter { !$0.isEmpty }.joined(separator: " "))
        }
        if status.hostLive == false { return .retry(message: "\(host) is offline. Retrying…", after: wait) }
        if why.isEmpty || why == "detached" { return .retry(message: "Reconnecting…", after: tries == 0 ? 0.2 : wait) }
        return .retry(message: "\(why). Retrying…", after: wait)
    }
}
