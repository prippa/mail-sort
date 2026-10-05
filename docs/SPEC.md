# MailSorter specification

Working title: MailSorter. One self-contained binary classifies each message by content and files it into a folder. A non-technical person can run it, fill in a form, and press Start.

Code, comments, and docs are English. The UI is English and Russian. Category criteria sent to a model are English, because TypeSafe documents English as the language where Jev is most accurate. Display names are localized.

This file is the product contract. [AGENTS.md](../AGENTS.md) repeats the safety rules that must not be weakened.

## Hard constraints

1. Never delete mail. Only move, copy, or label. Never run a plain `EXPUNGE`. Never open Trash, Junk, Drafts, or Sent unless the user explicitly selected that folder as a source.
2. Never change read state. Fetch with `BODY.PEEK`. Never set `\Seen`.
3. On any error or uncertainty (API failure, low confidence, unknown category), the message stays where it is, or goes to "Needs review" when the policy says so.
4. The first run of every profile is a dry run. Nothing moves until the user confirms. The confirmation is a flag in SQLite, so copying the YAML to another machine dry-runs again.
5. Pure Go. `CGO_ENABLED=0` must build `windows/amd64`, `linux/amd64`, and `linux/arm64`. The dev machine is Arch Linux cross-compiling to Windows. No cgo dependencies (no go-sqlite3, Fyne, or Wails).
6. No telemetry and no auto-update. Network traffic only to the user's mail server, the classifier endpoints the user enabled, OAuth endpoints, and (opt-in, after confirmation) autodiscovery.
7. Secrets (passwords, API keys, tokens) never appear in logs, config files, or error messages.

`UID EXPUNGE` is allowed only for the exact UIDs just copied, and only when `MOVE` is missing and `UIDPLUS` is advertised. If neither exists, refuse to move and offer copy-only.

## Decisions

Approved with the phase 0 plan:

