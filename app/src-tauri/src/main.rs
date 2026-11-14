//! Brain Hub: a small native client for the llm-brain hub.
//! The main window shows the hub's own web pages. This crate adds a hub switcher, a global quick-search
//! window, inbox notifications and a menu-bar icon. See docs/desktop-app.md.

mod allow;
mod config;
mod hub;
mod inbox;
mod search;

use config::{Config, Hub};
use serde::Serialize;
use std::str::FromStr;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, Instant};
use tauri::image::Image;
use tauri::menu::{CheckMenuItem, Menu, MenuBuilder, MenuItem, MenuItemBuilder, SubmenuBuilder};
use tauri::tray::TrayIconBuilder;
use tauri::webview::NewWindowResponse;
use tauri::{
    AppHandle, Emitter, LogicalSize, Manager, State, WebviewUrl, WebviewWindow, WebviewWindowBuilder,
    WindowEvent,
};
use tauri_plugin_global_shortcut::{GlobalShortcutExt, Shortcut, ShortcutState};
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_window_state::StateFlags;
use url::Url;

const POLL_EVERY: Duration = Duration::from_secs(30);
const APP_TITLE: &str = "Brain Hub";
const LOCAL_HUBS_PAGE: &str = "tauri://localhost/hubs.html";

struct AppState {
    cfg: Mutex<Config>,
    cfg_path: std::path::PathBuf,
    /// Highest search sequence number seen. A lower one is stale.
    search_seq: AtomicU64,
    hotkey_error: Mutex<Option<String>>,
    /// Wakes the inbox poller at once (after a hub switch).
    kick: Mutex<mpsc::Sender<()>>,
    search_shown_at: Mutex<Instant>,
}

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

// ---------------------------------------------------------------- guards

/// Commands change settings and talk to hubs. Only the app's own pages may call them, never a hub page
/// in the main window. (The capability file already gives hub pages no IPC; this is a second lock.)
fn require_local(w: &WebviewWindow) -> Result<(), String> {
    let url = w.url().map_err(|e| e.to_string())?;
    if url.scheme() == "tauri" || url.host_str() == Some("tauri.localhost") {
        Ok(())
    } else {
        Err("Not allowed from this page.".into())
    }
}

fn selected_url(state: &AppState) -> Option<String> {
    lock(&state.cfg).selected_hub().map(|h| h.url.clone())
}

fn persist(state: &AppState, cfg: &Config) -> Result<(), String> {
    cfg.save(&state.cfg_path)
}

// ---------------------------------------------------------------- windows

fn open_external(url: &Url) {
    if matches!(url.scheme(), "http" | "https") {
        let _ = std::process::Command::new("open").arg(url.as_str()).spawn();
    }
}

/// Main window navigation rule: hub hosts and the app's own pages stay in the window. Other web links
/// (GitHub, Slack) open in the default browser. Everything else is refused.
fn navigation_allowed(url: &Url) -> bool {
    match url.scheme() {
        "tauri" | "about" => true,
        "http" | "https" => {
            if allow::url_allowed(url) {
                true
            } else {
                open_external(url);
                false
            }
        }
        _ => false,
    }
}

fn build_main(app: &AppHandle, url: WebviewUrl) -> tauri::Result<WebviewWindow> {
    let handle = app.clone();
    let win = WebviewWindowBuilder::new(app, "main", url)
        .title(APP_TITLE)
        .inner_size(1280.0, 860.0)
        .min_inner_size(640.0, 480.0)
        .on_navigation(navigation_allowed)
        .on_new_window(move |url, _| {
            if allow::url_allowed(&url) {
                if let Some(w) = handle.get_webview_window("main") {
                    let _ = w.navigate(url);
                }
            } else {
                open_external(&url);
            }
            NewWindowResponse::Deny
        })
        .on_document_title_changed(|w, title| {
            let _ = w.set_title(if title.trim().is_empty() { APP_TITLE } else { &title });
        })
        .build()?;
    // Closing the window hides it. The app keeps running for the hotkey, the tray and notifications.
    let w = win.clone();
    win.on_window_event(move |e| {
        if let WindowEvent::CloseRequested { api, .. } = e {
            api.prevent_close();
            let _ = w.hide();
        }
    });
    Ok(win)
}

