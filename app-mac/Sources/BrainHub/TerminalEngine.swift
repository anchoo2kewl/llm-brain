import AppKit
import GhosttyKit

/// The terminal behind a pane: a libghostty surface that runs `brain-attach --hub <url> <id>` in its own pty.
/// brain-attach owns the hub websocket (bytes, resize, reconnect after a network drop); Ghostty draws and handles
/// keys, IME, mouse, selection and the clipboard. This is the only file that imports GhosttyKit.
///
/// `view` stays the same for the life of the pane; `start` replaces the surface inside it (Reconnect).
@MainActor
final class TerminalEngine {
    let view = NSView()
    var background: NSColor { Ghostty.shared.background }
    /// The remote side rang the bell.
    var onBell: (() -> Void)?
    /// The command (brain-attach) exited and the surface closed.
    var onExit: (() -> Void)?

    private var surface: SurfaceView?

    /// Run `command` (a /bin/sh command line) in a new surface, replacing the current one.
    func start(command: String) {
        stop()
        let s = SurfaceView(command: command, frame: view.bounds)
        s.autoresizingMask = [.width, .height]
        s.onBell = { [weak self] in self?.onBell?() }
        s.onClose = { [weak self, weak s] in
            guard let self, let s, s === surface else { return }
            stop()
            onExit?()
        }
        view.addSubview(s)
        surface = s
    }

    func stop() {
        guard let s = surface else { return }
        surface = nil
        let had = s.window?.firstResponder === s
        s.removeFromSuperview()
        s.free()
        if had { view.window?.makeFirstResponder(nil) }
    }

    /// Make it the window's first responder.
    func focus() {
        if let s = surface, s.window?.firstResponder !== s { s.window?.makeFirstResponder(s) }
    }

    /// Bytes as if typed (tmux send-prefix, copy-mode). Ghostty's `text:` action writes them to the pty as they are,
    /// not as a bracketed paste, so tmux sees keys.
    func send(_ bytes: Data) {
        guard let s = surface?.surface else { return }
        let action = "text:" + bytes.map { String(format: "\\x%02x", $0) }.joined()
        _ = ghostty_surface_binding_action(s, action, UInt(action.utf8.count))
    }
}

/// The libghostty app: one per process, shared by every surface.
@MainActor
final class Ghostty {
    static let shared = Ghostty()
    private(set) var app: ghostty_app_t?
    private(set) var background = NSColor(red: 0.05, green: 0.07, blue: 0.09, alpha: 1)

