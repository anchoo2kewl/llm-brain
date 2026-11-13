//! Quick search: the `GET /api/search` contract, parsing, and the fallback decision.

use serde::{Deserialize, Deserializer, Serialize};
use url::form_urlencoded;

pub const LIMIT: u32 = 20;

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

#[derive(Serialize, Deserialize, Debug, Clone, PartialEq, Default)]
#[serde(default)]
pub struct Session {
    pub id: i64,
    pub node: String,
    pub host: String,
    /// "claude" or "codex"
    pub provider: String,
    pub uuid: String,
    pub title: String,
    pub cwd: String,
    pub branch: String,
    /// RFC 3339
    pub last_at: String,
    pub matches: i64,
    /// Plain text.
    pub snippet: String,
    /// Path on the hub, for example `/sessions/123`.
    pub link: String,
    /// Path to POST `{"mode":"auto"}` to.
    pub continue_url: String,
}

#[derive(Serialize, Deserialize, Debug, Clone, PartialEq, Default)]
#[serde(default)]
pub struct SearchResponse {
    pub took_ms: f64,
    pub total: i64,
    /// Node names that did not answer.
    #[serde(deserialize_with = "null_default")]
    pub partial: Vec<String>,
    #[serde(deserialize_with = "null_default")]
    pub sessions: Vec<Session>,
}

/// What the quick-search window gets back.
#[derive(Serialize, Debug, PartialEq)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum Outcome {
    Results(SearchResponse),
    /// The hub has no `/api/search` yet. Open this path in the main window instead.
    Fallback { path: String },
    /// A newer keystroke replaced this request.
    Stale,
    Error { message: String },
}

/// `/api/search?q=<q>&scope=cluster&limit=20`
pub fn api_path(q: &str) -> String {
    let query = form_urlencoded::Serializer::new(String::new())
        .append_pair("q", q)
        .append_pair("scope", "cluster")
        .append_pair("limit", &LIMIT.to_string())
        .finish();
    format!("/api/search?{query}")
}

/// `/search?q=<q>`: the hub's own search page.
pub fn fallback_path(q: &str) -> String {
    format!("/search?{}", form_urlencoded::Serializer::new(String::new()).append_pair("q", q).finish())
}

/// Turn the HTTP reply into an outcome. 200 is parsed. 404 means the hub is older than the API, so the
/// caller falls back to the search page. Anything else is an error shown in the window.
pub fn decide(status: u16, body: &str, q: &str) -> Outcome {
    match status {
        200 => match serde_json::from_str::<SearchResponse>(body) {
            Ok(r) => Outcome::Results(r),
            Err(e) => Outcome::Error { message: format!("Unexpected search reply: {e}") },
        },
        404 => Outcome::Fallback { path: fallback_path(q) },
        s => Outcome::Error { message: format!("Search failed: the hub answered {s}{}", error_suffix(body)) },
    }
}

fn error_suffix(body: &str) -> String {
    match serde_json::from_str::<serde_json::Value>(body).ok().and_then(|v| v.get("error")?.as_str().map(str::to_owned)) {
        Some(e) if !e.is_empty() => format!(": {e}"),
        _ => String::new(),
    }
}

/// Read `{"link": "..."}` from the reply to a continue request.
pub fn parse_continue(status: u16, body: &str) -> Result<String, String> {
    if !(200..300).contains(&status) {
        return Err(format!("Continue failed: the hub answered {status}{}", error_suffix(body)));
    }
    #[derive(Deserialize)]
    struct Reply {
        #[serde(default)]
        link: String,
    }
    let r: Reply = serde_json::from_str(body).map_err(|e| format!("Unexpected continue reply: {e}"))?;
    if r.link.is_empty() {
        return Err("The hub did not return a link for the new session.".into());
    }
    Ok(r.link)
}

#[cfg(test)]
mod tests {
    use super::*;

    const FULL: &str = r#"{"took_ms": 12.5, "total": 2, "partial": ["laptop"], "sessions": [
        {"id": 123, "node": "studio", "host": "studio", "provider": "claude", "uuid": "u-1",
         "title": "Fix the build", "cwd": "/Users/x/projects/app", "branch": "main",
         "last_at": "2026-11-13T09:00:00Z", "matches": 3, "snippet": "the build failed because",
         "link": "/sessions/123", "continue_url": "/sessions/123/continue"},
        {"id": 7, "node": "n", "host": "h", "provider": "codex", "uuid": "u-2", "title": "", "cwd": "",
         "branch": "", "last_at": "2026-11-13T08:00:00Z", "matches": 1, "snippet": "",
         "link": "/sessions/7", "continue_url": "/sessions/7/continue"}]}"#;

    #[test]
    fn parses_the_contract() {
        match decide(200, FULL, "build") {
            Outcome::Results(r) => {
                assert_eq!(r.total, 2);
                assert_eq!(r.partial, ["laptop"]);
                assert_eq!(r.sessions.len(), 2);
                let s = &r.sessions[0];
                assert_eq!((s.id, s.provider.as_str(), s.link.as_str()), (123, "claude", "/sessions/123"));
                assert_eq!(s.continue_url, "/sessions/123/continue");
                assert_eq!(s.cwd, "/Users/x/projects/app");
            }
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn null_lists_and_integer_took_ms() {
        match decide(200, r#"{"took_ms": 3, "total": 0, "partial": null, "sessions": null}"#, "q") {
            Outcome::Results(r) => assert!(r.sessions.is_empty() && r.partial.is_empty() && r.took_ms == 3.0),
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn not_found_falls_back_to_the_search_page() {
        assert_eq!(decide(404, "404 page not found", "a b&c"), Outcome::Fallback { path: "/search?q=a+b%26c".into() });
    }

    #[test]
    fn other_statuses_and_garbage_are_errors() {
        for (status, body) in [(500, ""), (403, "forbidden"), (200, "<html>"), (503, r#"{"error":"index is rebuilding"}"#)] {
            match decide(status, body, "q") {
                Outcome::Error { message } => assert!(!message.is_empty()),
                other => panic!("{status}: {other:?}"),
            }
        }
        match decide(503, r#"{"error":"index is rebuilding"}"#, "q") {
            Outcome::Error { message } => assert!(message.contains("index is rebuilding"), "{message}"),
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn api_path_is_exact_and_encoded() {
        assert_eq!(api_path("hello"), "/api/search?q=hello&scope=cluster&limit=20");
        assert_eq!(api_path("a b&c=d/é"), "/api/search?q=a+b%26c%3Dd%2F%C3%A9&scope=cluster&limit=20");
    }

    #[test]
    fn outcome_serialises_with_a_kind_tag() {
        let v = serde_json::to_value(decide(404, "", "x")).unwrap();
        assert_eq!(v["kind"], "fallback");
        assert_eq!(v["path"], "/search?q=x");
        let v = serde_json::to_value(decide(200, FULL, "x")).unwrap();
        assert_eq!(v["kind"], "results");
        assert_eq!(v["sessions"][0]["continue_url"], "/sessions/123/continue");
        assert_eq!(serde_json::to_value(Outcome::Stale).unwrap()["kind"], "stale");
    }

    #[test]
    fn continue_reply() {
        assert_eq!(parse_continue(200, r#"{"link":"/terminals/9"}"#).unwrap(), "/terminals/9");
        assert!(parse_continue(200, "{}").is_err());
        assert!(parse_continue(200, "nope").is_err());
        let e = parse_continue(403, r#"{"error":"forbidden"}"#).unwrap_err();
        assert!(e.contains("403") && e.contains("forbidden"), "{e}");
    }
}