- Module `github.com/prippa/mail-sort`. Binary `mailsorter`.
- One mailbox per profile, many profiles.
- `go 1.27.0` in `go.mod` (this machine's toolchain is a local Go 1.27.0). CI uses upstream Go 1.27.1, released 2026-08-28.
- go-imap pin is `v2.0.0-beta.8.0.20260702120225-f68ef419e622`, the `v2.0.0-beta.8` tag plus six commits. One of them stops the client from tearing the connection down on a dynamic `COPYUID`, which undo needs. There is still no stable v2 release.
- GMX preset host is `imap.gmx.com`, which is what GMX documents. `imap.gmx.net` is not on that page.
- Model-facing criteria are English. Russian is for the UI.
- Microsoft 365 / Outlook.com is OAuth2 only. Gmail offers OAuth first and an app password second.
- No app-only Microsoft client-credentials flow in phases 0–6.
- No macOS build.
- OAuth client ids are empty in source. `-ldflags` may set `internal/buildinfo.DefaultGoogleClientID` and `DefaultMicrosoftClientID`.
- Cost uses prices the user types. The models page currently lists $0.042 per million input tokens and free output; that is a hint, not a built-in rate.
- Default Jev client rate is 2 requests/s, burst 4, user-adjustable. Honor `Retry-After` on 429 and 529, then exponential backoff with jitter.
- At most 253 user categories, so `needs_review` and `keep_in_inbox` fit in Jev's 255-option Choice limit.
- Config schema rejects unknown fields so a secret cannot hide under a typo. The schema grows with each phase.
- YAML anchors and aliases are rejected.
- Log files are mode `0600`, state and config directories `0700`. Rotation is in-process: 5 MiB, 5 files. Subjects are off unless the user opts in. Bodies are always redacted.

## Layout

```
cmd/mailsorter/          CLI
internal/buildinfo/      ldflags version and optional OAuth client ids
internal/config/         YAML and paths
internal/secrets/        key names and redaction policy; storage comes later
internal/logging/        JSON slog and rotation
internal/store/          SQLite state (phase 3)
internal/i18n/           EN + RU catalogs
internal/mail/           backend interface and IMAP (phase 1+)
internal/message/        parse and clean (phase 1)
internal/classify/       rules, Jev, LLMs, chain, cache (phase 2)
internal/engine/         plan, apply, undo, watch (phases 3 and 6)
internal/ui/             HTTP API and embedded pages (phase 4)
internal/deps/           version pins, not linked into the binary
```

IMAP types do not leave `internal/mail`.

## CLI

Global flags come before the command. `--config` overrides the default path.

| Command | Phase | Behavior |
| --- | --- | --- |
| `ui` (default) | 4 | local page on 127.0.0.1; per-launch token in the cookie and header |
| `test-conn` | 1 | connects with a password, prints capabilities and folders |
| `dev-clean` | 1 | prints cleaned messages; does not move mail |
| `classify` | 2 | classifies one message from stdin; does not move mail |
| `categories export` | 2 | writes the starter category list as YAML |
| `run` | 3 | dry-run by default; `--confirm` stores the SQLite confirmation; `--apply` files that plan |
| `undo` | 3 | moves a filed run back, or reports a copy left in place |
| `watch` | 6 | after confirmation, files mail that arrives later; IDLE renewed before 29 minutes; poll every 5 minutes without IDLE |
| `version` | 0 | prints the ldflags version, or `dev` |
| `help` | 0 | usage |

Exit 0 on success, 1 on a config or log error, 2 on usage or a command this build does not run. Errors are one human line. No stack traces.

`version` reads a config file only when `--config` is set, so a broken config cannot hide the version. Other commands load the default file when it exists.

## Paths and config

| OS | Config | State (logs, database) |
| --- | --- | --- |
| Linux | `$XDG_CONFIG_HOME/mailsorter` or `~/.config/mailsorter` | `$XDG_STATE_HOME/mailsorter` or `~/.local/state/mailsorter` |
| Windows | `%AppData%\MailSorter` | `%LocalAppData%\MailSorter` |

The file is `config.yaml`. A relative `XDG_STATE_HOME` is ignored. Files larger than 1 MiB are rejected.

Settings:

```yaml
language: auto # auto, en, ru
profiles:
  - name: Work
    provider: gmail
    host: imap.gmail.com
    port: 993
    security: implicit_tls # or starttls
    username: ada@example.com
    email: ada@example.com
    password_env: MAIL_SORTER_PASSWORD_WORK
    auth: password # password, oauth_google, oauth_microsoft
    # host_id: personal   # zoho and workmail, when host is empty
    # ca_file: /path/to/ca.pem
    # cert_sha256: 64 hex characters
    discover: false # custom provider with an empty host
    max_chars: 1500
privacy:
  # redact_email: true
  # redact_phone: true
  # subject_only: true   # send subject and sender only
  # rules_only: true
  # local_only: true     # refuse a classifier that is not on this computer
categories_file: categories.yaml # optional; a missing file uses the starter list
classifiers:
  - provider: jev # jev, openai_compatible, anthropic
    # model defaults to jev-1.13.0; jev-latest and jev-preview are aliases
    base_url: https://api.typesafe.ai
    # key_env defaults to TYPESAFE_API_KEY for jev
    min_confidence: 0.8
    # min_margin: 0.1
    rps: 2
    burst: 4
    # price_input and price_output are dollars per million tokens, optional
  - provider: openai_compatible
    model: your-model-id # required; this program does not pick one
    base_url: https://api.openai.com/v1 # or http://127.0.0.1:11434/v1
    key_env: OPENAI_API_KEY
    min_confidence: 0.7
  - provider: anthropic
    model: your-model-id
    base_url: https://api.anthropic.com
    key_env: ANTHROPIC_API_KEY
```

`auth` defaults to a password when `password_env` is set and the preset allows it. Gmail without `password_env` uses `oauth_google`. Microsoft accepts only `oauth_microsoft`. Sign in from the local page. The refresh token stays in the keyring, or in the encrypted file when Secret Service is missing. `client_id`, `tenant`, and `device_code` are profile fields. A client id is not a secret. An empty `client_id` uses the id injected at build time. `discover: true` is used when `provider` is `custom` and `host` is empty. Lookup order is the ISP autoconfig URL, `/.well-known/autoconfig/mail/config-v1.1.xml`, then `https://autoconfig.thunderbird.net/v1.1/{domain}`, then RFC 6186 `_imaps._tcp`. The local part of the address is not sent. HTTP and host guessing are not used.

`classifiers` is the enabled chain, in order. An empty list calls no provider. `key_env` is a variable name. `api_key` is rejected. Jev's base URL is the origin (`POST /v1/systemone`). An OpenAI-compatible base URL includes `/v1` (`POST /chat/completions`). Anthropic's base URL is the origin (`POST /v1/messages`). A missing or rejected key stops the chain. Category rubrics live in `categories.yaml` beside the config, or in `categories_file`.

`password_env` is a variable name. These environment variables are the headless overrides:

- `TYPESAFE_API_KEY`
- `MAIL_SORTER_MASTER_PASSWORD`
- `MAIL_SORTER_PASSWORD_` plus the profile-specific suffix stored in `password_env`

The keyring service name is `mailsorter`. Windows Credential Manager limits a secret to 2560 bytes (`go-keyring` documents this). Persist the refresh token only. Access tokens stay in memory. If Secret Service is missing on Linux, use an encrypted file (argon2id + XChaCha20-Poly1305) and require the master-password variable for headless runs.

Do not add config keys whose names contain `password`, `secret`, `token`, or `bearer`, except `password_env`. `client_secret` is rejected. A public PKCE client has no client secret.

## Dependencies

Direct modules, pinned by `internal/deps` (and by `internal/config` for YAML):

| Module | Version | License | Why |
| --- | --- | --- | --- |
| `github.com/emersion/go-imap/v2` | `v2.0.0-beta.8.0.20260702120225-f68ef419e622` | MIT | IMAP client and in-memory server (`imapserver/imapmemserver`). `FetchItemBodySection.Peek` exists. Modified UTF-7 is internal; test it through CREATE/LIST. |
| `github.com/emersion/go-message` | `v0.18.2` | MIT | MIME. |
| `modernc.org/sqlite` | `v1.60.1` | BSD-3-Clause | Pure-Go SQLite. Requires Go 1.26. |
| `github.com/zalando/go-keyring` | `v0.2.8` | MIT | Windows Credential Manager and Linux Secret Service. The Linux file does not require cgo. |
| `golang.org/x/oauth2` | `v0.37.0` | BSD-3-Clause | Authorization code, PKCE, device code. |
| `golang.org/x/text` | `v0.42.0` | BSD-3-Clause | Charsets, including windows-1251 and koi8-r. |
| `golang.org/x/crypto` | `v0.57.0` | BSD-3-Clause | argon2id and XChaCha20-Poly1305. |
| `golang.org/x/net` | `v0.59.0` | BSD-3-Clause | HTML-to-text for HTML-only messages. |
| `gopkg.in/yaml.v3` | `v3.0.1` | MIT and Apache-2.0 | Config and category import/export. `go.yaml.in/yaml/v4` is still a release candidate. |

`github.com/emersion/go-sasl` `v0.0.0-20241020182733-b788ff22d5a6` (MIT) is indirect. It has OAUTHBEARER and no XOAUTH2. Implement the initial response by hand: `user=` + email + `\x01auth=Bearer ` + token + `\x01\x01`, then base64. Google and Microsoft both document that form.

Alpine.js `v3.17.4` (MIT) is vendored in phase 4. No npm and no CDN.

Not used: a log-rotation library, an HTML-to-text library, a TypeSafe SDK (the docs list Python and JavaScript only), go-sqlite3, Fyne, Wails.

`go test -race` uses cgo for the race runtime. Release builds and `make test` set `CGO_ENABLED=0`. Dependencies must still compile with cgo off.

## Provider presets

Each preset stores the source URL it was copied from. Phase 1 puts these in embedded JSON. Values below were read from the provider's own page unless marked `// VERIFY`.

| Preset | Host | Security | Auth |
| --- | --- | --- | --- |
| Gmail | `imap.gmail.com:993` | implicit TLS | Scope exactly `https://mail.google.com/`. App passwords still exist and are discouraged. OAuth is offered first. Developer page updated 2026-09-15. |
| Microsoft 365 / Outlook.com | `outlook.office365.com:993` | implicit TLS | OAuth2 only. Scope `https://outlook.office.com/IMAP.AccessAsUser.All` plus `offline_access`. IMAP is off until the user enables it. Device code is documented. |
| iCloud | `imap.mail.me.com:993` | implicit TLS | App-specific password. Apple says the username is usually the local part (support article 2026-02-03). |
| Yahoo | `imap.mail.yahoo.com:993` | implicit TLS | App password. Full email address. |
| Fastmail | `imap.fastmail.com:993` | implicit TLS | App password required. Full email address. |
| Zoho | see below | implicit TLS 993 | IMAP must be enabled. App password if 2FA or SAML. |
| GMX | `imap.gmx.com:993` | implicit TLS | IMAP must be enabled in the account. |
| Yandex | `imap.yandex.com:993` | implicit TLS | App password. For `@yandex.com`, the username is the local part. `imap.ya.ru` is the documented alternate outside Russia. |
| AWS WorkMail | `imap.mail.<region>.awsapps.com:993` | implicit TLS | Full email. Documented regions: `us-east-1`, `us-west-2`, `eu-west-1`. |
| Proton Mail Bridge | `127.0.0.1:1143` | STARTTLS | `// VERIFY` against Proton's own support page. Never skip certificate verification; pin the fingerprint or supply the Bridge CA. |
| Mail.ru | `imap.mail.ru` | `// VERIFY` | Host is from the product brief. Confirm on `help.mail.ru` before shipping the preset. |
| Custom | user host | 993 implicit TLS or 143 STARTTLS | Autodiscovery is opt-in. |

Zoho, from the IMAP guide and the autodiscovery article: personal/free US `imap.zoho.com`; paid org US `imappro.zoho.com`; sample org hosts `imappro.zoho.in`, `imappro.zoho.eu`, `imap.zoho.com.cn`, `imap.zoho.jp`, `imap.zoho.com.au`. The preset is that list plus "paste the host from Zoho settings". Do not invent other regional personal hosts.

Connection rules: implicit TLS on 993 and STARTTLS on 143. Verify certificates. A profile may set a custom CA or a pinned fingerprint. Never skip verification silently. After connect, show `MOVE`, `UIDPLUS`, `IDLE`, `SPECIAL-USE`, and `X-GM-EXT-1`. At most two concurrent IMAP connections, per-command timeouts, reconnect with exponential backoff and jitter, and honor context cancellation.

On `AUTHENTICATE` failure, show a provider-specific hint. For Gmail, say that an app password needs 2-Step Verification and that Workspace or an admin policy may require OAuth. For Microsoft, say that IMAP may be disabled or may need admin consent.

### OAuth

Bring-your-own client. Authorization code plus PKCE, loopback redirect on a random free port, system browser, and the URL printed when there is no browser. Microsoft also has the device-code flow. Google is loopback only: its device flow does not cover the Gmail scope.

Google: `access_type=offline` and `prompt=consent`. The consent screen must be "In production" (unverified is fine for personal use). Refresh tokens from "Testing" expire after 7 days. On `invalid_grant`, say to re-authorize.

Microsoft: tenant is `common`, `organizations`, `consumers`, or a tenant id. Public client. Redirect `http://localhost`.

Refresh the access token about five minutes before expiry. On `AUTHENTICATE` failure, refresh once and retry. Persist a rotated refresh token.

Gmail labels: MOVE from INBOX archives and labels. COPY adds the label and leaves the message in INBOX. Category actions are "move" and "label only".

Folders: LIST with the hierarchy delimiter and SPECIAL-USE. Create missing targets only after confirmation (`CREATE` and `SUBSCRIBE`). Record `COPYUID` / `MOVE` results so a run can be undone. If `UIDVALIDITY` changes, fall back to `Message-ID`. Never process a message twice.

## Messages

Fetch `ENVELOPE`, headers `From`, `To`, `Cc`, `Subject`, `Date`, `List-Id`, `List-Unsubscribe`, `Auto-Submitted`, `Precedence`, `BODYSTRUCTURE`, and the first `text/plain` part (partial, about 8 KB). If the message is HTML-only, fetch at most about 64 KB and convert to text with `golang.org/x/net/html`. All fetches use `BODY.PEEK`.

Decode MIME words, quoted-printable, base64, and charsets. Strip quoted replies and signatures, including Russian "написал(а)" patterns. Replace URLs with their domain, collapse whitespace, and cap the body at `max_chars` (default 1500). Attachments contribute name, MIME type, and size only.

Classification features: from, to, subject, date, `is_bulk` (`List-Unsubscribe`, `List-Id`, `Precedence`), attachment names, body excerpt.

## Classification

A category has an ASCII-slug key, a display name, a description written as criteria (what belongs and what does not), optional examples, a target folder, an action (`move`, `label`, or `none`), and an optional minimum confidence.

Reserved categories, always sent to the model: `needs_review` (folder "Needs review") and `keep_in_inbox` (no-op).

Starter templates, importable and exportable as YAML: `invoices_receipts`, `newsletters`, `notifications`, `social`, `travel`, `support_requests`, `security_alerts` (keep in inbox), `personal`, `work`, `promotions`.

Pipeline: skip filters, deterministic rules, cache, provider chain, confidence gate, action.

Rules run before any model and can match from, domain, to (including plus-addressing), subject regex, header presence or value, keywords, and attachment type. Actions: assign a category, `keep_in_inbox`, or `never_touch`.

### Jev

`POST {base_url}/v1/systemone`. Default base `https://api.typesafe.ai`. `Authorization: Bearer`. The key comes from the keyring or `TYPESAFE_API_KEY`.

The folder question is a Choice. `state` carries `from`, `to`, `subject`, `date`, `is_bulk`, `attachments`, and `body`. The question key is `folder`. Instructions tell the model to judge by content. Criteria are the category descriptions plus `needs_review` ("Unclear, ambiguous, or none of the above"). `keep_in_inbox` is also always an option.

Jev also sends a Noul question, key `urgent`, unless `urgent: false`. Instructions ask whether the message is time-sensitive. Criteria are `true` and `false` sentences in English. The response `answers.urgent` is `{type, noul}`. `noul` is a probability from 0 to 1 and has no confidence field. The default cutoff is 0.80, a placeholder like the Choice cutoff; `urgent_min` replaces it. At or above the cutoff, a `move` stays in the inbox (`action: none`) and the dry run says why. A `label` is still applied. Rules still win, because they run before the model. The cache key changes when the question is turned on. Changing the cutoff reuses a stored probability.

The response `answers.folder` is `{type, choice, probabilities, confidence}` plus `usage` `{input_tokens, output_tokens}`. Store the response `model` id with the decision. Default request model is `jev-1.13.0`. `jev-latest` and `jev-preview` may be selected; the UI warns that aliases move. As of the models page both aliases point at `jev-1.13.0`.

Choice confidence is `(p_max - 1/n) / (1 - 1/n)`. The same 0.80 means a different top probability as categories are added. Default minimum confidence 0.80 is a placeholder and the UI says so. Optional `min_margin` (`p_top - p_second`) is off unless set.

401 and 422 are not retried. 422 is logged without the message body. 429 and 529 retry, as do transport failures. Other HTTP 5xx responses are not retried, so the next provider can run. The TypeSafe SDK also retries 408 and 500–599; this client follows the statuses named here. If there is no key, show onboarding and do not call another provider. A rejected key stops the chain the same way.

### Other providers

`openai_compatible`: `base_url`, `key_env`, `model`. The key stays in the environment because `api_key` is not allowed in the config file. Covers OpenAI, OpenRouter, Groq, Ollama (`http://localhost:11434/v1`), LM Studio, and vLLM. Prefer JSON schema, then JSON mode, then tolerant parsing.

`anthropic`: Messages API with a forced tool call of the same schema.

Output: `category` (enum of keys), `confidence` 0..1, `reason` at most 140 characters. Temperature 0. An unknown category becomes `needs_review`. LLM confidence is self-reported; the default threshold is 0.70 and the UI says it is not calibrated.

The message is untrusted data inside a delimited block. The model has no tools. The app acts only from the category map.

Provider chain: ordered, each with its own thresholds. Default `[jev]`. Example: Jev at 0.80, then an LLM at 0.70, then `needs_review`. A provider error tries the next provider. If all fail, leave the message and retry next run. Never send mail to a provider the user has not enabled.

Cache key: SHA-256 of normalized features, categories hash, and provider/model. Value: the decision, in SQLite. The classification cache is the `classification_cache` table in `mailsorter.db`. Confirmation, runs, and undo rows are the other tables in that file.

Before a remote call, card-like numbers (13–19 digits with single spaces or dashes), IBANs, and runs of 8 or more digits are redacted. Eight is a product choice; the spec does not set the length. Rules still see the unredacted text. Email and phone redaction stay off.

## Engine

Source folders default to INBOX. Filters: unread only, since date, max N newest first, skip flagged, skip drafts. Flagged messages and drafts are skipped unless `--include-flagged` or `--include-drafts` is set. Classify with a worker pool (default 4) and rate limiting. IMAP mutations are serialized on one connection.

`run` stores a dry run and does not move mail. `--override UID=category` edits that plan. `--confirm` writes the SQLite confirmation and still does not move mail. `--apply` files the stored plan and does not classify again. A new dry run replaces an unapplied one. A partial apply must be finished or undone before the next plan.

State: profile, mailbox, `UIDVALIDITY`, UID, `Message-ID`, decision, provider/model, confidence, applied flag, run id, destination UID, timestamps. Subject and from are stored in the 0600 database so the dry run can be shown again. They are not written to the log.

Dry run shows subject, from, predicted category, confidence, and provider. The user can override a row, then apply the selection.

Safety: max moves per run default 200, counting copies as well. Rows past the cap stay pending. Abort after 5 consecutive classifier errors. A message already filed for the profile is not filed again; if `UIDVALIDITY` changes, the Message-ID is used.

MOVE is sent only when the server advertises MOVE or IMAP4rev2. Otherwise UIDPLUS copies the message, marks that UID `\Deleted`, and UID EXPUNGEs that UID. Without MOVE and without UIDPLUS the move is refused; `--copy-only` copies and leaves the original. A label is always a copy. Undo moves a moved message back. Undo leaves a copy in place, because removing it would delete mail.

Undo reverses a run or selected rows from the stored UIDs and reports what failed.

`backend` on a profile is `imap` (the default), `gmail`, or `graph`. Gmail API filing uses the same `https://mail.google.com/` token: find the message with `rfc822msgid`, refuse `TRASH`, `SPAM`, `DRAFT`, and `SENT`, then `messages.modify` to add the destination label. A move out of INBOX also removes the `INBOX` label. A copy only adds the label. `UNREAD` is never removed. Microsoft Graph filing is a second sign-in with delegated `Mail.ReadWrite` (`https://graph.microsoft.com/Mail.ReadWrite` plus `offline_access`), because that resource is not the IMAP scope. It finds `internetMessageId`, refuses Deleted Items, Junk, Drafts, Sent Items, and recoverable deletions, then `POST /me/messages/{id}/move` or `/copy`. INBOX uses the well-known name `inbox`. Nested folder names are left in place. An API move still needs the Message-ID. Undo of an API move finds the message over IMAP and files it back through the same API. A missing Graph sign-in fails the move and leaves the message.

Watch: IMAP IDLE, re-issued before 29 minutes, polling fallback default 5 minutes, reconnect, token refresh, SIGINT/SIGTERM. The pinned IMAP client restarts IDLE every 28 minutes. `watch` refuses to dial until the profile is confirmed, and it leaves an open dry run in place. A partial apply continues, at most the move cap, then waits one poll interval. README includes a systemd user unit and a Windows Task Scheduler example. An unsigned Windows build can show SmartScreen.

Logging is JSON lines. Per-run summary: counts per category, API calls, tokens, latency, and a cost estimate when the user entered prices. CSV export.

## UI

Local web UI, embedded, bound to `127.0.0.1` on a random port. A per-launch token is required on every API call (cookie and header). Strict Host and Origin checks. Content-Security-Policy. No external requests. Human-readable errors.

First-run wizard: provider, authenticate, category template, classifier, dry run, review, apply.

Screens: Accounts, Categories, Classifier (including a "Try it" box that shows exactly which fields would be sent, and the Jev urgency cutoff), Run, Automation, Activity, Evaluate, and Settings (language auto/en/ru, privacy, data dir, redaction).

Evaluate lists classified messages stored on this computer. The model's category is kept when the user relabels a row. A suggestion needs at least 8 labels (a correction, or an applied row left as filed). It sweeps cutoffs from 0.50 to 0.95 and prefers the lowest cutoff that misfiles none of those labels. The page says the number is not a calibrated score. Saving it writes `min_confidence` on the first Jev classifier. The page does not call a provider.

In-app EN/RU setup guides for the Google Cloud and Entra consoles.

Consent screen before the first remote classification, naming the fields and the provider. Redaction masks card-like numbers, IBANs, and long digit runs by default; emails and phones are optional. `body_chars` limit, "subject + sender only", local-only Ollama, and rules-only are available. Never render HTML, load remote content, or follow links.

## Build

`make build` produces `dist/mailsorter-linux-amd64`, `dist/mailsorter-linux-arm64`, and `dist/mailsorter-windows-amd64.exe` with `-trimpath` and `-ldflags "-s -w"`. Version comes from `git describe --tags --always --dirty`.

CI (`.github/workflows/ci.yml`): `CGO_ENABLED=0 go test ./...`, `go test -race ./...`, golangci-lint v2.14.0, and the three cross-compiles. An unsigned `.exe` triggers Windows SmartScreen; the README will say so.

## Phases

0. Scaffold. This document, safety rules, config, logging, CLI, build, CI.
1. IMAP with password auth, presets, folders, fetch, preprocessing, `test-conn`, and a dev print of cleaned messages. In-memory IMAP tests.
2. Categories, rules, Jev, OpenAI-compatible, Anthropic, chain, gating, cache, `classify --stdin`. Mocked HTTP tests.
3. Dry run, apply, SQLite, idempotency, undo, safety caps.
4. Web UI for password auth, EN/RU, and the security measures above.
5. OAuth for Google and Microsoft, XOAUTH2, refresh, keyring, setup guides.
6. Watch, hardening, packaging, CI green, README.
7. Optional: Graph / Gmail API backends, a Noul "urgent" question, and an Evaluate screen.

Stop after each phase until the user says `next`.

## Still unverified

These stay `// VERIFY` until the cited source is read in the phase that implements them:

- Mail.ru host, port, and app-password rule on `help.mail.ru`. The preset is not shipped.
- Proton Mail Bridge host, port, STARTTLS, and certificate instructions on a Proton support page. The preset is not shipped.
- AWS WorkMail regions beyond `us-east-1`, `us-west-2`, and `eu-west-1` on the AWS endpoints page.
- Zoho personal IMAP hosts outside `imap.zoho.com` and `imappro.zoho.com`. The current IMAP guide says to paste the datacenter host from the account.
- A live ISPDB domain document. The index `https://autoconfig.thunderbird.net/v1.1/` exists; `gmail.com` returned HTTP 500. Thunderbird's autoconfig page says it does not use DNS SRV; this program still tries RFC 6186 last.
- Live-server `UID MOVE` and `UID EXPUNGE`. The in-memory server covers MOVE, and COPY plus STORE `\Deleted` plus UID EXPUNGE of one UID, including a second `\Deleted` message that must stay. A dynamic COPYUID has no numeric UID here; the fallback is a Message-ID search. `Authenticate(sasl.Client) error` is the pinned signature. The XOAUTH2 initial response is tested against Microsoft's documented example. Live Gmail and Microsoft sign-in were not used.
- Whether a Google Desktop client accepts an unregistered random `http://127.0.0.1` port, and whether Entra treats a registered `http://localhost` as matching `http://localhost:<port>`. If the provider reports a redirect mismatch, add the printed address and sign in again.
- Live IMAP IDLE against Gmail or Microsoft. The in-memory server covers an IDLE wake and the poll fallback. The pinned `go-imap` client restarts IDLE every 28 minutes.
- A live Jev, OpenAI, or Anthropic call. The adapters follow the published request shapes and are tested with a local HTTP server. OpenAI usage is read from `prompt_tokens` / `completion_tokens`, and also from `input_tokens` / `output_tokens` when a proxy sends those names.
- Anthropic models that reject `tool_choice` type `tool` (the primer names Opus 5.5, Sonnet 5.5, Fable 5.1, and Mythos 5.1). The client retries a 400 without the force, then once without `strict`. That fallback was not verified against a live model.
- A live Jev Noul answer. The request follows the published `noul` question (`instructions`, criteria `true` and `false`) and reads `answers.urgent.noul`. The 0.80 cutoff is a product placeholder. The local HTTP test covers the body and the inbox hold.
- A live Gmail `messages.modify` and a live Graph `message: move`. The clients follow the published methods and are tested with a local HTTP server. `format=minimal` on `messages.get` was not re-read. The Graph scope string `https://graph.microsoft.com/Mail.ReadWrite` was not sent to a live authorize endpoint. The permission name `Mail.ReadWrite` is the least-privileged delegated permission on the move and copy pages.
