package auth

import (
	"html/template"
	"net/url"
	"strings"
)

// The page the browser lands on after Keycloak redirects back.
//
// It is the only interface lt has that is not a terminal, and it is the last
// thing a user sees in a flow that started with `lt auth login` — so it is
// worth looking like the product it just signed them in to rather than like a
// default error page.
//
// Two constraints shape it, and they are why this is hand-written rather than
// lifted from apps/libretimes-web:
//
//   - No network. Every byte is inline: no font CDN, no stylesheet, no image.
//     A sign-in callback that phones a third party mid-flow is a privacy leak,
//     and one served from a loopback port has to render before that socket
//     closes, which it does two seconds later.
//   - No build step. This binary cross-compiles to six targets from one Linux
//     machine with `go build` and nothing else. A Tailwind pipeline here would
//     be the only thing in the repo needing one.
//
// So the palette is transcribed from @libretimes/ui's default theme
// (apps/packages/ui/src/styles/themes/default.css) as literal oklch values,
// with an sRGB fallback for browsers that predate oklch, and the type stack
// names Geist first and falls through to the system UI face. That is a copy,
// and a copy can drift — it is four colours and a radius, wrapped around one
// sentence, so the drift costs nothing that matters.

// callbackPage renders both outcomes; callbackView decides which.
//
// html/template rather than string concatenation, because Code is echoed from
// the query string and anyone who can get the user to open a URL controls it.
// Contextual escaping is the difference between showing `<img onerror=...>`
// and running it, on a page whose origin has just handled an authorization
// code.
var callbackPage = template.Must(template.New("callback").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{ .Title }} · LibreTimes</title>
<style>
  :root {
    --radius: 0.625rem;
    --background: #ffffff;
    --foreground: #252525;
    --card: #ffffff;
    --muted-foreground: #8e8e8e;
    --border: #e5e5e5;
    --primary: #343434;
    --destructive: #d33c30;
  }
  @supports (color: oklch(0 0 0)) {
    :root {
      --background: oklch(1 0 0);
      --foreground: oklch(0.145 0 0);
      --card: oklch(1 0 0);
      --muted-foreground: oklch(0.556 0 0);
      --border: oklch(0.922 0 0);
      --primary: oklch(0.205 0 0);
      --destructive: oklch(0.577 0.245 27.325);
    }
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --background: #252525;
      --foreground: #fbfbfb;
      --card: #343434;
      --muted-foreground: #b4b4b4;
      --border: rgba(255, 255, 255, 0.1);
      --primary: #ebebeb;
      --destructive: #e8695c;
    }
    @supports (color: oklch(0 0 0)) {
      :root {
        --background: oklch(0.145 0 0);
        --foreground: oklch(0.985 0 0);
        --card: oklch(0.205 0 0);
        --muted-foreground: oklch(0.708 0 0);
        --border: oklch(1 0 0 / 10%);
        --primary: oklch(0.922 0 0);
        --destructive: oklch(0.704 0.191 22.216);
      }
    }
  }

  * { box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    margin: 0;
    display: grid;
    place-items: center;
    padding: 1.5rem;
    background: var(--background);
    color: var(--foreground);
    font-family: Geist, ui-sans-serif, system-ui, -apple-system,
      "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    font-size: 16px;
    line-height: 1.5;
    -webkit-font-smoothing: antialiased;
  }

  .card {
    width: 100%;
    max-width: 26rem;
    padding: 2.5rem 2rem;
    border: 1px solid var(--border);
    border-radius: calc(var(--radius) + 4px);
    background: var(--card);
    text-align: center;
    animation: rise 0.4s cubic-bezier(0.16, 1, 0.3, 1) both;
  }
  @keyframes rise {
    from { opacity: 0; transform: translateY(6px); }
    to   { opacity: 1; transform: none; }
  }

  .wordmark {
    font-size: 0.8125rem;
    font-weight: 600;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--muted-foreground);
  }

  .mark {
    margin: 1.75rem auto 1.25rem;
    width: 3.5rem;
    height: 3.5rem;
    color: {{ if .OK }}var(--primary){{ else }}var(--destructive){{ end }};
  }
  .mark svg { width: 100%; height: 100%; display: block; }
  .mark circle, .mark path {
    fill: none;
    stroke: currentColor;
    stroke-width: 2;
    stroke-linecap: round;
    stroke-linejoin: round;
  }
  .mark circle { opacity: 0.25; }
  .mark path {
    stroke-dasharray: 32;
    stroke-dashoffset: 32;
    animation: draw 0.45s 0.15s cubic-bezier(0.65, 0, 0.35, 1) forwards;
  }
  @keyframes draw { to { stroke-dashoffset: 0; } }

  @media (prefers-reduced-motion: reduce) {
    .card, .mark path { animation: none; }
    .mark path { stroke-dashoffset: 0; }
  }

  h1 {
    margin: 0 0 0.5rem;
    font-size: 1.375rem;
    font-weight: 600;
    letter-spacing: -0.015em;
  }
  p {
    margin: 0;
    color: var(--muted-foreground);
    text-wrap: pretty;
  }

  .code {
    margin-top: 1.25rem;
    padding: 0.625rem 0.875rem;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font-family: "Geist Mono", ui-monospace, SFMono-Regular, "SF Mono",
      Menlo, Consolas, "Liberation Mono", monospace;
    font-size: 0.8125rem;
    color: var(--foreground);
    overflow-wrap: anywhere;
  }

  .hint {
    margin-top: 1.75rem;
    padding-top: 1.25rem;
    border-top: 1px solid var(--border);
    font-size: 0.8125rem;
  }
  kbd {
    padding: 0.125rem 0.375rem;
    border: 1px solid var(--border);
    border-bottom-width: 2px;
    border-radius: 0.375rem;
    background: var(--background);
    font-family: "Geist Mono", ui-monospace, SFMono-Regular, Menlo,
      Consolas, monospace;
    font-size: 0.75rem;
    color: var(--foreground);
  }
