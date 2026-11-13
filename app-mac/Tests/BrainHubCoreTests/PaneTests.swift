import Foundation
import Testing
import BrainHubCore

@Suite struct TmuxConf {
    @Test func keyNames() {
        #expect(Key(tmux: "C-a") == Key(name: "a", ctrl: true))
        #expect(Key(tmux: "C-A") == Key(name: "a", ctrl: true))
        #expect(Key(tmux: "^A") == Key(name: "a", ctrl: true))
        #expect(Key(tmux: "M-Left") == Key(name: "Left", meta: true))
        #expect(Key(tmux: "C-M-x") == Key(name: "x", ctrl: true, meta: true))
        #expect(Key(tmux: "PPage") == Key(name: "PageUp"))
        #expect(Key(tmux: "%") == Key(name: "%"))
        #expect(Key(tmux: "-") == Key(name: "-"))
        #expect(Key(tmux: "Bogus") == nil)
        #expect(Key(tmux: "C-a")!.bytes == [1])
        #expect(Key(tmux: "C-b")!.bytes == [2])
        #expect(Key(tmux: "C-Space")!.bytes == [0])
        #expect(Key(tmux: "M-x")!.bytes == [0x1b, 0x78])
        #expect(Key(tmux: "`")!.bytes == [0x60])
    }

    @Test func defaultsAreTmuxs() {
        let t = KeyTable.defaults
        #expect(t.prefix == Key(name: "b", ctrl: true))
        #expect(t.binding(Key(name: "%"), root: false)?.action == .split(.horizontal))
        #expect(t.binding(Key(name: "\""), root: false)?.action == .split(.vertical))
        #expect(t.binding(Key(name: "o"), root: false)?.action == .nextPane(1))
        #expect(t.binding(Key(name: "z"), root: false)?.action == .zoom)
        #expect(t.binding(Key(name: "x"), root: false)?.action == .killPane)
        #expect(t.binding(Key(name: "c"), root: false)?.action == .newWindow)
        #expect(t.binding(Key(name: "p"), root: false)?.action == .nextWindow(-1))
        #expect(t.binding(Key(name: "3"), root: false)?.action == .selectWindow(3))
        #expect(t.binding(Key(name: "Left"), root: false)?.repeats == true)
        #expect(t.binding(Key(name: "Up", meta: true), root: false)?.action == .resize(.up, 5))
        #expect(t.binding(Key(name: "b", ctrl: true), root: false)?.action == .sendPrefix)
    }

