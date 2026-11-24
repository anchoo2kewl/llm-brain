//! HTTP to a hub. Small blocking client, 5 second timeout, no proxy, no redirects.
//! Every URL passes the allow-list first, so a bad config or a bad hub reply cannot reach another host.

use crate::allow;
use serde::Serialize;
use std::sync::OnceLock;
use std::time::{Duration, Instant};

pub const TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Debug)]
pub struct Reply {
    pub status: u16,
    pub body: String,
}

fn agent() -> &'static ureq::Agent {
    static AGENT: OnceLock<ureq::Agent> = OnceLock::new();
    AGENT.get_or_init(|| {
        ureq::Agent::config_builder()
            .timeout_global(Some(TIMEOUT))
            .http_status_as_error(false)
            .max_redirects(0)
            .proxy(None)
            .build()
            .into()
    })
}

fn finish(res: Result<ureq::http::Response<ureq::Body>, ureq::Error>, what: &str) -> Result<Reply, String> {
    let resp = res.map_err(|e| describe(&e, what))?;
    let status = resp.status().as_u16();
    let body = resp.into_body().read_to_string().map_err(|e| describe(&e, what))?;
    Ok(Reply { status, body })
}

fn describe(e: &ureq::Error, what: &str) -> String {
    match e {
        ureq::Error::Timeout(_) => format!("{what}: no answer within {} seconds", TIMEOUT.as_secs()),
        other => format!("{what}: {other}"),
    }
}

/// GET `path` on the hub at `base`.
pub fn get(base: &str, path: &str) -> Result<Reply, String> {
    let url = allow::join_path(base, path)?;
    finish(agent().get(url.as_str()).header("Accept", "application/json").call(), "Hub unreachable")
}

/// POST a JSON body to `path` on the hub at `base`. No Origin header is sent, which the hub accepts.
pub fn post_json(base: &str, path: &str, body: &str) -> Result<Reply, String> {
    let url = allow::join_path(base, path)?;
    finish(
        agent().post(url.as_str()).header("Content-Type", "application/json").header("Accept", "application/json").send(body),
        "Hub unreachable",
    )
}

#[derive(Serialize, Clone, Debug, PartialEq)]
pub struct Health {
    pub url: String,
    pub ok: bool,
    pub ms: u64,
    pub error: Option<String>,
}

/// `GET /healthz` must answer `ok`.
pub fn healthz(base: &str) -> Health {
    let started = Instant::now();
    let result = get(base, "/healthz").and_then(|r| {
        if r.status == 200 && r.body.trim() == "ok" {
            Ok(())
        } else {
            Err(format!("Unexpected /healthz reply (HTTP {})", r.status))
        }
    });
    let ms = started.elapsed().as_millis() as u64;
    match result {
        Ok(()) => Health { url: base.to_string(), ok: true, ms, error: None },
        Err(e) => Health { url: base.to_string(), ok: false, ms, error: Some(e) },
    }
}

/// A hub's own version and its update state. Hub and app versions are separate and may differ.
#[derive(Serialize, Clone, Debug, Default, PartialEq)]
pub struct Versions {
    /// The version the hub runs (`GET /api/hub-update/health`).
    pub running: Option<String>,
    /// The version installed on disk (`GET /api/hub-update`). Newer than `running`: the hub waits for a restart.
    pub installed: Option<String>,
    /// A newer release the hub's updater found.
    pub available: Option<String>,
    /// For Settings: "Hub 0.1.11", "Hub 0.1.11 · 0.1.12 installed, restart pending", or "Hub version unknown".
    pub note: String,
}

fn number(v: &str) -> Option<[u64; 3]> {
    let mut out = [0; 3];
    let mut parts = v.trim().trim_start_matches('v').split('.');
    for slot in &mut out {
        *slot = parts.next()?.parse().ok()?;
    }
    parts.next().is_none().then_some(out)
}

/// True when `a` is a higher version than `b`. Unreadable versions are never newer.
pub fn newer(a: &str, b: &str) -> bool {
    matches!((number(a), number(b)), (Some(x), Some(y)) if x > y)
}

fn json_field(reply: Result<Reply, String>, key: &str) -> Option<serde_json::Value> {
    let r = reply.ok().filter(|r| r.status == 200)?;
    serde_json::from_str::<serde_json::Value>(&r.body).ok()?.get(key).cloned()
}

