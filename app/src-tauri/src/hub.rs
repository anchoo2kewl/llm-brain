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
