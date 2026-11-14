//! The hub inbox (`GET /api/inbox`) and the "which items are new" decision.
//!
//! The hub encodes its Go `Inbox` struct without JSON tags, so field names are capitalised
//! (`Items`, `Key`, `Title`, ...). Lower-case names are accepted too, in case the hub adds tags later.

use serde::Deserialize;
use std::collections::HashSet;

#[derive(Deserialize, Debug, Clone, PartialEq, Default)]
#[serde(default)]
pub struct InboxItem {
    #[serde(rename = "Key", alias = "key")]
    pub key: String,
    /// waiting | blocked | failed | review
    #[serde(rename = "State", alias = "state")]
    pub state: String,
    #[serde(rename = "Kind", alias = "kind")]
    pub kind: String,
    #[serde(rename = "Title", alias = "title")]
    pub title: String,
    #[serde(rename = "Detail", alias = "detail")]
    pub detail: String,
    #[serde(rename = "Where", alias = "where")]
    pub where_: String,
    #[serde(rename = "Link", alias = "link")]
    pub link: String,
    /// When it happened. A newer value on the same key is a new event.
    #[serde(rename = "At", alias = "at")]
    pub at: String,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Inbox {
    #[serde(rename = "Items", alias = "items")]
    items: Option<Vec<InboxItem>>,
}

pub fn parse(body: &str) -> Result<Vec<InboxItem>, String> {
    let inbox: Inbox = serde_json::from_str(body).map_err(|e| format!("Unexpected inbox reply: {e}"))?;
    Ok(inbox.items.unwrap_or_default())
}

/// Remembers which items the app has seen, in memory only.
#[derive(Default)]
pub struct Seen {
    ids: HashSet<String>,
    primed: bool,
}

impl Seen {
    pub fn reset(&mut self) {
        self.ids.clear();
        self.primed = false;
    }

    /// Return the items not seen before. The first call after a reset only records what is there and
    /// returns nothing, so opening the app does not announce the whole inbox. An item is new when its
    /// key is new or when its time changed, because the hub brings an item back when something newer
    /// happens there. Items that left the inbox are forgotten, so they notify again if they return.
    pub fn diff(&mut self, items: &[InboxItem]) -> Vec<InboxItem> {
        let now: HashSet<String> =
            items.iter().filter(|i| !i.key.is_empty()).map(|i| format!("{}\u{1}{}", i.key, i.at)).collect();
        let fresh = if self.primed {
            items
                .iter()
                .filter(|i| !i.key.is_empty() && !self.ids.contains(&format!("{}\u{1}{}", i.key, i.at)))
                .cloned()
                .collect()
        } else {
            Vec::new()
        };
        self.ids = now;
        self.primed = true;
        fresh
    }
}

fn state_word(state: &str) -> &str {
    match state {
        "waiting" => "Waiting for you",
        "blocked" => "Blocked",
        "failed" => "Failed",
        "review" => "Needs review",
        "" => "Inbox",
        other => other,
    }
}

/// Notification title and body for the new items. Many items collapse into one summary.
pub fn notification_text(new: &[InboxItem]) -> Option<(String, String)> {
    match new {
        [] => None,
        [one] => {
            let title = if one.title.is_empty() { "Hub inbox".to_string() } else { one.title.clone() };
            let mut head = state_word(&one.state).to_string();
            if !one.where_.is_empty() {
                head = format!("{head} · {}", one.where_);
            }
            let body = if one.detail.is_empty() { head } else { format!("{head}\n{}", one.detail) };
            Some((title, body))
        }
        many => {
            let list = many.iter().take(3).map(|i| i.title.as_str()).collect::<Vec<_>>().join(", ");
            let more = if many.len() > 3 { ", ..." } else { "" };
            Some((format!("{} new inbox items", many.len()), format!("{list}{more}")))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn item(key: &str, at: &str) -> InboxItem {
        InboxItem { key: key.into(), at: at.into(), title: format!("t-{key}"), ..Default::default() }
    }

    #[test]
    fn parses_the_real_hub_shape() {
        let body = r#"{"Items":[{"Key":"s:12","State":"waiting","Kind":"session","Title":"Fix bug",
            "Detail":"Done, please look","Where":"studio · repo","Link":"/sessions/12","At":"2026-11-13T10:00:00Z",
            "Ext":false,"ReplyTo":"/sessions/12/send","TermID":0,"Provider":"claude","Close":"","Restart":""}],
            "Counts":{"waiting":1},"Total":1,"Hash":"abc"}"#;
        let items = parse(body).unwrap();
        assert_eq!(items.len(), 1);
        assert_eq!(items[0].key, "s:12");
        assert_eq!(items[0].where_, "studio · repo");
        assert_eq!(items[0].state, "waiting");
    }

    #[test]
    fn parses_null_missing_and_lowercase() {
        assert!(parse(r#"{"Items":null,"Counts":{},"Total":0,"Hash":""}"#).unwrap().is_empty());
        assert!(parse("{}").unwrap().is_empty());
        let items = parse(r#"{"items":[{"key":"a","title":"x","at":"1"}]}"#).unwrap();
        assert_eq!((items[0].key.as_str(), items[0].title.as_str()), ("a", "x"));
        assert!(parse("not json").is_err());
        assert!(parse("<html>").is_err());
    }

    #[test]
    fn first_poll_primes_without_notifying() {
        let mut s = Seen::default();
        assert!(s.diff(&[item("a", "1"), item("b", "1")]).is_empty());
        assert!(s.diff(&[item("a", "1"), item("b", "1")]).is_empty());
    }

    #[test]
    fn only_new_items_are_returned() {
        let mut s = Seen::default();
        s.diff(&[item("a", "1")]);
        let new = s.diff(&[item("a", "1"), item("b", "2")]);
        assert_eq!(new.iter().map(|i| i.key.as_str()).collect::<Vec<_>>(), ["b"]);
        assert!(s.diff(&[item("a", "1"), item("b", "2")]).is_empty(), "b is not announced twice");
    }

    #[test]
    fn newer_time_on_same_key_is_new() {
        let mut s = Seen::default();
        s.diff(&[item("a", "1")]);
        let new = s.diff(&[item("a", "2")]);
        assert_eq!(new.len(), 1);
    }

    #[test]
    fn item_that_left_and_came_back_notifies_again() {
        let mut s = Seen::default();
        s.diff(&[item("a", "1")]);
        assert!(s.diff(&[]).is_empty());
        assert_eq!(s.diff(&[item("a", "1")]).len(), 1);
    }

    #[test]
    fn reset_primes_again_and_empty_keys_are_ignored() {
        let mut s = Seen::default();
        s.diff(&[item("a", "1")]);
        s.reset();
        assert!(s.diff(&[item("b", "1")]).is_empty(), "after a hub switch nothing is announced");
        assert!(s.diff(&[item("", "1")]).is_empty());
    }

    #[test]
    fn notification_text_variants() {
        assert!(notification_text(&[]).is_none());
        let mut one = item("a", "1");
        one.state = "failed".into();
        one.where_ = "studio · app".into();
        one.detail = "exit 1".into();
        let (t, b) = notification_text(&[one]).unwrap();
        assert_eq!(t, "t-a");
        assert_eq!(b, "Failed · studio · app\nexit 1");
        let many: Vec<_> = (0..5).map(|i| item(&i.to_string(), "1")).collect();
        let (t, b) = notification_text(&many).unwrap();
        assert_eq!(t, "5 new inbox items");
        assert_eq!(b, "t-0, t-1, t-2, ...");
    }
}
