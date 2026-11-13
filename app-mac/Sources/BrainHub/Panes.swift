import AppKit
import BrainHubCore
import Observation
import SwiftUI

/// One window's panes and the sheets it shows.
@MainActor @Observable
final class WindowPanes {
    var tree = PaneTree()
    var showNew = false
    var showSearch = false
    @ObservationIgnored weak var window: NSWindow?
    private var model: AppModel { AppModel.shared }

    func show(_ ref: TerminalRef) { tree.show(ref) }

    func perform(_ action: PaneAction) {
        switch action {
        case .split(let axis): if !tree.split(axis) { NSSound.beep() }
        case .selectPane(let d): tree.focus(d)
        case .nextPane(let n): tree.focusNext(n)
        case .resize(let d, let n): tree.resize(d, cells: n)
        case .zoom: tree.toggleZoom()
        case .killPane: requestClose()
        case .newWindow: if model.hub != nil { showNew = true }
        case .nextWindow(let n):
            let refs = model.orderedRefs
            guard !refs.isEmpty else { return }
            let i = tree.focusedTerminal.flatMap { refs.firstIndex(of: $0) } ?? (n > 0 ? -1 : 0)
            show(refs[((i + n) % refs.count + refs.count) % refs.count])
        case .selectWindow(let n):
            let refs = model.orderedRefs, i = n - model.keys.baseIndex
            if refs.indices.contains(i) { show(refs[i]) }
        case .copyMode: focusedSession?.send(Data([0x02, 0x5b])) // the hub's own tmux runs with the default prefix, C-b
        case .sendPrefix: focusedSession?.send(Data(model.keys.prefix.bytes))
        }
    }

    /// Asks before closing a pane whose agent is still working.
    var confirmClose = false

    /// Cmd+W, tmux kill-pane: close the focused pane, or the window when it is the last one. Only the view goes: the
    /// hub terminal keeps running (End Terminal stops it).
    func requestClose() {
        if let ref = tree.focusedTerminal, model.state(ref) == .working { confirmClose = true } else { closeNow() }
    }

    func closeNow() {
        if tree.panes.count == 1 { (window ?? NSApp.keyWindow)?.close() } else { tree.close() }
    }

    var focusedSession: TerminalSession? { tree.focusedTerminal.map { model.session($0) } }
}

struct WeakPanes { weak var value: WindowPanes? }

/// Routes keys and clicks before the terminal sees them: the tmux prefix and bindings, and click-to-focus a pane.
@MainActor
enum KeyRouter {
    private static var repeatUntil = Date.distantPast

    static func install() {
        NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .leftMouseDown]) { event in
            nonisolated(unsafe) let e = event // local monitors run on the main thread
            let pass = MainActor.assumeIsolated { e.type == .keyDown ? key(e) : click(e) }
            return pass ? event : nil
        }
    }

    /// True: let the event through.
    private static func click(_ event: NSEvent) -> Bool {
        let model = AppModel.shared
        guard let win = event.window, let hit = win.contentView?.hitTest(win.contentView!.convert(event.locationInWindow, from: nil)),
              let panes = model.windows.lazy.compactMap(\.value).first(where: { $0.window === win }) else { return true }
        var v: NSView? = hit
        while let cur = v {
            if let ref = model.sessions.first(where: { $0.value.engine.view === cur })?.key {
                if let pane = panes.tree.pane(showing: ref) { panes.tree.focus(pane) }
                break
            }
            v = cur.superview
        }
        return true
    }

    private static func key(_ event: NSEvent) -> Bool {
        let model = AppModel.shared
        // Menus keep Cmd keys; text fields (sheets, Settings, the sidebar's rename) keep everything.
        guard !event.modifierFlags.contains(.command), let panes = model.keyPanes,
              !(event.window?.firstResponder is NSTextView), let key = Key(event: event) else { return true }
        let keys = model.keys
        if model.prefixArmed {
            model.prefixArmed = false
            if let b = keys.binding(key, root: false) {
                run(b, panes)
            } else if key == keys.prefix {
                panes.perform(.sendPrefix) // prefix twice sends it through
            }
            return false // tmux swallows an unbound key after the prefix
        }
        if Date() < repeatUntil, let b = keys.binding(key, root: false), b.repeats {
            run(b, panes)
            return false
        }
        if key == keys.prefix {
            model.prefixArmed = true
            return false
        }
        if let b = keys.binding(key, root: true) {
            run(b, panes)
            return false
        }
        return true
    }

    private static func run(_ b: KeyBinding, _ panes: WindowPanes) {
        repeatUntil = b.repeats ? Date().addingTimeInterval(0.5) : .distantPast // tmux's repeat-time
        panes.perform(b.action)
    }
}

extension Key {
    /// The tmux name of a key press.
    init?(event: NSEvent) {
        let f = event.modifierFlags
        let named: [UInt16: String] = [123: "Left", 124: "Right", 125: "Down", 126: "Up", 36: "Enter", 76: "Enter", 48: "Tab",
                                       49: "Space", 51: "BSpace", 53: "Escape", 115: "Home", 119: "End", 116: "PageUp",
                                       121: "PageDown", 117: "DC", 122: "F1", 120: "F2", 99: "F3", 118: "F4", 96: "F5",
                                       97: "F6", 98: "F7", 100: "F8", 101: "F9", 109: "F10", 103: "F11", 111: "F12"]
        if let n = named[event.keyCode] {
            self.init(name: n, ctrl: f.contains(.control), meta: f.contains(.option), shift: f.contains(.shift))
            return
        }
        // Shift is already in the character ("%"); Option is Meta, so take the key without it.
        guard let ch = event.charactersIgnoringModifiers, ch.count == 1 else { return nil }
        self.init(name: ch, ctrl: f.contains(.control), meta: f.contains(.option))
    }
}

