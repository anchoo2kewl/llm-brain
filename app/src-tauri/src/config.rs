//! The saved hub list, the selected hub and the hotkey. One JSON file in the app config directory.

use crate::allow;
use serde::{Deserialize, Serialize};
use std::fs;
use std::path::Path;

/// The default quick-search hotkey. Linux desktops often take Alt+Space for the window menu, so Linux uses
/// Control+Alt+Space.
#[cfg(target_os = "macos")]
pub const DEFAULT_HOTKEY: &str = "Alt+Space";
#[cfg(not(target_os = "macos"))]
pub const DEFAULT_HOTKEY: &str = "Control+Alt+Space";
const MAX_NAME: usize = 64;

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
pub struct Hub {
    pub name: String,
    pub url: String,
}

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
pub struct Config {
    #[serde(default)]
    pub hubs: Vec<Hub>,
    /// URL of the selected hub. Always one of `hubs`.
    #[serde(default)]
    pub selected: Option<String>,
    #[serde(default = "default_hotkey")]
    pub hotkey: String,
}

fn default_hotkey() -> String {
    DEFAULT_HOTKEY.to_string()
}

impl Default for Config {
    fn default() -> Self {
        Config { hubs: Vec::new(), selected: None, hotkey: default_hotkey() }
    }
}

fn clean_name(name: &str, url: &str) -> Result<String, String> {
    let name = name.trim();
    let name = if name.is_empty() {
        url.split("://").nth(1).unwrap_or(url).to_string()
    } else {
        name.to_string()
    };
    if name.chars().count() > MAX_NAME {
        return Err(format!("The name is too long (at most {MAX_NAME} characters)."));
    }
    if name.chars().any(char::is_control) {
        return Err("The name must not contain control characters.".into());
    }
    Ok(name)
}

impl Config {
    /// Read the file. A missing file gives the default. A broken file is kept as `*.bad` and gives the default.
    /// Hubs with a disallowed host are dropped, so a hand-edited file cannot point the app elsewhere.
    pub fn load(path: &Path) -> Config {
        let text = match fs::read_to_string(path) {
            Ok(t) => t,
            Err(_) => return Config::default(),
        };
        let mut cfg: Config = match serde_json::from_str(&text) {
            Ok(c) => c,
            Err(e) => {
                eprintln!("config: {} is not valid ({e}); keeping it as .bad", path.display());
                let _ = fs::rename(path, path.with_extension("json.bad"));
                return Config::default();
            }
        };
        cfg.hubs.retain(|h| match allow::validate_hub_url(&h.url) {
            Ok(u) => u == h.url,
            Err(e) => {
                eprintln!("config: dropped hub {:?}: {e}", h.name);
                false
            }
        });
        cfg.fix_selection();
        cfg
    }

    /// Write the file atomically (temporary file, then rename).
    pub fn save(&self, path: &Path) -> Result<(), String> {
        if let Some(dir) = path.parent() {
            fs::create_dir_all(dir).map_err(|e| format!("Cannot create {}: {e}", dir.display()))?;
        }
        let tmp = path.with_extension("json.tmp");
        let json = serde_json::to_string_pretty(self).map_err(|e| e.to_string())?;
        fs::write(&tmp, json + "\n").map_err(|e| format!("Cannot write {}: {e}", tmp.display()))?;
        fs::rename(&tmp, path).map_err(|e| format!("Cannot save {}: {e}", path.display()))
    }

    fn fix_selection(&mut self) {
        let valid = self.selected.as_ref().is_some_and(|s| self.hubs.iter().any(|h| &h.url == s));
        if !valid {
            self.selected = self.hubs.first().map(|h| h.url.clone());
        }
    }

    pub fn selected_hub(&self) -> Option<&Hub> {
        let s = self.selected.as_ref()?;
        self.hubs.iter().find(|h| &h.url == s)
    }