/// A terminal window: the selected hub's Workspace with its own layout (`?w=`), so terminals can be spread
/// over several windows and screens. It follows the main window's navigation rule and gets no app commands
/// (capabilities list only the app's own windows). Closing it closes it; the terminals keep running.
fn open_terminal_window(app: &AppHandle) {
    static NEXT: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(1);
    let state = app.state::<AppState>();
    let Some(base) = selected_url(&state) else {
        open_hubs_window(app);
        return;
    };
    let n = NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
    let label = format!("terms-{n}");
    let Ok(url) = Url::parse(&format!("{}/workspace?w=win{n}", base.trim_end_matches('/'))) else { return };
    if !allow::url_allowed(&url) {
        return;
    }
    let handle = app.clone();
    let own = label.clone();
    let built = WebviewWindowBuilder::new(app, &label, WebviewUrl::External(url))
        .title(format!("{APP_TITLE} · Terminals {n}"))
        .inner_size(1440.0, 900.0)
        .min_inner_size(640.0, 400.0)
        .on_navigation(navigation_allowed)
        .on_new_window(move |url, _| {
            if allow::url_allowed(&url) {
                if let Some(w) = handle.get_webview_window(&own) {
                    let _ = w.navigate(url);
                }
            } else {
                open_external(&url);
            }
            NewWindowResponse::Deny
        })
        .on_document_title_changed(move |w, title| {
            let _ = w.set_title(&format!("{} · Terminals {n}", if title.trim().is_empty() { APP_TITLE } else { title.as_str() }));
        })
        .build();
    match built {
        Ok(w) => {
            let _ = w.set_focus();
        }
        Err(e) => eprintln!("terminal window: {e}"),
    }
}

/// eval in the window in front (main or a terminal window), for Reload, Back and Forward.
fn focused_eval(app: &AppHandle, js: &str) {
    let front = app.webview_windows().into_values().find(|w| w.is_focused().unwrap_or(false) && (w.label() == "main" || w.label().starts_with("terms-")));
    match front {
        Some(w) => {
            let _ = w.eval(js);
        }
        None => main_eval(app, js),
    }
}

fn show_main(app: &AppHandle) {
    let win = match app.get_webview_window("main") {
        Some(w) => w,
        None => match build_main(app, WebviewUrl::App("hubs.html".into())) {
            Ok(w) => w,
            Err(e) => return eprintln!("cannot create the main window: {e}"),
        },
    };
    let _ = win.show();
    let _ = win.unminimize();
    let _ = win.set_focus();
}

fn navigate_main(app: &AppHandle, url: Url) -> Result<(), String> {
    show_main(app);
    let win = app.get_webview_window("main").ok_or("The main window is missing.")?;
    win.navigate(url).map_err(|e| e.to_string())
}

fn open_hubs_window(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("hubs") {
        let _ = w.show();
        let _ = w.set_focus();
        return;
    }
    let built = WebviewWindowBuilder::new(app, "hubs", WebviewUrl::App("hubs.html".into()))
        .title("Hubs")
        .inner_size(560.0, 640.0)
        .min_inner_size(460.0, 420.0)
        .build();
    if let Err(e) = built {
        eprintln!("cannot create the hubs window: {e}");
    }
}

