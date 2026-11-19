use std::fs::{DirBuilder, OpenOptions};
use std::io;
use std::os::fd::AsRawFd;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::os::unix::net::UnixDatagram;
use tauri::{AppHandle, Manager};

extern "C" {
    fn flock(fd: i32, operation: i32) -> i32;
}

/// Hold a per-user application lock and let later launches activate the first window.
/// The kernel releases the lock on a crash, so a stale socket cannot prevent a restart.
pub fn claim(app: &AppHandle) -> Result<bool, Box<dyn std::error::Error>> {
    let directory = app.path().app_config_dir()?.join("instance");
    DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(&directory)?;
    let lock = OpenOptions::new()
        .create(true)
        .truncate(false)
        .read(true)
        .write(true)
        .mode(0o600)
        .open(directory.join("lock"))?;
    // Linux flock LOCK_EX | LOCK_NB. Keep the file alive in the listener thread.
    if unsafe { flock(lock.as_raw_fd(), 2 | 4) } != 0 {
        let error = io::Error::last_os_error();
        if error.kind() != io::ErrorKind::WouldBlock {
            return Err(error.into());
        }
        let sender = UnixDatagram::unbound()?;
        sender.send_to(b"activate", directory.join("activate.sock"))?;
        return Ok(false);
    }
    let path = directory.join("activate.sock");
    match std::fs::remove_file(&path) {
        Ok(()) => {}
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(error.into()),
    }
    let socket = UnixDatagram::bind(path)?;
    let app = app.clone();
    std::thread::spawn(move || {
        let _lock = lock;
        let mut data = [0; 8];
        while let Ok(length) = socket.recv(&mut data) {
            if &data[..length] == b"activate" {
                super::show_main(&app);
            }
        }
    });
    Ok(true)
}
