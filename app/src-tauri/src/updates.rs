use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;
use tauri::{AppHandle, Manager};
use tauri_plugin_notification::NotificationExt;

static CHECKING: AtomicBool = AtomicBool::new(false);
/// The version the helper installed in this run. The app shows "Restart to Update" until it restarts.
static INSTALLED: Mutex<Option<String>> = Mutex::new(None);

/// The version in the helper's "Installed 0.1.12. Restart Brain Hub to use it." line.
pub fn installed_version(output: &str) -> Option<&str> {
    let v = output.strip_prefix("Installed ")?.split(". ").next()?.trim_end_matches('.');
    (!v.is_empty() && v.split('.').all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()))).then_some(v)
}

pub fn pending() -> Option<String> {
    INSTALLED.lock().unwrap_or_else(|e| e.into_inner()).clone()
}

/// Start the installed AppImage after this process ends, then end this process. The new process waits for this
/// one to exit first, because the single-instance lock sends a second instance away while the first one runs.
pub fn restart(app: &AppHandle) -> Result<(), String> {
    if pending().is_none() {
        return Err("No update waits for a restart.".into());
    }
    let target = std::env::var_os("APPIMAGE").ok_or("Only the AppImage installs updates itself.")?;
    std::process::Command::new("/bin/sh")
        .args(["-c", r#"while kill -0 "$1" 2>/dev/null; do sleep 0.2; done; exec "$2""#, "sh"])
        .arg(std::process::id().to_string())
        .arg(target)
        .env_remove("APPIMAGE")
        .env_remove("APPDIR")
        .spawn()
        .map_err(|e| format!("Cannot restart: {e}"))?;
    app.exit(0);
    Ok(())
}

fn preference(app: &AppHandle) -> tauri::Result<std::path::PathBuf> {
    Ok(app
        .path()
        .app_config_dir()?
        .join("disable-automatic-updates"))
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

pub fn check(app: &AppHandle, manual: bool) {
    if (!manual && !automatic(app)) || CHECKING.swap(true, Ordering::AcqRel) {
        return;
    }
    let app = app.clone();
    std::thread::spawn(move || {
        let result = (|| -> Result<String, Box<dyn std::error::Error>> {
            let helper = app.path().resource_dir()?.join("brain-update");
            let out = std::process::Command::new(helper)
                .arg(app.package_info().version.to_string())
                .arg(std::env::var_os("APPIMAGE").unwrap_or_default())
                .output()?;
            if !out.status.success() {
                return Err(String::from_utf8_lossy(&out.stderr).into_owned().into());
            }
            Ok(String::from_utf8_lossy(&out.stdout).trim().to_owned())
        })();
        CHECKING.store(false, Ordering::Release);
        if let Some(v) = result.as_deref().ok().and_then(installed_version) {
            *INSTALLED.lock().unwrap_or_else(|e| e.into_inner()) = Some(v.to_owned());
            crate::refresh_restart(&app);
        }
        if manual
            || result
                .as_ref()
                .is_ok_and(|s| s.starts_with("Installed ") || s.starts_with("Version "))
        {
            let body = result.unwrap_or_else(|err| format!("Update failed: {err}"));
            let _ = app
                .notification()
                .builder()
                .title("Brain Hub Updates")
                .body(body)
                .show();
        }
    });
}

#[cfg(test)]
mod tests {
    use super::installed_version;

    #[test]
    fn installed_line_gives_the_version() {
        assert_eq!(installed_version("Installed 0.1.12. Restart Brain Hub to use it."), Some("0.1.12"));
        assert_eq!(installed_version("Installed 1.20.3."), Some("1.20.3"));
        for other in ["You’re up to date.", "Version 0.1.12 is available. Update through…", "Installed . x", "Installed 0.1.x. y", ""] {
            assert_eq!(installed_version(other), None, "{other}");
        }
    }
}
