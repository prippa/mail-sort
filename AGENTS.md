# MailSorter agent guide

The product spec is [docs/SPEC.md](docs/SPEC.md). This file is the non-negotiable safety and workflow contract.

## Hard constraints

1. Never delete mail. Only move, copy, or label. Never run a plain `EXPUNGE`. Never open Trash, Junk, Drafts, or Sent unless the user explicitly selected that folder as a source.
2. Never change read state. Fetch with `BODY.PEEK`. Never set `\Seen`.
3. On any error or uncertainty (API failure, low confidence, unknown category), the message stays where it is, or goes to "Needs review" when the policy says so.
4. The first run of every profile is a dry run. Nothing moves until the user confirms. Store that confirmation in SQLite, not in the YAML file.
5. Pure Go. `CGO_ENABLED=0` must build `windows/amd64`, `linux/amd64`, and `linux/arm64`. No cgo dependencies.
6. No telemetry and no auto-update. Network traffic only to the user's mail server, the classifier endpoints the user enabled, OAuth endpoints, and (opt-in, after confirmation) autodiscovery.
7. Secrets (passwords, API keys, tokens) never appear in logs, config files, or error messages.

`UID EXPUNGE` is allowed only for the exact UIDs just copied, and only when `MOVE` is missing and `UIDPLUS` is advertised. If neither `MOVE` nor `UIDPLUS` exists, refuse to move and offer copy-only.

## Workflow

- Finish one phase, run the tests, report what works, what is stubbed, how to try it, and every `// VERIFY` item. Then stop until the user says `next`.
- One conventional commit per phase.
- Do not invent API shapes, OAuth scopes, or provider settings. Check current docs or the pinned source in `go.mod`. Mark anything unverified with `// VERIFY`.
- Stubs return a clear not-implemented error. No fake messages, capabilities, or classification results on a production path.
- IMAP types stay inside `internal/mail`.
- Interfaces live at the consumer. Pass `context.Context` through I/O. Wrap errors.
- Code, comments, and docs are English. UI strings exist in English and Russian.
- Category criteria sent to a model are English. Russian is for display strings.
- Do not ship an OAuth client id in source. A build may inject one with `-ldflags`.

## Layout

`cmd/mailsorter` is the binary. Packages: `internal/config`, `secrets`, `store`, `i18n`, `mail`, `message`, `classify`, `engine`, `ui`, `logging`, `buildinfo`. `internal/deps` only pins module versions.
