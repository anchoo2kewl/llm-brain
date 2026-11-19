use std::sync::atomic::{AtomicBool, Ordering};
use tauri::{AppHandle, Manager};
use tauri_plugin_notification::NotificationExt;

static CHECKING: AtomicBool = AtomicBool::new(false);

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
