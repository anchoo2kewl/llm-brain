# Jev

`bin/jev` asks Jev (TypeSafe systemOne) typed questions from any agent on any Mac.

## Commands

| Command | What it does |
| --- | --- |
| `bin/jev decide DECISION.json` | Yes/no questions. Prints p(yes) per question and writes `DECISION.answers.json`. |
| `bin/jev ask < REQUEST.json` | A raw systemOne request (`{state, questions}`). Prints the response. |
| `bin/jev check` | Says whether a key is available. Never prints it. |

## Decision files

```json
{
  "title": "Merge the cache fix?",
  "state": {"pr": "acme/api#88", "ci": "green", "open_threads": 0},
  "questions": {
    "safe": {"question": "Is it safe to merge now?", "yes": "No open risk", "no": "Something still blocks it"}
  }
}
```

`decide` turns each question into a `noul` question (instructions plus true/false criteria) and prints:

```
# Merge the cache fix?
0.91  safe: Is it safe to merge now?
```

Keep `state` small: one PR, one hunk, one comment. Jev's window is 32k tokens, and `decide` refuses a request it estimates is larger.

## The key

Looked up in this order, so hub terminals, fleet workers and Codex (which strips `*KEY*` variables from the commands it runs) all reach it:

1. `$TYPESAFE_API_KEY`
2. The file `jev.env_file` from the config (default `~/.config/elastio/typesafe.env`, chmod 600)
3. The macOS keychain item `jev.keychain` (default `typesafe-jev-personal`)

The key is sent in a header read from a file descriptor, not on the command line, so `ps` doesn't show it.

## Settings

| Variable | Default | Meaning |
| --- | --- | --- |
| `TYPESAFE_BASE_URL` | `https://api.typesafe.ai` | API base. Requests go to `/v1/systemone`. |
| `TYPESAFE_DEFAULT_MODEL` | `jev-latest` | Model when the request doesn't name one. |
| `JEV_TIMEOUT` | `60` | Seconds per request. Failed requests are retried 3 times. |

The `jev` skill tells Claude and Codex when and how to use it.
