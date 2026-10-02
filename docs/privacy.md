# Privacy and local redaction

qcode enables local text filtering by default. Detected values become
`[REDACTED]` in terminal output and history, tool previews and diffs, diagnostics,
JSON events, saved sessions, HTML exports, and browser remote views. Raw and
pretty exports use the same policy as browser downloads; `/clear` retains the
filtered export archive.

Built-in detection covers common OpenAI, GitHub, and AWS credential forms,
credential assignments (including quoted names and values), JSON credential
fields, Bearer tokens, and complete PEM private-key blocks. Credential field
names include API keys, passwords, secrets, access and refresh tokens,
authorization, client secrets, and private keys. The effective provider key and
nonempty supported credential environment values are also masked wherever they
appear, including their JSON-escaped form. Registered values and matched text
are never included in redaction diagnostics.

Supported environment sources are `QCODE_API_KEY`, `OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, `OPENCODE_API_KEY`, `OPENCODE_GO_API_KEY`, `GITHUB_TOKEN`,
`GH_TOKEN`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`,
`GOOGLE_API_KEY`, `GEMINI_API_KEY`, `BRAVE_API_KEY`, `TAVILY_API_KEY`, and
`SERPER_API_KEY`. Custom rules and sink switches are described in
[Configuration](configuration.md#local-sensitive-data-filtering).

## Saved sessions

With `persistence = true`, session files contain permanently redacted text.
Filtering decodes tool arguments stored as bytes and reconstructs styled
history cells before matching. Unfinished response and terminal parser buffers
are omitted. Work history, reasoning text, inspectable provider replay text,
plans, skill draft contents, learning context, previews, diffs, and persisted
composer drafts are filtered too. Session files keep private permissions.

Older files stay unchanged until resumed. Session lists and browser catalogs
filter their display copies in memory. Resume acquires the session lock, loads
and sanitizes the snapshot, and atomically rewrites it before building the
restored interface. A failed rewrite aborts resume; no unredacted backup is
created. The snapshot version remains compatible. Resumed tasks receive the
redacted conversation and may need secrets to be supplied again.

## Scope and limits

Local filtering does **not** filter provider requests. Active prompts, tool
arguments, tool results, and provider responses remain available to the running
agent. A secret may still be sent to a provider, passed to a shell command, or
written by a tool into a workspace file. Provider-request filtering is a
separate follow-up to issue #32.

Image contents and opaque encrypted or signed provider replay fields are
retained without inspection. Image detection is also a separate follow-up.
Required workspace and directory-grant metadata, binary images, routing
identifiers, model and tool identifiers, counters, and timestamps remain
usable. Sensitive operational paths can therefore still occur in session
metadata. This is a text-redaction feature, not encryption or a guarantee that
session files contain no sensitive information.

Live editable input and the dedicated remote-login credential screen remain
visible so you can compose text and sign in. Submitted history and persisted
draft copies are filtered. Question options are displayed with numbers; a
submitted number resolves to the original option internally. Browser directory
approval resolves the original proposed directory on the server.

Detection can produce false positives and miss unfamiliar formats, encoded
values, text split across logical lines, or secrets absent from registered
sources. Custom patterns apply one logical line at a time. Streams buffer lines
up to 64 KiB, discard overflowing lines through their terminator, and retain
PEM state between lines. They flush final partial lines at response boundaries.

Disabling a sink opts out of its protection. Already redacted text cannot be
recovered by disabling another sink: for example, a raw export cannot recover a
secret removed from terminal history. Learning extraction and validation still
reject detected credentials, independently of the local display switches.
