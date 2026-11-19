import AppKit
import BrainHubCore
import SwiftUI
import UniformTypeIdentifiers

// Moving or cloning a terminal to another machine: drag its sidebar row or its pane's grip onto a machine (the
// machine's section, or its tile under the list), or use the Move/Clone menus. Holding ⌥ while dropping clones.
// The terminal's own hub runs the job; the source ends only after the destination is verified live.

extension AppModel {
    /// Start a move (or clone) from any control. `done` gets the new terminal, `failed` the reason.
    func startMigration(_ ref: TerminalRef, to machine: String, clone: Bool,
                        done: @escaping @MainActor (TerminalRef) -> Void, failed: @escaping @MainActor (String) -> Void) {
        guard let t = terminal(ref) else { return failed("That terminal is no longer listed by its hub.") }
        guard t.host != machine else { return failed("\(t.title.isEmpty ? "It" : t.title) already runs on \(machineLabel(machine)).") }
        guard migrations[ref].map(\.done) ?? true else { return }
        Task {
            do {
                struct Pins: Decodable { var mappings: [MoveFolderPin] }
                let c = try HubClient(base: ref.hub)
                let pins = try? await c.get("/api/move-folders", as: Pins.self)
                let pinned = pins?.mappings.first { $0.fromHost == t.host && $0.to == machine && Self.portableFolder($0.sourcePath) == Self.portableFolder(t.cwd) }?.destinationPath
                let alert = NSAlert()
                alert.messageText = "\(clone ? "Clone" : "Move") to \(machineLabel(machine))"
                alert.informativeText = "Choose a folder on the destination, or leave it blank for \(pinned.map { "the pinned folder (\($0))" } ?? "automatic folder selection"). Conversation context moves; code files stay on their machine."
                alert.addButton(withTitle: clone ? "Clone" : "Move")
                alert.addButton(withTitle: "Cancel")
                let path = NSTextField(string: "")
                path.placeholderString = "Optional destination: ~/projects/repo"
                path.widthAnchor.constraint(equalToConstant: 390).isActive = true
                let pin = NSButton(checkboxWithTitle: "Pin this path for future moves to this machine", target: nil, action: nil)
                let home = NSButton(checkboxWithTitle: "Use home folder instead (ignore pinned folder)", target: nil, action: nil)
                let stack = NSStackView(views: [path, pin, home])
                stack.orientation = .vertical
                stack.alignment = .leading
                stack.spacing = 10
                alert.accessoryView = stack
                guard alert.runModal() == .alertFirstButtonReturn else { return }
                let folder = path.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
                let useHome = home.state == .on
                if !useHome && pin.state == .on && folder.isEmpty { throw HubError("Enter a destination folder to pin.") }
                if pins == nil && (useHome || !folder.isEmpty) { throw HubError("Update this hub before choosing a destination folder.") }
                if let dest = try await migrate(ref, to: machine, clone: clone, destinationPath: useHome ? "" : folder, pinPath: !useHome && pin.state == .on, withoutFolder: useHome) { done(dest) }
            } catch { failed(error.localizedDescription) }
        }
    }

    static func portableFolder(_ raw: String) -> String {
        raw.replacingOccurrences(of: "^/(Users|home)/[^/]+(?=/|$)", with: "~", options: .regularExpression)
            .trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    }

    /// A dropped sidebar row or pane grip: "<hub>#<id>", as text.
    static func droppedRef(_ providers: [NSItemProvider], _ use: @escaping @MainActor (TerminalRef) -> Void) -> Bool {
        guard let p = providers.first(where: { $0.canLoadObject(ofClass: NSString.self) }) else { return false }
        _ = p.loadObject(ofClass: NSString.self) { s, _ in
            guard let text = s as? String, let ref = TerminalRef(dragText: text) else { return }
            Task { @MainActor in use(ref) }
        }
        return true
    }

    /// ⌥ held while dropping: clone instead of move.
    static var cloneModifier: Bool { NSEvent.modifierFlags.contains(.option) }
}