/// Build the versions from the two replies. Any missing field (an older hub, no updater) is simply left out.
pub fn versions_from(health: Result<Reply, String>, status: Result<Reply, String>) -> Versions {
    let text = |v: Option<serde_json::Value>| v.and_then(|v| v.as_str().map(str::to_owned)).filter(|s| number(s).is_some());
    let running = text(json_field(health, "version"));
    let status = status.ok().filter(|r| r.status == 200).and_then(|r| serde_json::from_str::<serde_json::Value>(&r.body).ok());
    let field = |k: &str| text(status.as_ref().and_then(|s| s.get(k).cloned()));
    let installed = field("version").filter(|v| running.as_deref().is_some_and(|r| newer(v, r)));
    let base = installed.as_deref().or(running.as_deref()).unwrap_or("");
    let available = field("latest").filter(|v| newer(v, base));
    let note = match (&running, &installed, &available) {
        (None, _, _) => "Hub version unknown".to_owned(),
        (Some(r), Some(i), _) => format!("Hub {r} · {i} installed, restart pending"),
        (Some(r), None, Some(a)) => format!("Hub {r} · {a} available"),
        (Some(r), None, None) => format!("Hub {r}"),
    };
    Versions { running, installed, available, note }
}

/// Read the hub's versions. The status route runs the hub's updater, so call this only when Settings opens.
pub fn versions(base: &str) -> Versions {
    versions_from(get(base, "/api/hub-update/health"), get(base, "/api/hub-update"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};
    use std::net::TcpListener;
    use std::thread;

    /// Serve one canned HTTP reply on a loopback port. Returns the base URL.
    fn serve_once(status_line: &'static str, body: &'static str) -> String {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = l.local_addr().unwrap().port();
        thread::spawn(move || {
            if let Ok((mut s, _)) = l.accept() {
                let mut buf = [0u8; 2048];
                let _ = s.read(&mut buf);
                let r = format!("HTTP/1.1 {status_line}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len());
                let _ = s.write_all(r.as_bytes());
            }
        });
        format!("http://127.0.0.1:{port}")
    }

    fn ok(body: &str) -> Result<Reply, String> {
        Ok(Reply { status: 200, body: body.into() })
    }

    #[test]
    fn version_order() {
        assert!(newer("0.1.12", "0.1.11") && newer("0.2.0", "0.1.99") && newer("1.0.0", "0.9.9"));
        assert!(!newer("0.1.11", "0.1.11") && !newer("0.1.9", "0.1.10") && !newer("dev", "0.1.0") && !newer("0.1", "0.0.9"));
    }

    #[test]
    fn hub_version_notes() {
        let health = || ok(r#"{"version":"0.1.11","ready":true}"#);
        assert_eq!(versions_from(health(), ok(r#"{"state":"up_to_date","version":"0.1.11"}"#)).note, "Hub 0.1.11");
        let v = versions_from(health(), ok(r#"{"state":"up_to_date","version":"0.1.12","latest":"0.1.12"}"#));
        assert_eq!((v.note.as_str(), v.installed.as_deref(), v.available), ("Hub 0.1.11 · 0.1.12 installed, restart pending", Some("0.1.12"), None));
        let v = versions_from(health(), ok(r#"{"state":"available","version":"0.1.11","latest":"0.1.13"}"#));
        assert_eq!(v.note, "Hub 0.1.11 · 0.1.13 available");
        // An older hub: no update routes. A hub without the updater: 502.
        assert_eq!(versions_from(Ok(Reply { status: 404, body: "".into() }), Err("x".into())).note, "Hub version unknown");
        assert_eq!(versions_from(health(), Ok(Reply { status: 502, body: "{}".into() })).note, "Hub 0.1.11");
        assert_eq!(versions_from(ok(r#"{"version":"dev"}"#), ok("{}")).note, "Hub version unknown");
    }

    #[test]
    fn healthz_ok_and_not_ok() {
        assert!(healthz(&serve_once("200 OK", "ok\n")).ok);
        let h = healthz(&serve_once("200 OK", "<html>"));
        assert!(!h.ok && h.error.is_some());
        assert!(!healthz(&serve_once("500 Internal Server Error", "ok")).ok);
    }

    #[test]
    fn refused_connection_is_a_clear_error() {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = l.local_addr().unwrap().port();
        drop(l);
        let h = healthz(&format!("http://127.0.0.1:{port}"));
        assert!(!h.ok);
        assert!(h.error.unwrap().contains("Hub unreachable"));
    }

    #[test]
    fn disallowed_host_never_connects() {
        let e = get("https://example.com", "/healthz").unwrap_err();
        assert!(e.contains("not allowed"), "{e}");
        assert!(!healthz("http://192.168.1.1").ok);
    }

    #[test]
    fn redirects_are_not_followed() {
        let base = serve_once("302 Found", "");
        let r = get(&base, "/x").unwrap();
        assert_eq!(r.status, 302);
    }

    #[test]
    fn get_returns_non_200_status_without_error() {
        let r = get(&serve_once("404 Not Found", "nope"), "/api/search?q=x").unwrap();
        assert_eq!((r.status, r.body.as_str()), (404, "nope"));
    }
}
