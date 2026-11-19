import SwiftUI
import WebKit

/// What a window shows on the right: the hub's own pages, the terminal panes, or both side by side.
enum WindowLayout: String, CaseIterable {
    case hub, terminal, both

    var title: String {
        switch self {
        case .hub: "Brain Hub"
        case .terminal: "Terminal"
        case .both: "Both"
        }
    }

    var icon: String {
        switch self {
        case .hub: "house"
        case .terminal: "terminal"
        case .both: "rectangle.righthalf.inset.filled"
        }
    }
}

/// The hub's web pages (Home, sessions, search, settings) in the window. The window keeps one web view, so a change
/// between layouts keeps the page where the user went.
struct HubPage: NSViewRepresentable {
    let web: WKWebView
    let url: URL

    func makeNSView(context: Context) -> WKWebView {
        web.allowsBackForwardNavigationGestures = true
        if web.url == nil { web.load(URLRequest(url: url)) }
        return web
    }

    func updateNSView(_ web: WKWebView, context: Context) {
        // Another hub was selected: show its pages. Links inside the same hub stay where the user went.
        if web.url?.host != url.host { web.load(URLRequest(url: url)) }
    }
}
