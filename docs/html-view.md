# HTML view

## Open Graph meta

HTML responses carry Open Graph + Twitter Card meta tags so links
shared on Slack / Discord / Twitter / Facebook unfurl with a title,
description, and preview image. The `og:image` URL points at the
same page's SVG render (`?format=svg`); Discord, Slack, and Telegram
display SVG previews, while Twitter and Facebook fall back to the
plain link if their crawler can't decode SVG. Switching the preview
to PNG (universal compatibility) is a one-line query change.

## Keyboard shortcuts

The `/stock` HTML view ships an inline JS handler that rewrites the
`?date=YYYY-MM-DD` parameter on these keys:

| Key       | Action                             |
| --------- | ---------------------------------- |
| `←` / `h` | Previous day                       |
| `→` / `l` | Next day                           |
| `t`       | Today (clear the `date=` override) |
| `?`       | Toggle the shortcut help overlay   |
| `Esc`     | Close the help overlay             |

On-screen prev / next chevron buttons sit at the left/right edges of
the viewport for mouse and touch users — clicking them is equivalent
to pressing the matching arrow key. Held arrow keys are short-
circuited via `e.repeat` so a long press doesn't slam the server.

Date navigation steps by calendar day; weekend / holiday quotes
fall back to the previous trading session server-side. `/now`, `/`,
and the 404 view do not register the handlers — the bindings only
attach on `/stock`.