    @Test func readsAPersonalConfig() {
        let conf = #"""
        # my tmux
        unbind C-b
        set -g prefix C-a   # screen habits
        bind C-a send-prefix
        set -g base-index 1
        bind | split-window -h -c "#{pane_current_path}"
        bind - split-window -v -c '#{pane_current_path}'
        unbind '"'
        unbind %
        bind -r h select-pane -L
        bind -r H resize-pane -L 3
        bind -n M-Right select-pane -R
        bind-key -T copy-mode-vi v send-keys -X begin-selection
        bind r source-file ~/.tmux.conf \; display "reloaded"
        bind x kill-pane
        set -g mouse on
        this is not a command
        bind
        """#
        let t = KeyTable.load(conf, source: "~/.tmux.conf")
        #expect(t.prefix == Key(name: "a", ctrl: true))
        #expect(t.prefixSource == "~/.tmux.conf:3")
        #expect(t.baseIndex == 1)
        #expect(t.binding(Key(name: "|"), root: false)?.action == .split(.horizontal))
        #expect(t.binding(Key(name: "|"), root: false)?.source == "~/.tmux.conf:6")
        #expect(t.binding(Key(name: "-"), root: false)?.action == .split(.vertical))
        #expect(t.binding(Key(name: "%"), root: false) == nil, "unbound")
        #expect(t.binding(Key(name: "\""), root: false) == nil, "unbound")
        #expect(t.binding(Key(name: "b", ctrl: true), root: false) == nil, "unbind C-b removed send-prefix on C-b")
        #expect(t.binding(Key(name: "a", ctrl: true), root: false)?.action == .sendPrefix)
        #expect(t.binding(Key(name: "h"), root: false)?.repeats == true)
        #expect(t.binding(Key(name: "H"), root: false)?.action == .resize(.left, 3))
        #expect(t.binding(Key(name: "Right", meta: true), root: true)?.action == .selectPane(.right))
        #expect(t.binding(Key(name: "v"), root: false) == nil, "other tables are ignored")
        #expect(t.binding(Key(name: "r"), root: false) == nil, "commands this app does not do are ignored")
        #expect(t.binding(Key(name: "o"), root: false)?.source == "tmux default")
    }

    @Test func rebindingToAnUnknownCommandDropsTheDefault() {
        let t = KeyTable.load("bind c display-message hi\nunbind -a\n", source: "f")
        #expect(t.binding(Key(name: "c"), root: false) == nil)
        #expect(t.bindings.filter { !$0.root }.isEmpty, "unbind -a clears the prefix table")
    }

    @Test func configFileLocations() throws {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("tmuxhome-\(ProcessInfo.processInfo.processIdentifier)")
        try? FileManager.default.removeItem(at: home)
        #expect(tmuxConfig(home: home) == nil)
        try FileManager.default.createDirectory(at: home.appendingPathComponent(".config/tmux"), withIntermediateDirectories: true)
        try Data("set -g prefix C-a\n".utf8).write(to: home.appendingPathComponent(".config/tmux/tmux.conf"))
        #expect(tmuxConfig(home: home)?.path == "~/.config/tmux/tmux.conf")
        try Data("set -g prefix C-s\n".utf8).write(to: home.appendingPathComponent(".tmux.conf"))
        #expect(tmuxConfig(home: home)?.path == "~/.tmux.conf")
    }
}

@Suite struct PaneLayout {
    let a = TerminalRef(hub: "http://127.0.0.1:8787", id: 1)
    let b = TerminalRef(hub: "http://127.0.0.1:8787", id: 2)

    @Test func splitFocusAndClose() {
        var p = PaneTree(terminal: a)
        let split = p.split(.horizontal)
        #expect(split)
        #expect(p.panes.count == 2 && p.focusedTerminal == nil, "the new pane is empty and focused")
        p.show(b)
        p.focus(.left)
        #expect(p.focusedTerminal == a)
        p.show(b)
        #expect(p.focusedTerminal == b && p.terminals.values.filter { $0 == b }.count == 1, "showing a terminal already in a pane focuses it")
        p.close()
        #expect(p.panes.count == 1 && p.focusedTerminal == a)
        p.close()
        #expect(p.panes.count == 1 && p.focusedTerminal == nil, "the last pane only empties")
    }

    @Test func atMostNinePanes() {
        var p = PaneTree()
        for i in 0..<8 { let ok = p.split(i % 2 == 0 ? .horizontal : .vertical); #expect(ok) }
        #expect(p.panes.count == 9)
        let tenth = p.split(.vertical)
        #expect(!tenth)
    }

    @Test func directionalFocus() {
        // [0 | 1 / 2]: left pane, right column split top and bottom.
        var p = PaneTree()
        p.split(.horizontal)
        p.split(.vertical)
        #expect(p.panes == [0, 1, 3])
        #expect(p.focused == 3)
        p.focus(.up); #expect(p.focused == 1)
        p.focus(.up); #expect(p.focused == 1, "nothing above")
        p.focus(.left); #expect(p.focused == 0)
        p.focus(.right); #expect(p.focused == 1, "ties go to the first pane")
        p.focusNext(1); #expect(p.focused == 3)
        p.focusNext(1); #expect(p.focused == 0)
        p.focusNext(-1); #expect(p.focused == 3)
    }

    @Test func resizeAndZoom() {
        var p = PaneTree()
        p.split(.horizontal)
        p.resize(.right, cells: 2)
        #expect(abs(p.frames()[0]!.width - 0.6) < 0.0001)
        p.resize(.up)
        #expect(abs(p.frames()[0]!.width - 0.6) < 0.0001, "no vertical split: nothing moves")
        p.resize(.left, cells: 20)
        #expect(abs(p.frames()[0]!.width - 0.1) < 0.0001, "clamped")
        p.toggleZoom()
        #expect(p.zoomed == p.focused)
        p.focus(.left)
        #expect(p.zoomed == nil, "moving to another pane unzooms")
    }
}