fn build_search_window(app: &AppHandle) -> tauri::Result<()> {
    let win = WebviewWindowBuilder::new(app, "search", WebviewUrl::App("search.html".into()))
        .title("Quick search")
        .inner_size(680.0, 76.0)
        .resizable(false)
        .decorations(false)
        .always_on_top(true)
        .visible_on_all_workspaces(true)
        .skip_taskbar(true)
        .visible(false)
        .center()
        .build()?;
    let handle = app.clone();
    let w = win.clone();
    win.on_window_event(move |e| match e {
        WindowEvent::Focused(false) => {
            // Ignore the focus change that comes from showing the window.
            let recent = lock(&handle.state::<AppState>().search_shown_at).elapsed() < Duration::from_millis(250);
            if !recent {
                let _ = w.hide();
            }
        }
        WindowEvent::CloseRequested { api, .. } => {
            api.prevent_close();
            let _ = w.hide();
        }
        _ => {}
    });
    Ok(())
}

fn show_search(app: &AppHandle) {
    let Some(win) = app.get_webview_window("search") else { return };
    *lock(&app.state::<AppState>().search_shown_at) = Instant::now();
    let _ = win.center();
    let _ = win.show();
    let _ = win.set_focus();
    let _ = app.emit_to("search", "search-shown", ());
}

fn toggle_search(app: &AppHandle) {
    match app.get_webview_window("search") {
        Some(w) if w.is_visible().unwrap_or(false) && w.is_focused().unwrap_or(false) => {
            let _ = w.hide();
        }
        _ => show_search(app),
    }
}

// ---------------------------------------------------------------- menus and tray

fn build_app_menu(app: &AppHandle) -> tauri::Result<Menu<tauri::Wry>> {
    let hubs = MenuItemBuilder::with_id("hubs", "Hubs...").accelerator("CmdOrCtrl+Shift+H").build(app)?;
    let terms = MenuItemBuilder::with_id("terms", "New Terminal Window").accelerator("CmdOrCtrl+N").build(app)?;
    let terms2 = MenuItemBuilder::with_id("terms", "New Terminal Window").build(app)?;
    let quick = MenuItemBuilder::with_id("search", "Quick Search").build(app)?;
    let reload = MenuItemBuilder::with_id("reload", "Reload").accelerator("CmdOrCtrl+R").build(app)?;
    let back = MenuItemBuilder::with_id("back", "Back").accelerator("CmdOrCtrl+[").build(app)?;
    let forward = MenuItemBuilder::with_id("forward", "Forward").accelerator("CmdOrCtrl+]").build(app)?;

    let app_menu = SubmenuBuilder::new(app, APP_TITLE)
        .about(None)
        .separator()
        .item(&hubs)
        .item(&quick)
        .item(&terms)
        .separator()
        .services()
        .separator()
        .hide()
        .hide_others()
        .show_all()
        .separator()
        .quit()
        .build()?;
    let edit = SubmenuBuilder::new(app, "Edit")
        .undo()
        .redo()
        .separator()
        .cut()
        .copy()
        .paste()
        .select_all()
        .build()?;
    let view = SubmenuBuilder::new(app, "View").item(&reload).item(&back).item(&forward).separator().fullscreen().build()?;
    let window = SubmenuBuilder::new(app, "Window").item(&terms2).separator().minimize().maximize().separator().close_window().build()?;
    MenuBuilder::new(app).items(&[&app_menu, &edit, &view, &window]).build()
}

fn build_tray_menu(app: &AppHandle, cfg: &Config) -> tauri::Result<Menu<tauri::Wry>> {
    let mut b = MenuBuilder::new(app)
        .item(&MenuItem::with_id(app, "open", "Open", true, None::<&str>)?)
        .item(&MenuItem::with_id(app, "search", "Quick search", true, None::<&str>)?)
        .item(&MenuItem::with_id(app, "terms", "New terminal window", true, None::<&str>)?)
        .separator()
        .item(&MenuItem::with_id(app, "hdr", "Hubs", false, None::<&str>)?);
    for h in &cfg.hubs {
        let on = cfg.selected.as_deref() == Some(h.url.as_str());
        b = b.item(&CheckMenuItem::with_id(app, format!("hub:{}", h.url), &h.name, true, on, None::<&str>)?);
    }
    b.item(&MenuItem::with_id(app, "hubs", "Manage hubs...", true, None::<&str>)?)
        .separator()
        .item(&MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?)
        .build()
}

