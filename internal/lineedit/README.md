This package derives from golang.org/x/term v0.29.0's terminal.go (BSD license,
included here). Raw terminal setup still uses golang.org/x/term.

The local line editor tracks terminal columns separately from rune indexes.
Cursor movement and writing share the same layout, including wide characters
at the right margin. Replacing or deleting input clears every previously
occupied row before repainting, so shorter lines cannot leave stale text.

Bracketed pastes are buffered as literal text, normalize CR/CRLF to LF, and
remain in one draft until an explicit submit key. Paste blocks render as
`[paste N characters]` and move/delete atomically; submission returns the full
content. External renderers keep the full draft through `RenderInput` and use
`DisplayInput` for the compact text and cursor position.

Keep these changes and the rendering regression tests when syncing upstream.
