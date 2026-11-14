import Foundation

// tmux-style key bindings for the panes of a window. The table starts from tmux's defaults and then applies the person's
// own ~/.tmux.conf (or ~/.config/tmux/tmux.conf): `set -g prefix`, `set -g base-index`, `unbind` and `bind` lines.
// Commands this app has no use for, other key tables and anything it cannot read are ignored, as tmux would skip them.

/// A key as tmux writes it: `C-a`, `M-Left`, `%`, `S-Up`, `^A`.
public struct Key: Hashable, Sendable, CustomStringConvertible {
    public var name: String
    public var ctrl = false
    public var meta = false
    public var shift = false

    public init(name: String, ctrl: Bool = false, meta: Bool = false, shift: Bool = false) {
        // tmux treats C-A and C-a as the same key.
        self.name = ctrl && name.count == 1 ? name.lowercased() : name
        (self.ctrl, self.meta, self.shift) = (ctrl, meta, shift)
    }

    /// Named keys tmux knows, by their lower-case name.
    static let named: [String: String] = Dictionary(uniqueKeysWithValues: [
        "Up", "Down", "Left", "Right", "Enter", "Escape", "Tab", "BTab", "Space", "BSpace", "Home", "End",
        "PageUp", "PageDown", "PPage", "NPage", "IC", "DC", "F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10",
        "F11", "F12",
    ].map { ($0.lowercased(), $0) })

    public init?(tmux text: String) {
        var rest = Substring(text)
        var ctrl = false, meta = false, shift = false
        if rest.count == 2, rest.first == "^" { self.init(name: String(rest.last!), ctrl: true); return }
        while rest.count > 2, rest.dropFirst().first == "-" {
            switch rest.first {
            case "C": ctrl = true
            case "M": meta = true
            case "S": shift = true
            default: return nil
            }
            rest = rest.dropFirst(2)
        }
        guard !rest.isEmpty else { return nil }
        var name = String(rest)
        if name.count > 1 {
            guard var n = Self.named[name.lowercased()] else { return nil }
            if n == "PPage" { n = "PageUp" }
            if n == "NPage" { n = "PageDown" }
            name = n
        }
        self.init(name: name, ctrl: ctrl, meta: meta, shift: shift)
    }

    public var description: String {
        (ctrl ? "C-" : "") + (meta ? "M-" : "") + (shift ? "S-" : "") + name
    }

    /// The bytes a terminal sends for this key, for `send-prefix`: C-a is 0x01, C-Space is 0x00, M- adds ESC.
    public var bytes: [UInt8] {
        var out: [UInt8] = []
        if ctrl, name == "Space" { out = [0] }
        else if ctrl, name.count == 1, let a = name.unicodeScalars.first, (0x40..<0x80).contains(a.value) {
            out = [UInt8(a.value & 0x1f)]
        } else if name == "Space" { out = [0x20] }
        else if name == "Enter" { out = [0x0d] }
        else if name == "Tab" { out = [0x09] }
        else if name == "Escape" { out = [0x1b] }
        else if name.count == 1 { out = Array(name.utf8) }
        return meta ? [0x1b] + out : out
    }
}

public enum Axis: Sendable, Equatable { case horizontal /* side by side */, vertical /* one above the other */ }
public enum Direction: Sendable, Equatable { case left, right, up, down }

/// What a binding does here. A hub terminal plays the part of a tmux window.
public enum PaneAction: Equatable, Sendable {
    case split(Axis)
    case selectPane(Direction)
    case nextPane(Int)
    case resize(Direction, Int)
    case zoom
    case killPane
    case newWindow
    case nextWindow(Int)
    case selectWindow(Int)
    case copyMode
    case sendPrefix
}

public struct KeyBinding: Equatable, Sendable {
    public var key: Key
    public var action: PaneAction
    /// `bind -n`: no prefix needed.
    public var root: Bool
    /// `bind -r`: may repeat without the prefix for a moment.
    public var repeats: Bool
    /// The tmux command as written.
    public var command: String
    /// "tmux default", or the file and line it came from.
    public var source: String
}

public struct KeyTable: Sendable {
    public private(set) var prefix = Key(name: "b", ctrl: true)
    public private(set) var prefixSource = "tmux default"
    public private(set) var baseIndex = 0
    public private(set) var bindings: [KeyBinding] = []

    /// tmux's own defaults for the commands this app does.
    public static let defaults: KeyTable = {
        var t = KeyTable()
        var lines = [
            "bind % split-window -h", #"bind '"' split-window"#, "bind -r Up select-pane -U", "bind -r Down select-pane -D",
            "bind -r Left select-pane -L", "bind -r Right select-pane -R", "bind o select-pane -t :.+", "bind x kill-pane",
            "bind z resize-pane -Z", "bind c new-window", "bind n next-window", "bind p previous-window", "bind [ copy-mode",
            "bind C-b send-prefix", "bind -r C-Up resize-pane -U", "bind -r C-Down resize-pane -D",
            "bind -r C-Left resize-pane -L", "bind -r C-Right resize-pane -R", "bind M-Up resize-pane -U 5",
            "bind M-Down resize-pane -D 5", "bind M-Left resize-pane -L 5", "bind M-Right resize-pane -R 5",
        ]
        lines += (0...9).map { "bind \($0) select-window -t :=\($0)" }
        t.apply(lines.joined(separator: "\n"), source: "tmux default")
        return t
    }()

    /// The defaults with a config file applied.
    public static func load(_ text: String?, source: String) -> KeyTable {
        var t = defaults
        if let text { t.apply(text, source: source) }
        return t
    }

    public mutating func overridePrefix(_ key: Key) {
        prefix = key
        prefixSource = "Settings"
    }