fn refresh_tray(app: &AppHandle) {
    let cfg = lock(&app.state::<AppState>().cfg).clone();
    if let (Some(tray), Ok(menu)) = (app.tray_by_id("main"), build_tray_menu(app, &cfg)) {
        let _ = tray.set_menu(Some(menu));
    }
}

fn main_eval(app: &AppHandle, js: &str) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.eval(js);
    }
}

fn on_menu(app: &AppHandle, id: &str) {
    match id {
        "open" => show_main(app),
        "search" => show_search(app),
        "hubs" => open_hubs_window(app),
        "terms" => open_terminal_window(app),
        "reload" => focused_eval(app, "location.reload()"),
        "back" => focused_eval(app, "history.back()"),
        "forward" => focused_eval(app, "history.forward()"),
        "quit" => app.exit(0),
        other => {
            if let Some(url) = other.strip_prefix("hub:") {
                let (app, url) = (app.clone(), url.to_string());
                tauri::async_runtime::spawn(async move {
                    match do_select(&app, &url).await {
                        Ok(h) if !h.ok => eprintln!("hub {url} is unreachable: {:?}", h.error),
                        Ok(_) => {}
                        Err(e) => eprintln!("select hub: {e}"),
                    }
                });
            }
        }
    }
}

// ---------------------------------------------------------------- hotkey

fn parse_hotkey(text: &str) -> Result<Shortcut, String> {
    let sc = Shortcut::from_str(text.trim()).map_err(|e| format!("Not a valid hotkey ({text:?}): {e}"))?;
    if sc.mods.is_empty() {
        return Err("A global hotkey needs at least one modifier key, for example Alt+Space.".into());
    }
    Ok(sc)
}

fn register_hotkey(app: &AppHandle, text: &str) -> Result<(), String> {
    let sc = parse_hotkey(text)?;
    app.global_shortcut()
        .register(sc)
        .map_err(|e| format!("Cannot use {text}: {e}. Another app may use this hotkey."))
}

fn unregister_hotkey(app: &AppHandle, text: &str) {
    if let Ok(sc) = parse_hotkey(text) {
        let _ = app.global_shortcut().unregister(sc);
    }
}

// ---------------------------------------------------------------- inbox poller

fn poll_loop(app: AppHandle, rx: mpsc::Receiver<()>) {
    let mut seen = inbox::Seen::default();
    let mut last: Option<String> = None;
    loop {
        let current = selected_url(&app.state::<AppState>());
        if current != last {
            seen.reset();
            last = current.clone();
        }
        if let Some(url) = current {
            let fetched = hub::get(&url, "/api/inbox").and_then(|r| {
                if r.status == 200 {
                    inbox::parse(&r.body)
                } else {
                    Err(format!("/api/inbox answered HTTP {}", r.status))
                }
            });
            match fetched {
                Ok(items) => {
                    if let Some((title, body)) = inbox::notification_text(&seen.diff(&items)) {
                        if let Err(e) = app.notification().builder().title(title).body(body).show() {
                            eprintln!("notification failed: {e}");
                        }
                    }
                }
                Err(e) => eprintln!("inbox poll for {url}: {e}"),
            }
        }
        match rx.recv_timeout(POLL_EVERY) {
            Err(RecvTimeoutError::Disconnected) => return,
            Ok(()) | Err(RecvTimeoutError::Timeout) => {}
        }
    }
}

// ---------------------------------------------------------------- commands

#[derive(Serialize)]
struct StateView {
    hubs: Vec<Hub>,
    selected: Option<String>,
    hotkey: String,
    hotkey_error: Option<String>,
}

fn view(state: &AppState) -> StateView {
    let c = lock(&state.cfg);
    StateView {
        hubs: c.hubs.clone(),
        selected: c.selected.clone(),
        hotkey: c.hotkey.clone(),
        hotkey_error: lock(&state.hotkey_error).clone(),
    }
}