    private init() {
        guard ghostty_init(UInt(CommandLine.argc), CommandLine.unsafeArgv) == GHOSTTY_SUCCESS,
              let config = ghostty_config_new() else { return }
        // The user's Ghostty config (~/.config/ghostty/config and the macOS app's file) sets font, theme and colors;
        // then this app's own rules: its menus and tmux keys own the shortcuts, and Option is Meta.
        ghostty_config_load_default_files(config)
        ghostty_config_load_recursive_files(config)
        let own = FileManager.default.temporaryDirectory.appendingPathComponent("brainhub-ghostty.conf")
        try? """
            keybind = clear
            macos-option-as-alt = true
            confirm-close-surface = false
            clipboard-paste-protection = false
            clipboard-read = deny
            shell-integration = none
            """.write(to: own, atomically: true, encoding: .utf8)
        ghostty_config_load_file(config, own.path)
        ghostty_config_finalize(config)
        var c = ghostty_config_color_s()
        if ghostty_config_get(config, &c, "background", UInt("background".utf8.count)) {
            background = NSColor(srgbRed: CGFloat(c.r) / 255, green: CGFloat(c.g) / 255, blue: CGFloat(c.b) / 255, alpha: 1)
        }

        var runtime = ghostty_runtime_config_s(
            userdata: nil,
            supports_selection_clipboard: false,
            wakeup_cb: { _ in DispatchQueue.main.async { MainActor.assumeIsolated { Ghostty.shared.tick() } } },
            action_cb: { _, target, action in MainActor.assumeIsolated { Ghostty.action(target, action) } },
            read_clipboard_cb: { ud, loc, state, mimes, count, _ in
                MainActor.assumeIsolated { Ghostty.readClipboard(ud, loc, state, mimes, count) }
            },
            confirm_read_clipboard_cb: { ud, _, state, _ in
                // Paste protection is off and OSC 52 reads are denied in the config: nothing should ask.
                MainActor.assumeIsolated {
                    if let s = SurfaceView.from(ud)?.surface { ghostty_surface_deny_clipboard_request(s, state) }
                }
            },
            write_clipboard_cb: { _, loc, content, count, _ in
                // tmux copies a selection with OSC 52 (`set-clipboard on`).
                guard loc == GHOSTTY_CLIPBOARD_STANDARD else { return }
                for i in 0..<count where String(cString: content[i].mime) == "text/plain" {
                    let text = String(decoding: UnsafeRawBufferPointer(start: content[i].data, count: content[i].len),
                                      as: UTF8.self)
                    MainActor.assumeIsolated {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(text, forType: .string)
                    }
                }
            },
            close_surface_cb: { ud, _ in
                MainActor.assumeIsolated {
                    // Freeing the surface inside its own callback is not safe: close on the next turn.
                    let view = SurfaceView.from(ud)
                    DispatchQueue.main.async { MainActor.assumeIsolated { view?.onClose?() } }
                }
            })
        app = ghostty_app_new(&runtime, config)
        guard let app else { return }
        let dark = NSApp.effectiveAppearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
        ghostty_app_set_color_scheme(app, dark ? GHOSTTY_COLOR_SCHEME_DARK : GHOSTTY_COLOR_SCHEME_LIGHT)
        ghostty_app_set_focus(app, NSApp.isActive)
        let nc = NotificationCenter.default
        nc.addObserver(forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { if let a = Ghostty.shared.app { ghostty_app_set_focus(a, true) } }
        }
        nc.addObserver(forName: NSApplication.didResignActiveNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { if let a = Ghostty.shared.app { ghostty_app_set_focus(a, false) } }
        }
        nc.addObserver(forName: NSTextInputContext.keyboardSelectionDidChangeNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated { if let a = Ghostty.shared.app { ghostty_app_keyboard_changed(a) } }
        }
    }

    private func tick() { if let app { ghostty_app_tick(app) } }

    private static func action(_ target: ghostty_target_s, _ action: ghostty_action_s) -> Bool {
        let view = target.tag == GHOSTTY_TARGET_SURFACE
            ? target.target.surface.flatMap { SurfaceView.from(ghostty_surface_userdata($0)) } : nil
        switch action.tag {
        case GHOSTTY_ACTION_RING_BELL:
            view?.onBell?()
        case GHOSTTY_ACTION_OPEN_URL:
            let u = action.action.open_url
            let s = String(decoding: UnsafeRawBufferPointer(start: u.url, count: Int(u.len)), as: UTF8.self)
            // Only web links: the remote side controls this text.
            guard let url = URL(string: s), ["http", "https"].contains(url.scheme?.lowercased()) else { return false }
            NSWorkspace.shared.open(url)
        case GHOSTTY_ACTION_MOUSE_SHAPE:
            view?.cursorShape = action.action.mouse_shape
        default:
            return false
        }
        return true
    }

