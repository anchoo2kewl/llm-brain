import AppKit
import SwiftUI

/// The shared look (docs/design.md): JetBrains Mono for the app text, the session list's attention colour, and how
/// ended sessions fade. build-app.sh bundles the font; without it SwiftUI falls back to the system font.
extension Font {
    static let appFamily = "JetBrainsMono Nerd Font"
    static func app(_ size: CGFloat, _ weight: Font.Weight = .regular) -> Font { .custom(appFamily, size: size).weight(weight) }
    static let appBody = app(13)
    static let appCaption = app(11)
    static let appCaption2 = app(10)
}

enum SessionStyle {
    /// "Finished its turn" or waiting for you: the dot and the title, never the row background.
    /// The hub's --st-waiting token: #b45309 in light mode, #fbbf24 in dark mode.
    static let attention = Color(nsColor: NSColor(name: nil) { a in
        a.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            ? NSColor(srgbRed: 0xfb / 255, green: 0xbf / 255, blue: 0x24 / 255, alpha: 1)
            : NSColor(srgbRed: 0xb4 / 255, green: 0x53 / 255, blue: 0x09 / 255, alpha: 1)
    })
    static let endedOpacity = 0.55
    /// A row moving to its new place in the list: short and calm; none with Reduce Motion.
    static var move: Animation? {
        NSWorkspace.shared.accessibilityDisplayShouldReduceMotion ? nil : .easeInOut(duration: 0.2)
    }
    /// The hub's --st-failed token: the slow red pulse of a session that waits for you.
    static let needsYou = Color(nsColor: NSColor(name: nil) { a in
        a.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua
            ? NSColor(srgbRed: 0xf8 / 255, green: 0x71 / 255, blue: 0x71 / 255, alpha: 1)
            : NSColor(srgbRed: 0xc2 / 255, green: 0x26 / 255, blue: 0x1b / 255, alpha: 1)
    })
}