#[tauri::command]
fn get_state(window: WebviewWindow, state: State<'_, AppState>) -> Result<StateView, String> {
    require_local(&window)?;
    Ok(view(&state))
}

#[tauri::command]
fn add_hub(window: WebviewWindow, app: AppHandle, state: State<'_, AppState>, name: String, url: String) -> Result<StateView, String> {
    require_local(&window)?;
    {
        let mut c = lock(&state.cfg);
        let mut next = c.clone();
        next.add(&name, &url)?;
        persist(&state, &next)?;
        *c = next;
    }
    refresh_tray(&app);
    let _ = lock(&state.kick).send(());
    Ok(view(&state))
}

#[tauri::command]
fn remove_hub(window: WebviewWindow, app: AppHandle, state: State<'_, AppState>, url: String) -> Result<StateView, String> {
    require_local(&window)?;
    {
        let mut c = lock(&state.cfg);
        let mut next = c.clone();
        next.remove(&url)?;
        persist(&state, &next)?;
        *c = next;
    }
    refresh_tray(&app);
    let _ = lock(&state.kick).send(());
    Ok(view(&state))
}

#[tauri::command]
fn rename_hub(window: WebviewWindow, app: AppHandle, state: State<'_, AppState>, url: String, name: String) -> Result<StateView, String> {
    require_local(&window)?;
    {
        let mut c = lock(&state.cfg);
        let mut next = c.clone();
        next.rename(&url, &name)?;
        persist(&state, &next)?;
        *c = next;
    }
    refresh_tray(&app);
    Ok(view(&state))
}

#[derive(Serialize)]
struct Selected {
    reachable: bool,
    error: Option<String>,
}

/// Select a hub and, when it answers `/healthz`, show it in the main window.
async fn do_select(app: &AppHandle, url: &str) -> Result<hub::Health, String> {
    let state = app.state::<AppState>();
    {
        let mut c = lock(&state.cfg);
        let mut next = c.clone();
        next.select(url)?;
        persist(&state, &next)?;
        *c = next;
    }
    refresh_tray(app);
    let _ = lock(&state.kick).send(());
    let target = url.to_string();
    let health = tauri::async_runtime::spawn_blocking(move || hub::healthz(&target)).await.map_err(|e| e.to_string())?;
    if health.ok {
        navigate_main(app, allow::join_path(url, "/")?)?;
    }
    Ok(health)
}

#[tauri::command]
async fn select_hub(window: WebviewWindow, app: AppHandle, url: String) -> Result<Selected, String> {
    require_local(&window)?;
    let h = do_select(&app, &url).await?;
    if h.ok && window.label() == "hubs" {
        let _ = window.hide();
    }
    Ok(Selected { reachable: h.ok, error: h.error })
}

#[tauri::command]
async fn check_hubs(window: WebviewWindow, state: State<'_, AppState>) -> Result<Vec<hub::Health>, String> {
    require_local(&window)?;
    let urls: Vec<String> = lock(&state.cfg).hubs.iter().map(|h| h.url.clone()).collect();
    tauri::async_runtime::spawn_blocking(move || {
        let threads: Vec<_> = urls.into_iter().map(|u| std::thread::spawn(move || hub::healthz(&u))).collect();
        threads.into_iter().filter_map(|t| t.join().ok()).collect()
    })
    .await
    .map_err(|e| e.to_string())
}

#[tauri::command]
fn set_hotkey(window: WebviewWindow, app: AppHandle, state: State<'_, AppState>, hotkey: String) -> Result<StateView, String> {
    require_local(&window)?;
    let hotkey = hotkey.trim().to_string();
    parse_hotkey(&hotkey)?;
    let old = lock(&state.cfg).hotkey.clone();
    unregister_hotkey(&app, &old);
    if let Err(e) = register_hotkey(&app, &hotkey) {
        if let Err(e2) = register_hotkey(&app, &old) {
            *lock(&state.hotkey_error) = Some(e2);
        }
        return Err(e);
    }
    *lock(&state.hotkey_error) = None;
    let mut c = lock(&state.cfg);
    let mut next = c.clone();
    next.hotkey = hotkey;
    persist(&state, &next)?;
    *c = next;
    drop(c);
    Ok(view(&state))
}