    private static func readClipboard(_ ud: UnsafeMutableRawPointer?, _ loc: ghostty_clipboard_e, _ state: UnsafeMutableRawPointer,
                                      _ mimes: UnsafePointer<UnsafePointer<CChar>>, _ count: Int) -> ghostty_clipboard_read_result_e {
        guard let s = SurfaceView.from(ud)?.surface, loc == GHOSTTY_CLIPBOARD_STANDARD else {
            return GHOSTTY_CLIPBOARD_READ_UNSUPPORTED
        }
        guard (0..<count).contains(where: { String(cString: mimes[$0]) == "text/plain" }),
              let text = NSPasteboard.general.string(forType: .string) else { return GHOSTTY_CLIPBOARD_READ_UNAVAILABLE }
        "text/plain".withCString { mime in
            text.withCString { data in
                var item = ghostty_clipboard_content_s(mime: mime, data: data, len: text.utf8.count)
                withUnsafePointer(to: &item) { items in
                    var done = ghostty_clipboard_complete_s(contents: items, contents_len: 1, available: nil, available_len: 0,
                                                            confirmed: false, remember: false)
                    ghostty_surface_complete_clipboard_request(s, &done, state)
                }
            }
        }
        return GHOSTTY_CLIPBOARD_READ_STARTED
    }
}

/// The NSView a Ghostty surface draws into (libghostty adds a Metal layer to it). Key, IME and mouse handling follow
/// Ghostty's own macOS app (macos/Sources/Ghostty/Surface View/SurfaceView_AppKit.swift), without its features this
/// app does not use.
@MainActor
final class SurfaceView: NSView, @preconcurrency NSTextInputClient {
    private(set) var surface: ghostty_surface_t?
    var onBell: (() -> Void)?
    var onClose: (() -> Void)?
    var cursorShape = GHOSTTY_MOUSE_SHAPE_TEXT {
        didSet { window?.invalidateCursorRects(for: self) }
    }

    private var markedText = NSMutableAttributedString()
    /// Set while keyDown runs interpretKeyEvents: text the input method committed during that key.
    private var keyTextAccumulator: [String]?
    private var tracking: NSTrackingArea?

    static func from(_ ud: UnsafeMutableRawPointer?) -> SurfaceView? {
        ud.map { Unmanaged<SurfaceView>.fromOpaque($0).takeUnretainedValue() }
    }

    init(command: String, frame: NSRect) {
        super.init(frame: frame)
        guard let app = Ghostty.shared.app else { return }
        var cfg = ghostty_surface_config_new()
        cfg.platform_tag = GHOSTTY_PLATFORM_MACOS
        cfg.platform = ghostty_platform_u(macos: ghostty_platform_macos_s(nsview: Unmanaged.passUnretained(self).toOpaque()))
        cfg.userdata = Unmanaged.passUnretained(self).toOpaque()
        cfg.scale_factor = Double(NSScreen.main?.backingScaleFactor ?? 2)
        cfg.context = GHOSTTY_SURFACE_CONTEXT_SPLIT
        surface = command.withCString { cmd in
            cfg.command = cmd
            return ghostty_surface_new(app, &cfg)
        }
    }

    required init?(coder: NSCoder) { fatalError("not used") }

    /// Stop the command and release the surface. The view does nothing after this.
    func free() {
        guard let s = surface else { return }
        surface = nil
        ghostty_surface_free(s)
    }

    // MARK: size, screen, visibility

    override var acceptsFirstResponder: Bool { true }

    override func setFrameSize(_ newSize: NSSize) {
        super.setFrameSize(newSize)
        updateSize()
    }

    private func updateSize() {
        guard let surface, bounds.width > 0, bounds.height > 0 else { return }
        let px = convertToBacking(bounds.size)
        ghostty_surface_set_size(surface, UInt32(px.width), UInt32(px.height))
    }

    override func viewDidChangeBackingProperties() {
        super.viewDidChangeBackingProperties()
        guard let surface, let window else { return }
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        layer?.contentsScale = window.backingScaleFactor
        CATransaction.commit()
        let fb = convertToBacking(frame)
        if frame.width > 0, frame.height > 0 {
            ghostty_surface_set_content_scale(surface, fb.width / frame.width, fb.height / frame.height)
        }
        updateSize()
    }