    /// Add a hub. The first hub added becomes the selected one. Returns the normalised URL.
    pub fn add(&mut self, name: &str, url: &str) -> Result<String, String> {
        let url = allow::validate_hub_url(url)?;
        if self.hubs.iter().any(|h| h.url == url) {
            return Err(format!("{url} is already in the list."));
        }
        let name = clean_name(name, &url)?;
        self.hubs.push(Hub { name, url: url.clone() });
        self.fix_selection();
        Ok(url)
    }

    pub fn remove(&mut self, url: &str) -> Result<(), String> {
        let before = self.hubs.len();
        self.hubs.retain(|h| h.url != url);
        if self.hubs.len() == before {
            return Err("No such hub.".into());
        }
        self.fix_selection();
        Ok(())
    }

    pub fn rename(&mut self, url: &str, name: &str) -> Result<(), String> {
        let name = clean_name(name, url)?;
        let hub = self.hubs.iter_mut().find(|h| h.url == url).ok_or("No such hub.")?;
        hub.name = name;
        Ok(())
    }

    pub fn select(&mut self, url: &str) -> Result<(), String> {
        if !self.hubs.iter().any(|h| h.url == url) {
            return Err("No such hub.".into());
        }
        self.selected = Some(url.to_string());
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn tmp(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("brain-hub-test-{}-{name}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        dir.join("hubs.json")
    }

    #[test]
    fn round_trip() {
        let path = tmp("roundtrip");
        let mut c = Config::default();
        c.add("Studio", "https://studio.tail1.ts.net/").unwrap();
        c.add("Local", "http://127.0.0.1:8787").unwrap();
        c.select("http://127.0.0.1:8787").unwrap();
        c.hotkey = "Control+Shift+K".into();
        c.save(&path).unwrap();
        let back = Config::load(&path);
        assert_eq!(back, c);
        assert_eq!(back.selected_hub().unwrap().name, "Local");
        assert!(!path.with_extension("json.tmp").exists());
    }

    #[test]
    fn missing_file_gives_default() {
        let c = Config::load(&tmp("missing"));
        assert_eq!(c, Config::default());
        assert_eq!(c.hotkey, DEFAULT_HOTKEY);
    }

    #[test]
    fn broken_file_is_kept_and_replaced_by_default() {
        let path = tmp("broken");
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(&path, "{not json").unwrap();
        assert_eq!(Config::load(&path), Config::default());
        assert!(path.with_extension("json.bad").exists());
    }

    #[test]
    fn hand_edited_bad_host_is_dropped() {
        let path = tmp("badhost");
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(
            &path,
            r#"{"hubs":[{"name":"x","url":"https://example.com"},{"name":"ok","url":"http://127.0.0.1:8787"}],"selected":"https://example.com"}"#,
        )
        .unwrap();
        let c = Config::load(&path);
        assert_eq!(c.hubs.len(), 1);
        assert_eq!(c.selected.as_deref(), Some("http://127.0.0.1:8787"));
    }

    #[test]
    fn add_remove_rename_select() {
        let mut c = Config::default();
        c.add("A", "http://127.0.0.1:1").unwrap();
        assert_eq!(c.selected.as_deref(), Some("http://127.0.0.1:1"), "first hub is selected");
        c.add("", "http://127.0.0.1:2/ignored").unwrap();
        assert_eq!(c.hubs[1].name, "127.0.0.1:2", "empty name falls back to the host");
        assert!(c.add("dup", "http://127.0.0.1:2").is_err());
        assert!(c.add("bad", "https://example.com").is_err());
        c.rename("http://127.0.0.1:2", "  Two ").unwrap();
        assert_eq!(c.hubs[1].name, "Two");
        assert!(c.rename("http://127.0.0.1:9", "x").is_err());
        assert!(c.rename("http://127.0.0.1:2", &"x".repeat(65)).is_err());
        c.select("http://127.0.0.1:2").unwrap();
        c.remove("http://127.0.0.1:2").unwrap();
        assert_eq!(c.selected.as_deref(), Some("http://127.0.0.1:1"), "selection moves to a remaining hub");
        c.remove("http://127.0.0.1:1").unwrap();
        assert!(c.selected.is_none() && c.hubs.is_empty());
        assert!(c.remove("http://127.0.0.1:1").is_err());
    }
}
