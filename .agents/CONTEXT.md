# Hooli Mail — Domain Context

The vocabulary the codebase uses. Architecture reviews (`improve-codebase-architecture`)
and naming decisions should reach for these terms and keep them sharp. When a concept
needs a name that isn't here, add it here first.

## Product

**Hooli Mail** — a self-hosted mail system: an SMTP/IMAP **server** and a terminal **client** (the TUI).
Two binaries built from one module:

- `server` (`cmd/server`) — receives and stores mail, serves it over IMAP.
- `tui` (`cmd/tui`) — the "Terminal Correspondence" client; talks to the server over IMAP/SMTP.

## Domain nouns

- **User** — an account, identified by email address, with a bcrypt password hash.
- **Mailbox** — a named folder of messages owned by one User (e.g. `INBOX`, `Sent`, `Drafts`, `Trash`, `Junk`).
  The set `models.DefaultMailboxes` is created for every new User.
- **INBOX** — the Mailbox messages are delivered into.
- **Email** (a stored **Message**) — a single piece of mail in a Mailbox: `From`, `To`, `Subject`, `Body`,
  `Flags`, `Date`, `Size`, plus IMAP addressing fields (`SeqNum`, UID).
- **Message** — used (lowercase, generic) for the RFC 5322 wire form before it is parsed into an Email,
  and for the "a sender wants to store this" concept behind the storage seam.

## Mail semantics

- **Flag** — an IMAP system flag on an Email: `\Recent`, `\Seen`, `\Deleted`, `\Flagged`, `\Draft`, `\Answered`
  (see `models` constants). Flags drive Mailbox status and which messages survive **Expunge**.
- **Delivery** — the act of placing a received Message into a recipient's INBOX. The receive-vs-submit
  distinction lives on the SMTP ports (see below), not in the domain noun.
- **SeqNum** vs **UID** — IMAP's two numbering schemes. SeqNum is a 1..N position within a Mailbox
  (renumbered on delete); UID is a stable, monotonic identifier. Status also reports UIDVALIDITY and UIDNEXT.

## The TUI's domain

- **Wax seal** — the brass (`seal`) unread indicator: a `██` bar rendered on unread rows that "breaks"
  when an Email is read (i.e. gains `\Seen`). The only place the accent colour appears.
- **Letter-tray rhythm** — the inbox is grouped into date buckets: **Today / Yesterday / This Week / Earlier**
  (see `dateGroup`). This grouping is a domain concept of the client, not the protocol.

## Wire / ports

| Port | Role                  | TLS      | Auth |
|------|-----------------------|----------|------|
| 2525 | SMTP receiving        | plain    | no   |
| 587  | SMTP submission       | STARTTLS | yes  |
| 143  | IMAP                  | STARTTLS | yes  |
| 993  | IMAPS                 | TLS      | yes  |
| 80   | ACME HTTP challenge   | plain    | —    |

## Design tokens (TUI theme)

Near-black canvas, single brass accent. Surface hierarchy via lightness shifts (parchment → paper → folded),
no visible borders. Token names: `ink`, `parchment`, `paper`, `folded`, `seal`, `postscript`, `faint`,
`watermark`. Kept sharp in `internal/config` (`ThemeConfig`).