    private var observers: [NSObjectProtocol] = []

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        observers.forEach(NotificationCenter.default.removeObserver)
        observers = []
        guard let surface else { return }
        // A surface that is not on screen stops drawing: no CPU for hidden or covered panes.
        guard let window else {
            ghostty_surface_set_occlusion(surface, false)
            return
        }
        let nc = NotificationCenter.default
        observers = [
            nc.addObserver(forName: NSWindow.didChangeOcclusionStateNotification, object: window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.updateVisibility() }
            },
            nc.addObserver(forName: NSWindow.didChangeScreenNotification, object: window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.updateScreen() }
            },
        ]
        updateScreen()
        updateVisibility()
        viewDidChangeBackingProperties()
    }

    private func updateVisibility() {
        guard let surface, let window else { return }
        ghostty_surface_set_occlusion(surface, window.occlusionState.contains(.visible))
    }

    private func updateScreen() {
        guard let surface, let n = window?.screen?.deviceDescription[NSDeviceDescriptionKey("NSScreenNumber")] as? NSNumber
        else { return }
        ghostty_surface_set_display_id(surface, n.uint32Value)
    }

    override func updateTrackingAreas() {
        if let tracking { removeTrackingArea(tracking) }
        tracking = NSTrackingArea(rect: frame, options: [.mouseEnteredAndExited, .mouseMoved, .inVisibleRect, .activeAlways],
                                  owner: self, userInfo: nil)
        addTrackingArea(tracking!)
        super.updateTrackingAreas()
    }

    override func resetCursorRects() {
        let cursor: NSCursor = switch cursorShape {
        case GHOSTTY_MOUSE_SHAPE_POINTER: .pointingHand
        case GHOSTTY_MOUSE_SHAPE_DEFAULT: .arrow
        case GHOSTTY_MOUSE_SHAPE_EW_RESIZE, GHOSTTY_MOUSE_SHAPE_COL_RESIZE: .resizeLeftRight
        case GHOSTTY_MOUSE_SHAPE_NS_RESIZE, GHOSTTY_MOUSE_SHAPE_ROW_RESIZE: .resizeUpDown
        case GHOSTTY_MOUSE_SHAPE_CROSSHAIR: .crosshair
        default: .iBeam
        }
        addCursorRect(bounds, cursor: cursor)
    }

    // MARK: focus

    override func becomeFirstResponder() -> Bool {
        let ok = super.becomeFirstResponder()
        if ok, let surface { ghostty_surface_set_focus(surface, true) }
        return ok
    }

    override func resignFirstResponder() -> Bool {
        let ok = super.resignFirstResponder()
        if ok, let surface { ghostty_surface_set_focus(surface, false) }
        return ok
    }

    // MARK: mouse

    private static func mods(_ f: NSEvent.ModifierFlags) -> ghostty_input_mods_e {
        var m = GHOSTTY_MODS_NONE.rawValue
        if f.contains(.shift) { m |= GHOSTTY_MODS_SHIFT.rawValue }
        if f.contains(.control) { m |= GHOSTTY_MODS_CTRL.rawValue }
        if f.contains(.option) { m |= GHOSTTY_MODS_ALT.rawValue }
        if f.contains(.command) { m |= GHOSTTY_MODS_SUPER.rawValue }
        if f.contains(.capsLock) { m |= GHOSTTY_MODS_CAPS.rawValue }
        let raw = f.rawValue
        if raw & UInt(NX_DEVICERSHIFTKEYMASK) != 0 { m |= GHOSTTY_MODS_SHIFT_RIGHT.rawValue }
        if raw & UInt(NX_DEVICERCTLKEYMASK) != 0 { m |= GHOSTTY_MODS_CTRL_RIGHT.rawValue }
        if raw & UInt(NX_DEVICERALTKEYMASK) != 0 { m |= GHOSTTY_MODS_ALT_RIGHT.rawValue }
        if raw & UInt(NX_DEVICERCMDKEYMASK) != 0 { m |= GHOSTTY_MODS_SUPER_RIGHT.rawValue }
        return ghostty_input_mods_e(m)
    }

    private func button(_ e: NSEvent, _ state: ghostty_input_mouse_state_e, _ b: ghostty_input_mouse_button_e) {
        guard let surface else { return }
        _ = ghostty_surface_mouse_button(surface, state, b, Self.mods(e.modifierFlags))
    }

    override func mouseDown(with e: NSEvent) { button(e, GHOSTTY_MOUSE_PRESS, GHOSTTY_MOUSE_LEFT) }
    override func mouseUp(with e: NSEvent) { button(e, GHOSTTY_MOUSE_RELEASE, GHOSTTY_MOUSE_LEFT) }
    override func rightMouseDown(with e: NSEvent) { button(e, GHOSTTY_MOUSE_PRESS, GHOSTTY_MOUSE_RIGHT) }
    override func rightMouseUp(with e: NSEvent) { button(e, GHOSTTY_MOUSE_RELEASE, GHOSTTY_MOUSE_RIGHT) }
    override func otherMouseDown(with e: NSEvent) { if e.buttonNumber == 2 { button(e, GHOSTTY_MOUSE_PRESS, GHOSTTY_MOUSE_MIDDLE) } }
    override func otherMouseUp(with e: NSEvent) { if e.buttonNumber == 2 { button(e, GHOSTTY_MOUSE_RELEASE, GHOSTTY_MOUSE_MIDDLE) } }

    override func mouseMoved(with e: NSEvent) {
        guard let surface else { return }
        let p = convert(e.locationInWindow, from: nil)
        ghostty_surface_mouse_pos(surface, p.x, frame.height - p.y, Self.mods(e.modifierFlags))
    }

    override func mouseDragged(with e: NSEvent) { mouseMoved(with: e) }
    override func rightMouseDragged(with e: NSEvent) { mouseMoved(with: e) }
    override func otherMouseDragged(with e: NSEvent) { mouseMoved(with: e) }
    override func mouseEntered(with e: NSEvent) { mouseMoved(with: e) }

    override func mouseExited(with e: NSEvent) {
        // Ghostty's convention for "the mouse left": a negative position.
        if let surface { ghostty_surface_mouse_pos(surface, -1, -1, Self.mods(e.modifierFlags)) }
    }

    override func scrollWheel(with e: NSEvent) {
        guard let surface else { return }
        var (x, y) = (e.scrollingDeltaX, e.scrollingDeltaY)
        if e.hasPreciseScrollingDeltas { (x, y) = (x * 2, y * 2) } // Ghostty's own feel
        let momentum: Int32 = switch e.momentumPhase {
        case .began: 1
        case .stationary: 2
        case .changed: 3
        case .ended: 4
        case .cancelled: 5
        case .mayBegin: 6
        default: 0
        }
        // Packed ScrollMods: bit 0 precision, bits 1-3 momentum phase.
        ghostty_surface_mouse_scroll(surface, x, y, (e.hasPreciseScrollingDeltas ? 1 : 0) | momentum << 1)
    }

    // MARK: keys

    override func keyDown(with event: NSEvent) {
        guard let surface else {
            interpretKeyEvents([event])
            return
        }
        // Option as Alt (and similar settings) change which modifiers produce the character.
        let tm = ghostty_surface_key_translation_mods(surface, Self.mods(event.modifierFlags)).rawValue
        var mods = event.modifierFlags
        for (flag, bit) in [(NSEvent.ModifierFlags.shift, GHOSTTY_MODS_SHIFT), (.control, GHOSTTY_MODS_CTRL),
                            (.option, GHOSTTY_MODS_ALT), (.command, GHOSTTY_MODS_SUPER)] {
            if tm & bit.rawValue != 0 { mods.insert(flag) } else { mods.remove(flag) }
        }
        // The same event object when nothing changed: input methods (Korean, for one) depend on it.
        let translated = mods == event.modifierFlags ? event : NSEvent.keyEvent(
            with: event.type, location: event.locationInWindow, modifierFlags: mods, timestamp: event.timestamp,
            windowNumber: event.windowNumber, context: nil, characters: event.characters(byApplyingModifiers: mods) ?? "",
            charactersIgnoringModifiers: event.charactersIgnoringModifiers ?? "", isARepeat: event.isARepeat,
            keyCode: event.keyCode) ?? event
        let action = event.isARepeat ? GHOSTTY_ACTION_REPEAT : GHOSTTY_ACTION_PRESS

        keyTextAccumulator = []
        defer { keyTextAccumulator = nil }
        let markedBefore = markedText.length > 0
        interpretKeyEvents([translated])
        syncPreedit(clearIfNeeded: markedBefore)
        let composing = markedText.length > 0 || markedBefore

        if let list = keyTextAccumulator, !list.isEmpty {
            for text in list where !Self.composingControl(text, composing) {
                if markedBefore { sendText(action, text) } else { key(action, event, translated, text: text) }
            }
        } else if !Self.composingControl(event.characters, composing) {
            key(action, event, translated, text: Self.characters(translated), composing: composing)
        }
    }

    override func keyUp(with event: NSEvent) { key(GHOSTTY_ACTION_RELEASE, event) }

    override func flagsChanged(with event: NSEvent) {
        let bit: UInt32
        switch event.keyCode {
        case 0x39: bit = GHOSTTY_MODS_CAPS.rawValue
        case 0x38, 0x3C: bit = GHOSTTY_MODS_SHIFT.rawValue
        case 0x3B, 0x3E: bit = GHOSTTY_MODS_CTRL.rawValue
        case 0x3A, 0x3D: bit = GHOSTTY_MODS_ALT.rawValue
        case 0x37, 0x36: bit = GHOSTTY_MODS_SUPER.rawValue
        default: return
        }
        if hasMarkedText() { return }
        key(Self.mods(event.modifierFlags).rawValue & bit != 0 ? GHOSTTY_ACTION_PRESS : GHOSTTY_ACTION_RELEASE, event)
    }

    /// Command keys belong to the app's menus. Two Control keys are taken here because AppKit would use them:
    /// C-Return (context menu) and C-/ (a beep), as Ghostty does.
    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        guard event.type == .keyDown, window?.firstResponder === self,
              event.modifierFlags.contains(.control), !event.modifierFlags.contains(.command) else { return false }
        let ch: String
        switch event.charactersIgnoringModifiers {
        case "\r": ch = "\r"
        case "/" where event.modifierFlags.isDisjoint(with: [.shift, .option]): ch = "_"
        default: return false
        }
        if let e = NSEvent.keyEvent(with: .keyDown, location: event.locationInWindow, modifierFlags: event.modifierFlags,
                                    timestamp: event.timestamp, windowNumber: event.windowNumber, context: nil,
                                    characters: ch, charactersIgnoringModifiers: ch, isARepeat: event.isARepeat,
                                    keyCode: event.keyCode) {
            keyDown(with: e)
        }
        return true
    }

    private func key(_ action: ghostty_input_action_e, _ event: NSEvent, _ translated: NSEvent? = nil, text: String? = nil,
                     composing: Bool = false) {
        guard let surface else { return }
        var k = ghostty_input_key_s()
        k.action = action
        k.keycode = UInt32(event.keyCode)
        k.mods = Self.mods(event.modifierFlags)
        // Control and Command never produce the character; the rest did.
        k.consumed_mods = Self.mods((translated ?? event).modifierFlags.subtracting([.control, .command]))
        k.composing = composing
        if event.type == .keyDown || event.type == .keyUp,
           let cp = event.characters(byApplyingModifiers: [])?.unicodeScalars.first {
            k.unshifted_codepoint = cp.value
        }
        // Ghostty encodes control characters from the key and mods itself.
        if let text, !text.isEmpty, !(text.unicodeScalars.count == 1 && text.unicodeScalars.first!.value < 0x20) {
            text.withCString { k.text = $0; _ = ghostty_surface_key(surface, k) }
        } else {
            _ = ghostty_surface_key(surface, k)
        }
    }

    /// Text committed by an input method, sent as typed (not pasted).
    private func sendText(_ action: ghostty_input_action_e, _ text: String) {
        guard let surface else { return }
        var k = ghostty_input_key_s()
        k.action = action
        text.withCString { k.text = $0; _ = ghostty_surface_key(surface, k) }
    }

    /// The text of a key for Ghostty: a control character without Control (Ghostty encodes it), nothing for the
    /// function keys' private-use characters.
    private static func characters(_ e: NSEvent) -> String? {
        guard let c = e.characters else { return nil }
        if c.unicodeScalars.count == 1, let s = c.unicodeScalars.first {
            if s.value < 0x20 { return e.characters(byApplyingModifiers: e.modifierFlags.subtracting(.control)) }
            if (0xF700...0xF8FF).contains(s.value) { return nil }
        }
        return c
    }

    /// A lone control character while an input method composes belongs to the input method.
    private static func composingControl(_ text: String?, _ composing: Bool) -> Bool {
        guard composing, let s = text?.unicodeScalars, s.count == 1 else { return false }
        return s.first!.value < 0x20
    }

    private func syncPreedit(clearIfNeeded: Bool = true) {
        guard let surface else { return }
        if markedText.length > 0 {
            let s = markedText.string
            s.withCString { ghostty_surface_preedit(surface, $0, UInt(s.utf8.count)) }
        } else if clearIfNeeded {
            ghostty_surface_preedit(surface, nil, 0)
        }
    }

    // MARK: copy and paste (Edit menu, through the responder chain)

    private func perform(_ action: String) {
        if let surface { _ = ghostty_surface_binding_action(surface, action, UInt(action.utf8.count)) }
    }

    @objc func copy(_ sender: Any?) { perform("copy_to_clipboard") }
    @objc func paste(_ sender: Any?) { perform("paste_from_clipboard") }
    @objc override func selectAll(_ sender: Any?) { perform("select_all") }

    // MARK: NSTextInputClient (input methods, dead keys, dictation)

    func hasMarkedText() -> Bool { markedText.length > 0 }
    func markedRange() -> NSRange { markedText.length > 0 ? NSRange(location: 0, length: markedText.length) : NSRange() }
    func selectedRange() -> NSRange { NSRange() }
    func validAttributesForMarkedText() -> [NSAttributedString.Key] { [] }
    func attributedSubstring(forProposedRange range: NSRange, actualRange: NSRangePointer?) -> NSAttributedString? { nil }
    func characterIndex(for point: NSPoint) -> Int { 0 }

    func setMarkedText(_ string: Any, selectedRange: NSRange, replacementRange: NSRange) {
        switch string {
        case let v as NSAttributedString: markedText = NSMutableAttributedString(attributedString: v)
        case let v as String: markedText = NSMutableAttributedString(string: v)
        default: return
        }
        if keyTextAccumulator == nil { syncPreedit() } // for example a keyboard layout change while composing
    }

    func unmarkText() {
        guard markedText.length > 0 else { return }
        markedText.mutableString.setString("")
        syncPreedit()
    }

    func firstRect(forCharacterRange range: NSRange, actualRange: NSRangePointer?) -> NSRect {
        guard let surface else { return .zero }
        var x = 0.0, y = 0.0, w = 0.0, h = 0.0
        ghostty_surface_ime_point(surface, &x, &y, &w, &h)
        let r = convert(NSRect(x: x, y: frame.height - y, width: range.length == 0 ? 0 : w, height: h), to: nil)
        return window?.convertToScreen(r) ?? r
    }

    func insertText(_ string: Any, replacementRange: NSRange) {
        guard NSApp.currentEvent != nil else { return }
        let chars = (string as? NSAttributedString)?.string ?? (string as? String) ?? ""
        unmarkText()
        if keyTextAccumulator != nil {
            keyTextAccumulator?.append(chars)
        } else if !chars.isEmpty {
            sendText(GHOSTTY_ACTION_PRESS, chars)
        }
    }

    /// Unbound editing commands (C-a's moveToBeginningOfParagraph:, and so on) do nothing: keyDown sends the key.
    /// Without this, AppKit beeps.
    override func doCommand(by selector: Selector) {}
}