#[tauri::command]
async fn search(window: WebviewWindow, state: State<'_, AppState>, q: String, seq: u64) -> Result<search::Outcome, String> {
    require_local(&window)?;
    let q = q.trim().to_string();
    state.search_seq.fetch_max(seq, Ordering::SeqCst);
    let Some(url) = selected_url(&state) else {
        return Ok(search::Outcome::Error { message: "No hub selected. Open the hub list and pick one.".into() });
    };
    if state.search_seq.load(Ordering::SeqCst) > seq {
        return Ok(search::Outcome::Stale);
    }
    let query = q.clone();
    let reply = tauri::async_runtime::spawn_blocking(move || hub::get(&url, &search::api_path(&query)))
        .await
        .map_err(|e| e.to_string())?;
    if state.search_seq.load(Ordering::SeqCst) > seq {
        return Ok(search::Outcome::Stale);
    }
    Ok(match reply {
        Ok(r) => search::decide(r.status, &r.body, &q),
        Err(message) => search::Outcome::Error { message },
    })
}

#[tauri::command]
fn resize_search(window: WebviewWindow, app: AppHandle, height: f64) -> Result<(), String> {
    require_local(&window)?;
    if let Some(w) = app.get_webview_window("search") {
        w.set_size(LogicalSize::new(680.0, height.clamp(60.0, 560.0))).map_err(|e| e.to_string())?;
    }
    Ok(())
}

#[tauri::command]
fn hide_search(window: WebviewWindow, app: AppHandle) -> Result<(), String> {
    require_local(&window)?;
    if let Some(w) = app.get_webview_window("search") {
        let _ = w.hide();
    }
    Ok(())
}

#[tauri::command]
fn show_hubs(window: WebviewWindow, app: AppHandle) -> Result<(), String> {
    require_local(&window)?;
    if let Some(w) = app.get_webview_window("search") {
        let _ = w.hide();
    }
    open_hubs_window(&app);
    Ok(())
}

/// Open a hub path (`/sessions/123`) in the main window.
#[tauri::command]
fn open_in_main(window: WebviewWindow, app: AppHandle, state: State<'_, AppState>, path: String) -> Result<(), String> {
    require_local(&window)?;
    let base = selected_url(&state).ok_or("No hub selected.")?;
    navigate_main(&app, allow::join_path(&base, &path)?)?;
    if let Some(w) = app.get_webview_window("search") {
        let _ = w.hide();
    }
    Ok(())
}