/// "Move to Machine" and "Clone to Machine" submenus for one terminal.
struct MoveMenus: View {
    let ref: TerminalRef
    let go: (_ machine: String, _ clone: Bool) -> Void
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let others = model.machines.map(\.name).filter { $0 != model.terminal(ref)?.host }
        if !others.isEmpty {
            Menu("Move to Machine") {
                ForEach(others, id: \.self) { n in Button(model.machineLabel(n)) { go(n, false) } }
            }
            Menu("Clone to Machine") {
                ForEach(others, id: \.self) { n in Button(model.machineLabel(n)) { go(n, true) } }
            }
        }
    }
}

/// Large drop tiles, one per machine, under the sidebar list: plain views, so a drop always lands (a List row or
/// section header can lose a drop to the list's own reordering).
struct MachineDropPanel: View {
    let go: (_ ref: TerminalRef, _ machine: String, _ clone: Bool) -> Void
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let names = model.machines.map(\.name)
        if names.count > 1 {
            VStack(alignment: .leading, spacing: 6) {
                Text("Drag a terminal onto a machine to move it. Hold ⌥ to clone.").font(.caption).foregroundStyle(.secondary)
                ForEach(names, id: \.self) { n in MachineTile(name: n) { go($0, n, $1) } }
            }
            .padding(10)
            .background(.bar)
        }
    }
}

struct MachineTile: View {
    let name: String
    let go: (_ ref: TerminalRef, _ clone: Bool) -> Void
    @ViewState private var over = false
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let label = model.machineLabel(name)
        let fill: AnyShapeStyle = over ? AnyShapeStyle(Color.accentColor.opacity(0.18)) : AnyShapeStyle(.quaternary.opacity(0.5))
        let edge: Color = over ? .accentColor : .secondary.opacity(0.4)
        return Label(label, systemImage: "desktopcomputer")
            .frame(maxWidth: .infinity, minHeight: 34)
            .background(fill, in: RoundedRectangle(cornerRadius: 7))
            .overlay(RoundedRectangle(cornerRadius: 7).strokeBorder(edge, style: StrokeStyle(lineWidth: 1, dash: [4, 3])))
            .contentShape(Rectangle())
            .help("Drop a terminal here to move its conversation to \(label); hold ⌥ to clone it")
            .accessibilityLabel("Move to \(label)")
            .onDrop(of: [.utf8PlainText, .plainText], isTargeted: $over) { providers in
                AppModel.droppedRef(providers) { go($0, AppModel.cloneModifier) }
            }
    }
}

/// The grip at a pane's top corner: drag it onto a machine, or right-click for Move/Clone. Shows a running move.
struct PaneGrip: View {
    let ref: TerminalRef
    var moved: @MainActor (TerminalRef) -> Void
    @ViewState private var failure: String?
    private var model: AppModel { AppModel.shared }

    var body: some View {
        HStack(spacing: 6) {
            if let m = model.migrations[ref], let p = m.current {
                Text(m.error.map { "Failed: \($0)" } ?? (m.done ? "\(m.isClone ? "Cloned" : "Moved") to \(model.machineLabel(m.to))"
                                                         : "\(Migration.label(p.name, to: model.machineLabel(m.to)))…"))
                    .font(.caption).lineLimit(1)
            }
            Image(systemName: "line.3.horizontal")
                .frame(width: 22, height: 18)
                .contentShape(Rectangle())
                .draggable(ref.dragText)
                .help("Drag onto a machine in the sidebar to move this terminal (hold ⌥ to clone); right-click for the menu")
                .accessibilityLabel("Move or clone this terminal")
        }
        .padding(.horizontal, 6).padding(.vertical, 3)
        .background(.regularMaterial, in: Capsule())
        .padding(6)
        .contextMenu {
            MoveMenus(ref: ref) { n, clone in
                model.startMigration(ref, to: n, clone: clone, done: moved, failed: { failure = $0 })
            }
        }
        .alert("Couldn't move the terminal", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
            Button("OK") { failure = nil }
        } message: { Text(failure ?? "") }
    }
}

struct MoveFolderPin: Decodable {
    var fromHost: String
    var sourcePath: String
    var to: String
    var destinationPath: String
    enum CodingKeys: String, CodingKey { case fromHost = "from_host", sourcePath = "source_path", to, destinationPath = "destination_path" }
}
