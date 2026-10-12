# Firekeeper End User License Agreement and Hosted Instance Terms

> **DRAFT. Not yet in effect, and not legal advice.** Have a lawyer review this
> before relying on it. Fill in every `[BRACKETED]` placeholder first.

Last updated: [DATE]

These terms are between you and Dan Bradbury ("**I**", "**me**"), the author
of Firekeeper. Part 1 covers the Firekeeper software. Part 2 covers the
Firekeeper server I run at `firekeeper.danbradbury.net` (the "**Hosted
Instance**"). Part 3 applies to both.

If you build Firekeeper from source and run your own server, only Part 1 and
Part 3 matter to you, and the [Apache License 2.0](LICENSE) is the license
that governs the code.

## Part 1. The software

### 1.1 License

Firekeeper's source code, and the binaries and container images I publish
(for example `ghcr.io/danbradbury/firekeeper`) (together, the "**Software**"),
are licensed to you under the Apache License 2.0. **Nothing in these terms
limits, adds conditions to, or takes away any right the Apache License 2.0
gives you.** If these terms and the Apache License 2.0 conflict about the
Software, the Apache License 2.0 wins. Third-party components included in
the Software are under their own licenses, listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

This Part only records how the Software behaves and what you are responsible
for when you use it.

### 1.2 What the Software does on your machine

- Plain `firekeeper` (the terminal dashboard) reads process lists and your
  coding agents' local session files, and uploads no session data.
- Its Usage tab may start your own `codex app-server`, which talks to OpenAI
  under your Codex account, and may send your GitHub token to GitHub to read
  your Copilot plan details. Those requests go to those providers, not to me.
- The `report`, `backfill`, `daemon`, and `login` subcommands send session
  metadata and transcripts to a Firekeeper server **you choose**, and only for
  providers you allow. You decide what is uploaded and where.

[PRIVACY.md](PRIVACY.md) describes this in detail.

### 1.3 Your responsibilities

- **Third-party terms.** Firekeeper reads data produced by tools such as
  Codex, GitHub Copilot CLI, Claude Code, Kimi Code, and OpenCode. You are
  responsible for making sure your use of Firekeeper, and any upload of that
  data, is allowed under those tools' terms, your employer's policies, and
  any confidentiality obligations that cover the code and conversations in
  your sessions.
- **Redaction is best effort.** Firekeeper tries to remove common secret
  formats before uploading, but it will miss things. Do not upload sessions
  that contain information you cannot afford to have read by whoever operates
  the receiving server.
- **Servers you run.** If you run `firekeeper serve` for other people, you are
  the operator of that server and responsible for it, including its privacy
  notice (`/privacy`), security, backups, and any legal obligations toward the
  people whose data it stores.

### 1.4 Trademarks

The Apache License 2.0 does not grant rights to the name "Firekeeper" or its
logo. You may use the name to truthfully describe the Software or a
modified version (for example, "based on Firekeeper"), but do not present a
modified build or a server you run as being made, endorsed, or operated by
me.

Codex, GitHub Copilot, Claude Code, Kimi Code, OpenCode, Ghostty,
Terminal.app, and iTerm2 belong to their respective owners. Firekeeper is not
affiliated with or endorsed by them.

## Part 2. The Hosted Instance

### 2.1 What it is

The Hosted Instance is a personal test bed that I run for myself and for
people I invite. It is not a commercial service, it is free, and it comes
with no availability, support, or data-retention commitment. **Its data may
be wiped at any time without notice.** Keep your own copy of anything you
care about.

### 2.2 Accounts

- You may create an account only with an invite code from me, and only if you
  are at least [16/18] years old.
- Use an email address you control. Keep your password and access tokens
  secret; you are responsible for activity under your account and tokens.
  Revoke a token you think is exposed (`DELETE /v1/tokens/{id}` or the account
  page) and tell me at [CONTACT EMAIL].
- One person per account. Do not share accounts or sell access.

### 2.3 Your content

"**Your Content**" is everything you or your machines upload: session
metadata, transcripts, tool output, and the original records.

- **You keep ownership** of Your Content.
- You give me a limited, non-exclusive, royalty-free license to store, copy,
  index, process, back up, and display Your Content **only to run the Hosted
  Instance for you**: to show it to you, search it, compute usage from it,
  export it, and delete it. This license ends when Your Content is deleted
  from the Hosted Instance and from its backups.
- I will not sell Your Content, use it for advertising, or use it to train
  machine-learning models. I will not read it except to operate, secure,
  debug, or back up the Hosted Instance, when you ask me to, or when the law
  requires it.
- You confirm you have the right to upload Your Content, and that uploading
  it does not breach any law, contract, confidentiality duty, or anyone
  else's rights.

### 2.4 Acceptable use

Do not use the Hosted Instance to:

- upload other people's personal data, credentials, or confidential
  information that you are not permitted to share, or any data that is
  subject to special regulation (for example health, payment-card, or
  government-classified data);
- upload malware, or content that is unlawful where you or I am located;
- probe, scan, attack, or overload the server, or try to access another
  account's data;
- get around account, storage, session, or rate limits, or use automated
  sign-up;
- resell or provide the Hosted Instance to others.

### 2.5 Limits, suspension, and termination

- The Hosted Instance enforces storage, session, and rate limits. I can change
  them at any time.
- You can stop at any time: download your data, then delete your account from
  the account page. Deletion removes your account, tokens, machines, sessions,
  and events. Backups age out as described in [PRIVACY.md](PRIVACY.md).
- I can disable or delete an account, or shut the Hosted Instance down, at any
  time and for any reason, including a breach of these terms. Where practical
  I will give notice and a chance to export first, but I do not promise to.

## Part 3. General

### 3.1 No warranty

THE SOFTWARE AND THE HOSTED INSTANCE ARE PROVIDED "AS IS" AND "AS AVAILABLE",
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING
WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE, TITLE, AND
NON-INFRINGEMENT. I DO NOT WARRANT THAT REDACTION WILL REMOVE ALL SECRETS OR
PERSONAL DATA, THAT SESSION STATES OR USAGE FIGURES ARE ACCURATE, OR THAT
STORED DATA WILL BE KEPT OR STAY AVAILABLE.

### 3.2 Limitation of liability

TO THE MAXIMUM EXTENT PERMITTED BY LAW, I AM NOT LIABLE FOR ANY INDIRECT,
INCIDENTAL, SPECIAL, CONSEQUENTIAL, OR PUNITIVE DAMAGES, OR FOR LOSS OF DATA,
PROFITS, REVENUE, OR GOODWILL, OR FOR THE DISCLOSURE OF ANY SECRET OR
INFORMATION THAT REDACTION DID NOT REMOVE, ARISING FROM THE SOFTWARE OR THE
HOSTED INSTANCE. MY TOTAL LIABILITY FOR ALL CLAIMS RELATING TO THE HOSTED
INSTANCE IS LIMITED TO [USD 50]. Some jurisdictions do not allow these
exclusions; there they apply to the extent allowed. For the Software, sections
7 and 8 of the Apache License 2.0 also apply.

### 3.3 Changes

I may update these terms. I will change the "Last updated" date, and for a
material change to Part 2 I will tell Hosted Instance users by email or a
notice on the dashboard at least [14] days before it takes effect. If you keep
using the Hosted Instance after that, the new terms apply; if you do not
agree, delete your account.

### 3.4 Governing law

These terms are governed by the laws of [STATE/COUNTRY], without regard to its
conflict-of-laws rules. Disputes go to the courts of [COUNTY, STATE/COUNTRY],
unless the law of the place you live gives you the right to bring them
elsewhere.

### 3.5 Other terms

If any part of these terms is unenforceable, the rest still applies. Not
enforcing a term is not a waiver of it. You may not transfer these terms; I
may transfer them to anyone who takes over the Software or the Hosted
Instance. These terms, the Apache License 2.0, and [PRIVACY.md](PRIVACY.md)
are the whole agreement between us about Firekeeper.

### 3.6 Contact

[CONTACT EMAIL]
