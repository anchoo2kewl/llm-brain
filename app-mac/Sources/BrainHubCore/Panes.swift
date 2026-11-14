import CoreGraphics

/// The panes of one window, like a tmux window: a tree of splits with one terminal (or none yet) in each pane.
public struct PaneTree: Equatable, Sendable {
    public indirect enum Node: Equatable, Sendable {
        case leaf(Int)
        /// `ratio` is the share of the first child.
        case split(id: Int, axis: Axis, ratio: Double, Node, Node)
    }

    public static let maxPanes = 9

    public private(set) var root: Node = .leaf(0)
    public private(set) var focused = 0
    public private(set) var zoomed: Int?
    public private(set) var terminals: [Int: TerminalRef] = [:]
    private var nextID = 1

    public init(terminal: TerminalRef? = nil) { terminals[0] = terminal }

    /// Pane ids, left to right and top to bottom.
    public var panes: [Int] { Self.leaves(root) }
    public var focusedTerminal: TerminalRef? { terminals[focused] }

    public func pane(showing ref: TerminalRef) -> Int? { terminals.first { $0.value == ref }?.key }

    /// Show a terminal in the focused pane, or focus the pane that already shows it.
    public mutating func show(_ ref: TerminalRef) {
        if let p = pane(showing: ref) { focus(p) } else { terminals[focused] = ref }
    }

    public mutating func focus(_ pane: Int) {
        guard panes.contains(pane) else { return }
        focused = pane
        if zoomed != nil && zoomed != pane { zoomed = nil }
    }

    /// Split the focused pane; the new, empty pane gets the focus. False at the limit.
    @discardableResult
    public mutating func split(_ axis: Axis) -> Bool {
        guard panes.count < Self.maxPanes else { return false }
        let new = nextID, splitID = nextID + 1
        nextID += 2
        root = Self.replace(root, focused) { .split(id: splitID, axis: axis, ratio: 0.5, $0, .leaf(new)) }
        zoomed = nil
        focused = new
        return true
    }

    /// Close the focused pane (the terminal keeps running on its machine). The last pane only empties.
    public mutating func close() {
        guard panes.count > 1 else { terminals[focused] = nil; return }
        let order = panes, i = order.firstIndex(of: focused) ?? 0
        root = Self.remove(root, focused) ?? .leaf(order[0])
        terminals[focused] = nil
        zoomed = nil
        let left = panes
        focused = left[min(i, left.count - 1)]
    }

    public mutating func toggleZoom() { zoomed = zoomed == nil && panes.count > 1 ? focused : nil }

    public mutating func focusNext(_ delta: Int) {
        let order = panes
        guard let i = order.firstIndex(of: focused) else { return }
        focus(order[((i + delta) % order.count + order.count) % order.count])
    }

    /// Focus the nearest pane in a direction, as tmux's select-pane -L/-R/-U/-D.
    public mutating func focus(_ d: Direction) {
        let f = frames()
        guard let me = f[focused] else { return }
        let eps = 0.0001
        let candidates = f.filter { id, r in
            guard id != focused else { return false }
            switch d {
            case .left: return r.maxX <= me.minX + eps && r.maxY > me.minY + eps && r.minY < me.maxY - eps
            case .right: return r.minX >= me.maxX - eps && r.maxY > me.minY + eps && r.minY < me.maxY - eps
            case .up: return r.maxY <= me.minY + eps && r.maxX > me.minX + eps && r.minX < me.maxX - eps
            case .down: return r.minY >= me.maxY - eps && r.maxX > me.minX + eps && r.minX < me.maxX - eps
            }
        }
        func gap(_ r: CGRect) -> Double {
            switch d {
            case .left: me.minX - r.maxX
            case .right: r.minX - me.maxX
            case .up: me.minY - r.maxY
            case .down: r.minY - me.maxY
            }
        }
        if let best = candidates.min(by: { (gap($0.value), $0.key) < (gap($1.value), $1.key) }) { focus(best.key) }
    }

    /// Move the border of the focused pane, as tmux's resize-pane: the nearest split along that axis, `cells`×5%.
    public mutating func resize(_ d: Direction, cells: Int = 1) {
        let axis: Axis = d == .left || d == .right ? .horizontal : .vertical
        let delta = (d == .left || d == .up ? -0.05 : 0.05) * Double(cells)
        guard let id = Self.nearestSplit(root, focused, axis) else { return }
        root = Self.adjust(root, id) { min(max($0 + delta, 0.1), 0.9) }
    }

    public mutating func setRatio(split id: Int, _ ratio: Double) {
        root = Self.adjust(root, id) { _ in min(max(ratio, 0.1), 0.9) }
    }

    /// Each pane's frame in the unit square (y grows downwards).
    public func frames() -> [Int: CGRect] {
        var out: [Int: CGRect] = [:]
        func walk(_ n: Node, _ r: CGRect) {
            switch n {
            case .leaf(let id): out[id] = r
            case let .split(_, axis, ratio, a, b):
                if axis == .horizontal {
                    let w = r.width * ratio
                    walk(a, CGRect(x: r.minX, y: r.minY, width: w, height: r.height))
                    walk(b, CGRect(x: r.minX + w, y: r.minY, width: r.width - w, height: r.height))
                } else {
                    let h = r.height * ratio
                    walk(a, CGRect(x: r.minX, y: r.minY, width: r.width, height: h))
                    walk(b, CGRect(x: r.minX, y: r.minY + h, width: r.width, height: r.height - h))
                }
            }
        }
        walk(root, CGRect(x: 0, y: 0, width: 1, height: 1))
        return out
    }

    static func leaves(_ n: Node) -> [Int] {
        switch n {
        case .leaf(let id): [id]
        case let .split(_, _, _, a, b): leaves(a) + leaves(b)
        }
    }

    static func replace(_ n: Node, _ leaf: Int, _ with: (Node) -> Node) -> Node {
        switch n {
        case .leaf(let id): id == leaf ? with(n) : n
        case let .split(id, axis, ratio, a, b): .split(id: id, axis: axis, ratio: ratio, replace(a, leaf, with), replace(b, leaf, with))
        }
    }

    static func remove(_ n: Node, _ leaf: Int) -> Node? {
        switch n {
        case .leaf(let id): return id == leaf ? nil : n
        case let .split(id, axis, ratio, a, b):
            switch (remove(a, leaf), remove(b, leaf)) {
            case let (a?, b?): return .split(id: id, axis: axis, ratio: ratio, a, b)
            case let (a?, nil): return a
            case let (nil, b?): return b
            case (nil, nil): return nil
            }
        }
    }

    static func nearestSplit(_ n: Node, _ leaf: Int, _ axis: Axis) -> Int? {
        guard case let .split(id, ax, _, a, b) = n else { return nil }
        let inA = leaves(a).contains(leaf), inB = leaves(b).contains(leaf)
        guard inA || inB else { return nil }
        return nearestSplit(inA ? a : b, leaf, axis) ?? (ax == axis ? id : nil)
    }

    static func adjust(_ n: Node, _ split: Int, _ f: (Double) -> Double) -> Node {
        guard case let .split(id, axis, ratio, a, b) = n else { return n }
        return .split(id: id, axis: axis, ratio: id == split ? f(ratio) : ratio, adjust(a, split, f), adjust(b, split, f))
    }
}
