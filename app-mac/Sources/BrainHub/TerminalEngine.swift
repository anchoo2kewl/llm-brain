import AppKit
import SwiftTerm

/// The terminal emulator behind a pane. Everything else (panes, tmux keys, notifications, the hub connection) talks to
/// this protocol only, so another engine (for example libghostty) can replace SwiftTerm by adding one conforming class.
/// This is the only file that imports SwiftTerm.
@MainActor
protocol TerminalEngine: AnyObject {
    /// The view to put in a pane. It handles selection, Cmd+C and Cmd+V itself, through the responder chain.
    var view: NSView { get }
    var background: NSColor { get }
    /// Bytes from the remote side, to draw.
    func feed(_ bytes: Data)
    /// Make it the window's first responder.
    func focus()

    /// Bytes the person typed or pasted, to send to the remote side.
    var onInput: ((Data) -> Void)? { get set }
    /// The grid size changed (columns, rows).
    var onResize: ((Int, Int) -> Void)? { get set }
    /// The remote side rang the bell.
    var onBell: (() -> Void)? { get set }
    /// The remote side set the window title.
    var onTitle: ((String) -> Void)? { get set }
}

/// SwiftTerm's macOS view as a TerminalEngine.
final class SwiftTermEngine: TerminalEngine, TerminalViewDelegate {
    private let term: TerminalView
    var view: NSView { term }
    var background: NSColor { term.nativeBackgroundColor }
    var onInput: ((Data) -> Void)?
    var onResize: ((Int, Int) -> Void)?
    var onBell: (() -> Void)?
    var onTitle: ((String) -> Void)?

    init() {
        term = TerminalView(frame: NSRect(x: 0, y: 0, width: 800, height: 500),
                            font: NSFont.monospacedSystemFont(ofSize: 13, weight: .regular))
        term.optionAsMetaKey = true
        // The hub's tmux runs with `mouse on`: the wheel scrolls its 100k-line history, a drag selects there and tmux
        // copies it to this Mac's clipboard (OSC 52, see clipboardCopy). Shift-drag selects locally for Cmd+C.
        term.allowMouseReporting = true
        let dark = NSApp.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
        term.nativeBackgroundColor = dark ? NSColor(red: 0.05, green: 0.07, blue: 0.09, alpha: 1) : .white
        term.nativeForegroundColor = dark ? NSColor(red: 0.90, green: 0.93, blue: 0.95, alpha: 1) : NSColor(white: 0.12, alpha: 1)
        term.terminalDelegate = self
    }

    func feed(_ bytes: Data) { term.feed(byteArray: ArraySlice(bytes)) }

    func focus() {
        if term.window?.firstResponder !== term { term.window?.makeFirstResponder(term) }
    }

    func send(source: TerminalView, data: ArraySlice<UInt8>) { onInput?(Data(data)) }
    func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) { onResize?(newCols, newRows) }
    func bell(source: TerminalView) { onBell?() }
    func setTerminalTitle(source: TerminalView, title: String) { onTitle?(title) }

    /// tmux copies a selection with OSC 52 (`set-clipboard on`).
    func clipboardCopy(source: TerminalView, content: Data) {
        guard let text = String(data: content, encoding: .utf8) else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }

    func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) {}
    func scrolled(source: TerminalView, position: Double) {}
    func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
}
