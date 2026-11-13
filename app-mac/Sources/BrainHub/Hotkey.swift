import AppKit
import BrainHubCore
import Carbon.HIToolbox

/// The system-wide "bring Brain Hub forward" key. Carbon's RegisterEventHotKey needs no Accessibility permission.
@MainActor
enum GlobalHotkey {
    static let defaultsKey = "hotkey.global"
    static let fallback = "C-`"
    private static var ref: EventHotKeyRef?
    private static var installed = false

    /// The key in tmux notation (C- Control, M- Option, S- Shift), from Settings or the default.
    static var text: String { UserDefaults.standard.string(forKey: defaultsKey).flatMap { $0.isEmpty ? nil : $0 } ?? fallback }

    /// Register the key from Settings. False when this key cannot be a hotkey or another app holds it.
    @discardableResult
    static func register() -> Bool {
        if !installed {
            installed = true
            var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
            InstallEventHandler(GetApplicationEventTarget(), { _, _, _ in
                MainActor.assumeIsolated { AppModel.shared.showMain() }
                return noErr
            }, 1, &spec, nil, nil)
        }
        if let ref { UnregisterEventHotKey(ref) }
        ref = nil
        guard let key = Key(tmux: text), let code = keyCode(key.name) else { return false }
        var mods: UInt32 = 0
        if key.ctrl { mods |= UInt32(controlKey) }
        if key.meta { mods |= UInt32(optionKey) }
        if key.shift { mods |= UInt32(shiftKey) }
        let id = EventHotKeyID(signature: OSType(0x4248_4B59), id: 1) // "BHKY"
        return RegisterEventHotKey(code, mods, id, GetApplicationEventTarget(), 0, &ref) == noErr
    }

    /// Virtual key codes (ANSI layout) for the keys a hotkey can use.
    static func keyCode(_ name: String) -> UInt32? {
        let table: [String: Int] = [
            "a": kVK_ANSI_A, "b": kVK_ANSI_B, "c": kVK_ANSI_C, "d": kVK_ANSI_D, "e": kVK_ANSI_E, "f": kVK_ANSI_F,
            "g": kVK_ANSI_G, "h": kVK_ANSI_H, "i": kVK_ANSI_I, "j": kVK_ANSI_J, "k": kVK_ANSI_K, "l": kVK_ANSI_L,
            "m": kVK_ANSI_M, "n": kVK_ANSI_N, "o": kVK_ANSI_O, "p": kVK_ANSI_P, "q": kVK_ANSI_Q, "r": kVK_ANSI_R,
            "s": kVK_ANSI_S, "t": kVK_ANSI_T, "u": kVK_ANSI_U, "v": kVK_ANSI_V, "w": kVK_ANSI_W, "x": kVK_ANSI_X,
            "y": kVK_ANSI_Y, "z": kVK_ANSI_Z, "0": kVK_ANSI_0, "1": kVK_ANSI_1, "2": kVK_ANSI_2, "3": kVK_ANSI_3,
            "4": kVK_ANSI_4, "5": kVK_ANSI_5, "6": kVK_ANSI_6, "7": kVK_ANSI_7, "8": kVK_ANSI_8, "9": kVK_ANSI_9,
            "`": kVK_ANSI_Grave, "-": kVK_ANSI_Minus, "=": kVK_ANSI_Equal, "[": kVK_ANSI_LeftBracket,
            "]": kVK_ANSI_RightBracket, "\\": kVK_ANSI_Backslash, ";": kVK_ANSI_Semicolon, "'": kVK_ANSI_Quote,
            ",": kVK_ANSI_Comma, ".": kVK_ANSI_Period, "/": kVK_ANSI_Slash, "Space": kVK_Space, "Enter": kVK_Return,
            "Tab": kVK_Tab, "Escape": kVK_Escape, "F1": kVK_F1, "F2": kVK_F2, "F3": kVK_F3, "F4": kVK_F4, "F5": kVK_F5,
            "F6": kVK_F6, "F7": kVK_F7, "F8": kVK_F8, "F9": kVK_F9, "F10": kVK_F10, "F11": kVK_F11, "F12": kVK_F12,
        ]
        return table[name.count == 1 ? name.lowercased() : name].map(UInt32.init)
    }
}
