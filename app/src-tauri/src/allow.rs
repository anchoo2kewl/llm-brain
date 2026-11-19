//! Where the app may talk to. The hub must stay on loopback or the tailnet, so every request URL,
//! every saved hub and every page the main window loads goes through this allow-list.

use url::{Host, Url};

pub const DENY_HINT: &str =
    "The hub must stay on loopback or the tailnet: use 127.0.0.1, a 100.64.0.0/10 address or a *.ts.net name.";

/// True for hosts the hub may use: loopback, Tailscale CGNAT, Tailscale IPv6 ULA, and `*.ts.net`.
pub fn host_allowed(host: &Host<&str>) -> bool {
    match host {
        Host::Domain(d) => {
            let d = d.trim_end_matches('.').to_ascii_lowercase();
            d.len() > ".ts.net".len() && d.ends_with(".ts.net") && !d.starts_with('.')
        }
        Host::Ipv4(ip) => {
            let o = ip.octets();
            ip.is_loopback() || (o[0] == 100 && (o[1] & 0xC0) == 64)
        }
        Host::Ipv6(ip) => {
            let s = ip.segments();
            ip.is_loopback() || (s[0] == 0xfd7a && s[1] == 0x115c && s[2] == 0xa1e0)
        }
    }
}

/// True when the URL is http or https and its host is allowed.
pub fn url_allowed(url: &Url) -> bool {
    matches!(url.scheme(), "http" | "https") && url.host().is_some_and(|h| host_allowed(&h))
}

/// Check a hub URL typed by a person. Returns the normalised origin, for example `http://127.0.0.1:8787`.
pub fn validate_hub_url(input: &str) -> Result<String, String> {
    let input = input.trim();
    if !input.contains("://") {
        return Err("Enter a full URL, for example http://127.0.0.1:8787".into());
    }
    let url = Url::parse(input).map_err(|e| format!("Not a valid URL: {e}"))?;
    if !matches!(url.scheme(), "http" | "https") {
        return Err("Only http and https hub URLs are allowed.".into());
    }
    if !url.username().is_empty() || url.password().is_some() {
        return Err("A hub URL must not contain a user name or password.".into());
    }
    match url.host() {
        None => Err("The URL has no host.".into()),
        Some(h) if host_allowed(&h) => Ok(url.origin().ascii_serialization()),
        Some(h) => Err(format!("Host {h} is not allowed. {DENY_HINT}")),
    }
}

/// Join a hub base URL with a path the hub gave us (`/sessions/1`). Refuses anything that is not a plain
/// absolute path, so a hub reply cannot send the app to another host.
pub fn join_path(base: &str, path: &str) -> Result<Url, String> {
    if !path.starts_with('/') || path.starts_with("//") || path.contains('\\') || path.chars().any(char::is_control) {
        return Err(format!("Refused link {path:?}: only paths on the hub are allowed."));
    }
    let base = Url::parse(base).map_err(|e| format!("Bad hub URL: {e}"))?;
    if !url_allowed(&base) {
        return Err(format!("Hub host is not allowed. {DENY_HINT}"));
    }
    let joined = base.join(path).map_err(|e| format!("Bad link {path:?}: {e}"))?;
    if joined.origin() != base.origin() {
        return Err(format!("Refused link {path:?}: it leaves the hub."));
    }
    Ok(joined)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ok(s: &str) -> String {
        validate_hub_url(s).unwrap_or_else(|e| panic!("{s} refused: {e}"))
    }

    #[test]
    fn allows_loopback_and_tailnet() {
        assert_eq!(ok("http://127.0.0.1:8787"), "http://127.0.0.1:8787");
        assert_eq!(ok("http://127.0.0.1:8787/some/path?x=1#f"), "http://127.0.0.1:8787");
        assert_eq!(ok("https://mac-studio.tail1234.ts.net"), "https://mac-studio.tail1234.ts.net");
        assert_eq!(ok("http://Mac.Tail1234.TS.NET:8787/"), "http://mac.tail1234.ts.net:8787");
        assert_eq!(ok("http://100.64.0.1:8787"), "http://100.64.0.1:8787");
        assert_eq!(ok("http://100.127.255.254:8787"), "http://100.127.255.254:8787");
        assert_eq!(ok("http://[::1]:8787"), "http://[::1]:8787");
        assert_eq!(ok("http://[fd7a:115c:a1e0::1]:8787"), "http://[fd7a:115c:a1e0::1]:8787");
        assert_eq!(ok("http://127.1.2.3:1"), "http://127.1.2.3:1");
    }

    #[test]
    fn refuses_everything_else() {
        for bad in [
            "http://example.com",
            "https://example.com:8787",
            "http://192.168.1.10:8787",
            "http://10.0.0.5",
            "http://100.63.255.255",
            "http://100.128.0.1",
            "http://8.8.8.8",
            "http://127.0.0.1.evil.com",
            "http://evil.com/.ts.net",
            "http://evilts.net",
            "http://ts.net",
            "http://.ts.net",
            "http://foo.ts.net.evil.com",
            "http://localhost:8787",
            "http://[2001:db8::1]",
            "http://user:pw@127.0.0.1:8787",
            "http://127.0.0.1@evil.com",
            "ftp://127.0.0.1",
            "file:///etc/passwd",
            "127.0.0.1:8787",
            "",
        ] {
            assert!(validate_hub_url(bad).is_err(), "{bad} should be refused");
        }
    }

    #[test]
    fn refusal_message_is_clear() {
        let e = validate_hub_url("https://example.com").unwrap_err();
        assert!(e.contains("example.com") && e.contains("tailnet"), "{e}");
    }

    #[test]
    fn url_allowed_checks_scheme_and_host() {
        assert!(url_allowed(&Url::parse("http://127.0.0.1:1/x").unwrap()));
        assert!(!url_allowed(&Url::parse("https://github.com/x").unwrap()));
        assert!(!url_allowed(&Url::parse("tauri://localhost/hubs.html").unwrap()));
    }

    #[test]
    fn join_accepts_only_hub_paths() {
        let u = join_path("http://127.0.0.1:8787", "/sessions/123").unwrap();
        assert_eq!(u.as_str(), "http://127.0.0.1:8787/sessions/123");
        let u = join_path("http://127.0.0.1:8787", "/search?q=a%20b").unwrap();
        assert_eq!(u.as_str(), "http://127.0.0.1:8787/search?q=a%20b");
        for bad in ["//evil.com/x", "https://evil.com/", "sessions/1", "/a\\b", "/a\nb", ""] {
            assert!(join_path("http://127.0.0.1:8787", bad).is_err(), "{bad:?} should be refused");
        }
        assert!(join_path("https://example.com", "/x").is_err());
    }
}