    public func binding(_ key: Key, root: Bool) -> KeyBinding? {
        bindings.last { $0.key == key && $0.root == root }
    }

    mutating func apply(_ text: String, source: String) {
        for (i, line) in text.components(separatedBy: .newlines).enumerated() {
            let words = Self.split(line)
            guard let cmd = words.first else { continue }
            let at = source == "tmux default" ? source : "\(source):\(i + 1)"
            switch cmd {
            case "set", "set-option", "setw", "set-window-option":
                let args = words.dropFirst().filter { !$0.hasPrefix("-") }
                guard args.count >= 2 else { continue }
                if args.first == "prefix", let k = Key(tmux: args[args.startIndex + 1]) { (prefix, prefixSource) = (k, at) }
                if args.first == "base-index", let n = Int(args[args.startIndex + 1]) { baseIndex = n }
            case "unbind", "unbind-key":
                var root = false, all = false, table = "prefix"
                var it = words.dropFirst().makeIterator()
                var key: Key?
                while let w = it.next() {
                    switch w {
                    case "-n": root = true
                    case "-a": all = true
                    case "-T": table = it.next() ?? ""
                    case let w where w.hasPrefix("-") && w.count > 1 && Key(tmux: w) == nil: continue
                    default: key = Key(tmux: w)
                    }
                }
                if table == "root" { root = true } else if table != "prefix" { continue }
                if all { bindings.removeAll { $0.root == root } }
                if let key { bindings.removeAll { $0.key == key && $0.root == root } }
            case "bind", "bind-key":
                var root = false, repeats = false, table = "prefix"
                var rest = Array(words.dropFirst())
                while let w = rest.first, w.hasPrefix("-"), w.count > 1, Key(tmux: w) == nil || ["-n", "-r"].contains(w) {
                    rest.removeFirst()
                    switch w {
                    case "-n": root = true
                    case "-r": repeats = true
                    case "-T": table = rest.isEmpty ? "" : rest.removeFirst()
                    case "-N": if !rest.isEmpty { rest.removeFirst() } // a note
                    default: break
                    }
                }
                if table == "root" { root = true } else if table != "prefix" { continue }
                guard rest.count >= 2, let key = Key(tmux: rest[0]) else { continue }
                let command = Array(rest.dropFirst().prefix { $0 != ";" && $0 != "\\;" })
                // A key bound to something this app does not do keeps tmux's meaning: drop any older binding.
                bindings.removeAll { $0.key == key && $0.root == root }
                if let action = Self.action(command) {
                    bindings.append(KeyBinding(key: key, action: action, root: root, repeats: repeats,
                                            command: rest.dropFirst().joined(separator: " "), source: at))
                }
            default: continue
            }
        }
    }

    static func action(_ words: [String]) -> PaneAction? {
        guard let cmd = words.first else { return nil }
        let flags = Set(words.dropFirst())
        func target() -> String? {
            guard let i = words.firstIndex(of: "-t"), i + 1 < words.count else { return nil }
            return words[i + 1]
        }
        func direction() -> Direction? {
            if flags.contains("-L") { return .left }
            if flags.contains("-R") { return .right }
            if flags.contains("-U") { return .up }
            if flags.contains("-D") { return .down }
            return nil
        }
        switch cmd {
        case "split-window", "splitw": return .split(flags.contains("-h") ? .horizontal : .vertical)
        case "select-pane", "selectp":
            if let d = direction() { return .selectPane(d) }
            switch target() {
            case ":.+", ".+", "+": return .nextPane(1)
            case ":.-", ".-", "-": return .nextPane(-1)
            default: return nil
            }
        case "resize-pane", "resizep":
            if flags.contains("-Z") { return .zoom }
            guard let d = direction() else { return nil }
            return .resize(d, words.dropFirst().compactMap { Int($0) }.last ?? 1)
        case "kill-pane", "killp": return .killPane
        case "new-window", "neww": return .newWindow
        case "next-window", "next": return .nextWindow(1)
        case "previous-window", "prev": return .nextWindow(-1)
        case "select-window", "selectw":
            guard let t = target() else { return nil }
            let n = t.drop { $0 == ":" || $0 == "=" }
            return Int(n).map { .selectWindow($0) }
        case "copy-mode": return .copyMode
        case "send-prefix": return .sendPrefix
        default: return nil
        }
    }

    /// Split a config line into words the way tmux does for these commands: quotes, backslash escapes, # comments.
    static func split(_ line: String) -> [String] {
        var words: [String] = [], cur = "", quote: Character?, escaped = false, has = false
        for ch in line {
            if escaped { cur.append(ch); escaped = false; has = true; continue }
            if let q = quote {
                if ch == q { quote = nil } else if ch == "\\" && q == "\"" { escaped = true } else { cur.append(ch) }
                continue
            }
            switch ch {
            case "\\": escaped = true
            case "'", "\"": quote = ch; has = true
            case "#" where !has && cur.isEmpty: return words
            case " ", "\t":
                if has || !cur.isEmpty { words.append(cur); cur = ""; has = false }
            default: cur.append(ch); has = true
            }
        }
        if has || !cur.isEmpty { words.append(cur) }
        return words
    }
}

/// The person's tmux config, where tmux looks for it.
public func tmuxConfig(home: URL = FileManager.default.homeDirectoryForCurrentUser) -> (text: String, path: String)? {
    for rel in [".tmux.conf", ".config/tmux/tmux.conf"] {
        let url = home.appendingPathComponent(rel)
        if let text = try? String(contentsOf: url, encoding: .utf8) { return (text, "~/" + rel) }
    }
    return nil
}