/// A pane tree drawn as nested splits with draggable borders.
struct PaneTreeView: View {
    @Bindable var panes: WindowPanes

    var body: some View {
        if let z = panes.tree.zoomed {
            PaneLeaf(panes: panes, id: z)
        } else {
            node(panes.tree.root)
        }
    }

    private func node(_ n: PaneTree.Node) -> AnyView {
        switch n {
        case .leaf(let id):
            return AnyView(PaneLeaf(panes: panes, id: id))
        case let .split(id, axis, ratio, a, b):
            return AnyView(GeometryReader { geo in
                let total = axis == .horizontal ? geo.size.width : geo.size.height
                let first = max(total * ratio - 0.5, 0)
                let layout = axis == .horizontal ? AnyLayout(HStackLayout(spacing: 0)) : AnyLayout(VStackLayout(spacing: 0))
                layout {
                    node(a).frame(width: axis == .horizontal ? first : nil, height: axis == .vertical ? first : nil)
                    Rectangle().fill(.separator)
                        .frame(width: axis == .horizontal ? 1 : nil, height: axis == .vertical ? 1 : nil)
                        .contentShape(Rectangle().inset(by: -3))
                        .onHover { inside in
                            if inside { (axis == .horizontal ? NSCursor.resizeLeftRight : NSCursor.resizeUpDown).push() } else { NSCursor.pop() }
                        }
                        .gesture(DragGesture(coordinateSpace: .named("split\(id)")).onChanged { g in
                            let at = axis == .horizontal ? g.location.x : g.location.y
                            if total > 0 { panes.tree.setRatio(split: id, at / total) }
                        })
                    node(b)
                }
                .coordinateSpace(.named("split\(id)"))
            })
        }
    }
}

struct PaneLeaf: View {
    @Bindable var panes: WindowPanes
    let id: Int
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let focused = panes.tree.focused == id
        let many = panes.tree.panes.count > 1
        Group {
            if let ref = panes.tree.terminals[id] {
                TerminalPane(session: model.session(ref), focused: focused).id(ref)
            } else {
                ContentUnavailableView {
                    Label(model.hub == nil ? "No hub yet" : "Empty pane", systemImage: "terminal")
                } description: {
                    Text(model.hub == nil ? "Add your hub in Settings (⌘,)."
                         : "Pick a terminal on the left, start one with ⌘T, or search with ⌘K. \(model.keys.prefix.description) then \(splitHint) splits.")
                } actions: {
                    if model.hub == nil { SettingsLink { Text("Open Settings") } } else { Button("New Terminal") { panes.showNew = true } }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .contentShape(Rectangle())
                .onTapGesture { panes.tree.focus(id) }
            }
        }
        .overlay { if many && focused { Rectangle().strokeBorder(Color.accentColor.opacity(0.7), lineWidth: 2).allowsHitTesting(false) } }
        .overlay(alignment: .bottomTrailing) {
            if focused && model.prefixArmed {
                Text(model.keys.prefix.description).font(.caption.monospaced()).padding(.horizontal, 8).padding(.vertical, 3)
                    .background(.orange, in: Capsule()).foregroundStyle(.black).padding(8)
            }
        }
    }

    private var splitHint: String {
        let s = model.keys.bindings.filter { !$0.root && ($0.action == .split(.horizontal) || $0.action == .split(.vertical)) }
        return s.map(\.key.description).joined(separator: " or ")
    }
}

/// The terminal and, over it, why it is not connected.
struct TerminalPane: View {
    let session: TerminalSession
    let focused: Bool

    var body: some View {
        TerminalHost(engine: session.engine, focused: focused)
            .overlay(alignment: .top) {
                if let msg = session.message {
                    HStack(spacing: 8) {
                        if !session.ended { ProgressView().controlSize(.small) }
                        Text(msg)
                        if !session.ended { Button("Retry Now") { session.reconnect() }.buttonStyle(.link) }
                    }
                    .padding(.horizontal, 12).padding(.vertical, 6)
                    .background(.regularMaterial, in: Capsule())
                    .padding(.top, 8)
                }
            }
            .background(SwiftUI.Color(nsColor: session.engine.background))
    }
}

/// Hosts a shared terminal view. A terminal shown in a second window moves there.
struct TerminalHost: NSViewRepresentable {
    let engine: TerminalEngine
    let focused: Bool

    func makeNSView(context: Context) -> NSView {
        let box = NSView()
        attach(to: box)
        return box
    }

    func updateNSView(_ box: NSView, context: Context) {
        if engine.view.superview !== box { attach(to: box) }
        if focused { DispatchQueue.main.async { [engine] in engine.focus() } }
    }

    private func attach(to box: NSView) {
        let view = engine.view
        view.removeFromSuperview()
        view.translatesAutoresizingMaskIntoConstraints = false
        box.addSubview(view)
        NSLayoutConstraint.activate([
            view.leadingAnchor.constraint(equalTo: box.leadingAnchor, constant: 6),
            view.trailingAnchor.constraint(equalTo: box.trailingAnchor),
            view.topAnchor.constraint(equalTo: box.topAnchor, constant: 4),
            view.bottomAnchor.constraint(equalTo: box.bottomAnchor),
        ])
    }
}

/// Gives a SwiftUI view its NSWindow.
struct WindowReader: NSViewRepresentable {
    var found: (NSWindow) -> Void
    func makeNSView(context: Context) -> NSView {
        let v = NSView()
        DispatchQueue.main.async { if let w = v.window { found(w) } }
        return v
    }
    func updateNSView(_ v: NSView, context: Context) {
        DispatchQueue.main.async { if let w = v.window { found(w) } }
    }
}
