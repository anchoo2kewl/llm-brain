//! Client updates on Linux and Windows: check on start, every hour and after a wake from sleep. The brain-update
//! helper finds the newest published release, downloads it and verifies its SHA-256 digest. The app then shows
//! "Restart to Update" (Chrome style). Sessions run on the hub, so a restart only reopens this window.
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use std::time::{Duration, SystemTime};
use tauri::{AppHandle, Manager};
use tauri_plugin_notification::NotificationExt;

const RELEASES: &str = "https://github.com/anchoo2kewl/llm-brain/releases/tag/client-v";
pub const RESTART_NOTE: &str = "Sessions run on the hub and stay open. A restart only reopens the app.";
const EVERY: Duration = Duration::from_secs(60 * 60);
const TICK: Duration = Duration::from_secs(60);

static CHECKING: AtomicBool = AtomicBool::new(false);

#[derive(Default)]
struct State {
    /// Installed (AppImage) or downloaded (Windows setup) in this run: the app shows "Restart to Update".
    ready: Option<String>,
    /// A newer release that this run did not install: the app shows "Update Available".
    available: Option<String>,
    /// The last version a background check told the user about, so each version notifies one time.
    told: Option<String>,
}
static STATE: Mutex<State> = Mutex::new(State { ready: None, available: None, told: None });

fn state() -> std::sync::MutexGuard<'static, State> {
    STATE.lock().unwrap_or_else(|e| e.into_inner())
}

/// The version in the helper's "Installed 0.1.12. Restart …" or "Downloaded 0.1.12. Restart …" line.
pub fn installed_version(output: &str) -> Option<&str> {
    let rest = output.strip_prefix("Installed ").or_else(|| output.strip_prefix("Downloaded "))?;
    valid(rest.split(". ").next()?.trim_end_matches('.'))
}

/// The version in the helper's check-only line "Version 0.1.13 is available: <url>".
pub fn available_version(output: &str) -> Option<&str> {
    valid(output.strip_prefix("Version ")?.split(" is available").next()?)
}

fn valid(v: &str) -> Option<&str> {
    let parts: Vec<&str> = v.split('.').collect();
    (parts.len() == 3 && parts.iter().all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()))).then_some(v)
}

/// A check is due on start, an hour after the last one, and after a wake from sleep. Wall-clock time moves
/// during sleep, so a tick that comes much later than `TICK` means the computer slept. A clock that moved back
/// also starts a check.
pub fn due(now: SystemTime, last_check: Option<SystemTime>, last_tick: SystemTime) -> bool {
    let Some(last) = last_check else { return true };
    let since = |t: SystemTime| now.duration_since(t).unwrap_or(Duration::MAX);
    since(last) >= EVERY || since(last_tick) >= 3 * TICK
}

pub fn pending() -> Option<String> {
    state().ready.clone()
}

pub fn available() -> Option<String> {
    state().available.clone()
}

/// Why this install cannot update itself, or None when it can.
pub fn blocked() -> Option<&'static str> {
    if cfg!(target_os = "linux") {
        std::env::var_os("APPIMAGE")
            .is_none()
            .then_some("A .deb or .rpm install needs root to update. Download the new package from the release page.")
    } else if cfg!(windows) {
        (!nsis_install()).then_some(
            "This install (MSI) needs administrator rights to update. Download the new installer from the release page.",
        )
    } else {
        Some("This build does not install updates itself.")
    }
}

/// The NSIS setup installs for the current user under %LOCALAPPDATA%; the MSI installs under Program Files.
fn nsis_install() -> bool {
    match (std::env::current_exe(), std::env::var_os("LOCALAPPDATA")) {
        (Ok(exe), Some(local)) => exe.starts_with(local),
        _ => false,
    }
}

/// Where the helper installs: the AppImage itself, or the Windows setup that the restart runs.
fn target(app: &AppHandle) -> Option<std::path::PathBuf> {
    if blocked().is_some() {
        return None;
    }
    if cfg!(windows) {
        let dir = app.path().app_cache_dir().ok()?;
        std::fs::create_dir_all(&dir).ok()?;
        return Some(dir.join("Brain Hub setup.exe"));
    }
    std::env::var_os("APPIMAGE").map(Into::into)
}

#[cfg(windows)]
fn setup_path(app: &AppHandle) -> Result<std::path::PathBuf, String> {
    target(app).ok_or_else(|| "No update setup is ready.".to_owned())
}