</style>
</head>
<body>
  <main class="card">
    <div class="wordmark">LibreTimes</div>

    <div class="mark" aria-hidden="true">
      <svg viewBox="0 0 24 24">
        <circle cx="12" cy="12" r="10" />
{{ if .OK }}        <path d="M8 12.5l2.5 2.5L16 9.5" />
{{ else }}        <path d="M15 9l-6 6M9 9l6 6" />
{{ end }}      </svg>
    </div>

    <h1>{{ .Heading }}</h1>
    <p>{{ .Body }}</p>

{{ with .Code }}    <div class="code">{{ . }}</div>
{{ end }}
    <p class="hint">
      This tab is served by <kbd>lt</kbd> on this machine, and can be closed.
    </p>
  </main>
</body>
</html>
`))

// callbackView is the content the template renders.
type callbackView struct {
	OK      bool
	Title   string
	Heading string
	Body    string
	// Code is the OAuth error slug, shown only on the failure page. The
	// terminal carries the authoritative message; this is here so the person
	// looking at the browser is not told to go read an error without being
	// given any hint of which one.
	Code string
}

// viewFor builds the page's content from the redirect's query parameters.
//
// Success is "there is a code" — the same test the flow itself makes — rather
// than "there is no error". A redirect carrying neither is a failure, and one
// that rendered as success would send the user back to a terminal still
// waiting for something that is never coming.
func viewFor(params url.Values) callbackView {
	if params.Get("code") != "" {
		return callbackView{
			OK:      true,
			Title:   "Signed in",
			Heading: "Signed in to LibreTimes",
			Body:    "You can close this tab and return to your terminal.",
		}
	}

	view := callbackView{
		Title:   "Sign-in failed",
		Heading: "Sign-in failed",
		Body:    "Return to your terminal for the error and what to do about it.",
		Code:    truncate(strings.TrimSpace(params.Get("error")), 120),
	}
	if view.Code == "" {
		// Neither a code nor an error is malformed rather than declined, and
		// saying so beats an empty box.
		view.Code = "the redirect carried no authorization code"
	}
	return view
}

// truncate bounds what the page will echo. The slug is a short OAuth error
// code; anything longer is not one, and a page that renders an arbitrarily
// long attacker-supplied string is a defacement surface even when it is
// escaped.
func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}