/// Start a new terminal session that continues an old one, then open it in the main window.
#[tauri::command]
async fn continue_session(window: WebviewWindow, app: AppHandle, state: State<'_, AppState>, continue_url: String) -> Result<(), String> {
    require_local(&window)?;
    let base = selected_url(&state).ok_or("No hub selected.")?;
    allow::join_path(&base, &continue_url)?;
    let b = base.clone();
    let reply = tauri::async_runtime::spawn_blocking(move || hub::post_json(&b, &continue_url, r#"{"mode":"auto"}"#))
        .await
        .map_err(|e| e.to_string())??;
    let link = search::parse_continue(reply.status, &reply.body)?;
    navigate_main(&app, allow::join_path(&base, &link)?)?;
    if let Some(w) = app.get_webview_window("search") {
        let _ = w.hide();
    }
    Ok(())
}

// ---------------------------------------------------------------- start

fn main() {
    let app = tauri::Builder::default()
        .plugin(
            tauri_plugin_window_state::Builder::default()
                .with_denylist(&["search", "hubs"])
                .with_state_flags(StateFlags::SIZE | StateFlags::POSITION | StateFlags::MAXIMIZED | StateFlags::FULLSCREEN)
                .build(),
        )
        .plugin(tauri_plugin_notification::init())
        .plugin(
            tauri_plugin_global_shortcut::Builder::new()
                .with_handler(|app, _shortcut, event| {
                    if event.state() == ShortcutState::Pressed {
                        toggle_search(app);
                    }
                })
                .build(),
        )
        .invoke_handler(tauri::generate_handler![
            get_state,
            add_hub,
            remove_hub,
            rename_hub,
            select_hub,
            check_hubs,
            set_hotkey,
            search,
            resize_search,
            hide_search,
            show_hubs,
            open_in_main,
            continue_session,
        ])
        .setup(|app| {
            let handle = app.handle().clone();
            let cfg_path = app.path().app_config_dir()?.join("hubs.json");
            let cfg = Config::load(&cfg_path);
            let (tx, rx) = mpsc::channel();
            let hotkey = cfg.hotkey.clone();
            let selected = cfg.selected_hub().map(|h| h.url.clone());
            app.manage(AppState {
                cfg: Mutex::new(cfg.clone()),
                cfg_path,
                search_seq: AtomicU64::new(0),
                hotkey_error: Mutex::new(None),
                kick: Mutex::new(tx),
                search_shown_at: Mutex::new(Instant::now()),
            });

            app.set_menu(build_app_menu(&handle)?)?;
            app.on_menu_event(|app, event| on_menu(app, event.id().as_ref()));

            build_search_window(&handle)?;

            let start = match selected.as_deref().map(Url::parse) {
                Some(Ok(u)) => WebviewUrl::External(u),
                _ => WebviewUrl::App("hubs.html".into()),
            };
            build_main(&handle, start)?;
            if let Some(url) = selected {
                // A hub that does not answer would leave a blank window. Show the hub list instead.
                let h = handle.clone();
                std::thread::spawn(move || {
                    if !hub::healthz(&url).ok {
                        if let Some(w) = h.get_webview_window("main") {
                            if let Ok(u) = Url::parse(LOCAL_HUBS_PAGE) {
                                let _ = w.navigate(u);
                            }
                        }
                    }
                });
            }

            TrayIconBuilder::with_id("main")
                .icon(Image::from_bytes(include_bytes!("../icons/tray.png"))?)
                .icon_as_template(true)
                .tooltip(APP_TITLE)
                .menu(&build_tray_menu(&handle, &cfg)?)
                .show_menu_on_left_click(true)
                .build(app)?;

            if let Err(e) = register_hotkey(&handle, &hotkey) {
                eprintln!("hotkey: {e}");
                *lock(&handle.state::<AppState>().hotkey_error) = Some(e);
            }

            let poller = handle.clone();
            std::thread::spawn(move || poll_loop(poller, rx));
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("cannot build the app");

    app.run(|_app, _event| {
        // macOS only: a click on the Dock icon brings the hidden main window back.
        // Linux has no Dock; the tray icon does this there.
        #[cfg(target_os = "macos")]
        if let tauri::RunEvent::Reopen { .. } = _event {
            show_main(_app);
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hotkey_parsing() {
        for ok in ["Alt+Space", "Control+Shift+K", "Command+Alt+Space", "Command+Shift+F5", "Alt+1"] {
            assert!(parse_hotkey(ok).is_ok(), "{ok} should parse");
        }
        for bad in ["Space", "K", "", "Alt+", "Alt+Nonsense", "Hyper+Space"] {
            assert!(parse_hotkey(bad).is_err(), "{bad} should be refused");
        }
    }

    #[test]
    fn main_window_navigation_rule() {
        let u = |s: &str| Url::parse(s).unwrap();
        assert!(navigation_allowed(&u("http://127.0.0.1:8787/inbox")));
        assert!(navigation_allowed(&u("https://studio.tail1.ts.net/sessions/1")));
        assert!(navigation_allowed(&u("tauri://localhost/hubs.html")));
        assert!(navigation_allowed(&u("about:blank")));
        assert!(!navigation_allowed(&u("file:///etc/passwd")));
        assert!(!navigation_allowed(&u("javascript:alert(1)")));
    }
}