/// Start the new version after this process ends, then end this process. The new process waits for this one
/// to exit first: the single-instance lock sends a second instance away, and Windows cannot replace a running
/// program. Paths go to the child as arguments or environment values, never as script text.
pub fn restart(app: &AppHandle) -> Result<(), String> {
    if pending().is_none() {
        return Err("No update waits for a restart.".into());
    }
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        // NSIS template of the pinned Tauri bundler: /S installs silently, /R starts the app when it is done.
        std::process::Command::new("powershell.exe")
            .args(["-NoProfile", "-NonInteractive", "-Command",
                "Wait-Process -Id $env:BRAIN_PID -ErrorAction SilentlyContinue; Start-Process -FilePath $env:BRAIN_SETUP -ArgumentList '/S','/R'"])
            .env("BRAIN_PID", std::process::id().to_string())
            .env("BRAIN_SETUP", setup_path(app)?)
            .creation_flags(CREATE_NO_WINDOW)
            .spawn()
            .map_err(|e| format!("Cannot restart: {e}"))?;
    }
    #[cfg(not(windows))]
    {
        let target = std::env::var_os("APPIMAGE").ok_or("Only the AppImage installs updates itself.")?;
        std::process::Command::new("/bin/sh")
            .args(["-c", r#"while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec "$2""#, "sh"])
            .arg(std::process::id().to_string())
            .arg(target)
            .env_remove("APPIMAGE")
            .env_remove("APPDIR")
            .spawn()
            .map_err(|e| format!("Cannot restart: {e}"))?;
    }
    app.exit(0);
    Ok(())
}

fn preference(app: &AppHandle) -> tauri::Result<std::path::PathBuf> {
    Ok(app.path().app_config_dir()?.join("disable-automatic-updates"))
}

pub fn automatic(app: &AppHandle) -> bool {
    preference(app).is_ok_and(|path| !path.exists())
}

pub fn toggle(app: &AppHandle) -> Result<(), Box<dyn std::error::Error>> {
    let path = preference(app)?;
    if path.exists() {
        std::fs::remove_file(path)?;
    } else {
        std::fs::create_dir_all(app.path().app_config_dir()?)?;
        std::fs::write(path, [])?;
    }
    Ok(())
}

/// Check on start, then every hour and after a wake from sleep.
pub fn schedule(app: &AppHandle) {
    let app = app.clone();
    std::thread::spawn(move || {
        let (mut last_check, mut last_tick) = (None, SystemTime::now());
        loop {
            let now = SystemTime::now();
            if due(now, last_check, last_tick) {
                last_check = Some(now);
                check(&app, false);
            }
            last_tick = now;
            std::thread::sleep(TICK);
        }
    });
}

/// The "Update Available" action: install where this build can, else open the release page.
pub fn update_now(app: &AppHandle) {
    match (available(), blocked()) {
        (Some(v), Some(_)) => crate::open_release(&format!("{RELEASES}{v}")),
        _ => check(app, true),
    }
}

/// Run the helper. It installs when the user asked, or when automatic updates are on and this build can install;
/// otherwise it only checks. A check that is still running makes this call do nothing.
pub fn check(app: &AppHandle, manual: bool) {
    if CHECKING.swap(true, Ordering::AcqRel) {
        return;
    }
    let install = (manual || automatic(app)).then(|| target(app)).flatten();
    // Compare with the version already waiting for a restart, so the hourly check does not fetch it again.
    let current = pending().unwrap_or_else(|| app.package_info().version.to_string());
    let app = app.clone();
    std::thread::spawn(move || {
        let result = (|| -> Result<String, Box<dyn std::error::Error>> {
            let helper = app.path().resource_dir()?.join(if cfg!(windows) { "brain-update.exe" } else { "brain-update" });
            let mut cmd = std::process::Command::new(helper);
            cmd.arg(&current).arg(install.unwrap_or_default());
            #[cfg(windows)]
            std::os::windows::process::CommandExt::creation_flags(&mut cmd, 0x0800_0000);
            let out = cmd.output()?;
            if !out.status.success() {
                return Err(String::from_utf8_lossy(&out.stderr).trim().to_owned().into());
            }
            Ok(String::from_utf8_lossy(&out.stdout).trim().to_owned())
        })();
        CHECKING.store(false, Ordering::Release);
        let text = result.as_deref().unwrap_or_default();
        let news = {
            let mut s = state();
            if let Some(v) = installed_version(text) {
                s.ready = Some(v.to_owned());
                s.available = None;
            } else if let Some(v) = available_version(text) {
                s.available = Some(v.to_owned());
            }
            let latest = s.ready.clone().or_else(|| s.available.clone());
            let news = latest.is_some() && latest != s.told;
            if news {
                s.told = latest;
            }
            news
        };
        crate::refresh_restart(&app);
        if manual || news {
            let body = match &result {
                Ok(_) if available_version(text).is_some() => format!("{text}\n{}", blocked().unwrap_or("")),
                Ok(_) if installed_version(text).is_some() => format!("{text} {RESTART_NOTE}"),
                Ok(_) => text.to_owned(),
                Err(err) => format!("Update failed: {err}"),
            };
            let _ = app.notification().builder().title("Brain Hub Updates").body(body).show();
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn installed_line_gives_the_version() {
        assert_eq!(installed_version("Installed 0.1.12. Restart Brain Hub to use it."), Some("0.1.12"));
        assert_eq!(installed_version("Downloaded 0.1.13. Restart Brain Hub to install it."), Some("0.1.13"));
        assert_eq!(installed_version("Installed 1.20.3."), Some("1.20.3"));
        for other in ["You’re up to date.", "Version 0.1.12 is available: https://x", "Installed . x", "Installed 0.1.x. y", "Installed 0.1. y", ""] {
            assert_eq!(installed_version(other), None, "{other}");
        }
    }

    #[test]
    fn check_line_gives_the_available_version() {
        assert_eq!(available_version("Version 0.1.13 is available: https://github.com/x"), Some("0.1.13"));
        for other in ["You’re up to date.", "Installed 0.1.13. Restart", "Version x is available", "Version 0.1 is available"] {
            assert_eq!(available_version(other), None, "{other}");
        }
    }

    #[test]
    fn schedule_with_a_fake_clock() {
        let t0 = SystemTime::UNIX_EPOCH + Duration::from_secs(1_000_000);
        let at = |s: u64| t0 + Duration::from_secs(s);
        assert!(due(t0, None, t0), "first tick checks");
        assert!(!due(at(60), Some(t0), at(0)), "a minute later");
        assert!(!due(at(3599), Some(t0), at(3540)), "just under an hour");
        assert!(due(at(3600), Some(t0), at(3540)), "an hour later");
        assert!(due(at(900), Some(t0), at(60)), "wake: the last tick was 14 minutes ago");
        assert!(!due(at(900), Some(t0), at(840)), "normal tick");
        assert!(due(t0, Some(at(60)), at(60)), "clock moved back");
    }
}
