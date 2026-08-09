package cmd

import (
	"net/url"
	"strings"
)

// Embeddable URL handling mirrors vanilla Excalidraw 0.18.1 (element/embeddable.ts),
// which octo-web uses unmodified: the board supplies no custom `validateEmbeddable`
// prop, so the built-in `embeddableURLValidator` falls through to
// `matchHostname(url, ALLOWED_DOMAINS)`. Only a URL whose host is in this allowlist
// may become an embeddable element — every other URL is rejected and the Web blocks
// the insert. The CLI reproduces that gate exactly so an element it creates is one
// the Web board would accept and render as an iframe rather than silently drop.
//
// The 14-entry allowlist is copied verbatim from ALLOWED_DOMAINS. The `*.` entry is
// a first-subdomain wildcard, matched by matchHostname's rule below.
var embeddableAllowedDomains = map[string]bool{
	"youtube.com":         true,
	"youtu.be":            true,
	"vimeo.com":           true,
	"player.vimeo.com":    true,
	"figma.com":           true,
	"link.excalidraw.com": true,
	"gist.github.com":     true,
	"twitter.com":         true,
	"x.com":               true,
	"*.simplepdf.eu":      true,
	"stackblitz.com":      true,
	"val.town":            true,
	"giphy.com":           true,
	"reddit.com":          true,
}

// maxEmbeddableURLLen bounds the accepted URL so no unbounded string reaches the
// wire. Vanilla Excalidraw imposes no explicit length limit, but every real embed
// URL is far under this bound; the cap only rejects pathological input and never a
// legitimate provider link.
const maxEmbeddableURLLen = 2048

// validateEmbeddableURL applies Excalidraw's matchHostname rule to an embeddable
// URL and returns the (trimmed) URL when it is allowed. It parses the URL, requires
// an http/https scheme and a host, strips a leading "www.", and accepts the URL
// only when the bare host is an allowlist entry or, for a "*.rest" entry, when the
// host is exactly one subdomain deep under "rest" (matchHostname replaces the first
// label with "*" and looks that up). Anything else is rejected fail-closed.
func validateEmbeddableURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > maxEmbeddableURLLen {
		return "", false
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", false
	}
	// url.Hostname already lowercases the host; TrimPrefix mirrors matchHostname's
	// `hostname.replace(/^www\./, "")`.
	bare := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if embeddableAllowedDomains[bare] {
		return trimmed, true
	}
	if i := strings.IndexByte(bare, '.'); i >= 0 {
		// bareDomain.replace(/^([^.]+)/, "*") — wildcard only the first label.
		if embeddableAllowedDomains["*"+bare[i:]] {
			return trimmed, true
		}
	}
	return "", false
}

// embeddableIntrinsicSize mirrors getEmbedLink's per-provider intrinsicSize so a
// created embeddable defaults to the same footprint the Web assigns on insert. The
// sizes are keyed by host at the granularity Excalidraw uses; the YouTube-shorts
// portrait orientation (315x560) is not distinguished from landscape here because
// the CLI has no player context — an explicit --width/--height always overrides.
// Any host without a specific size (the allowlist's generic providers) uses the
// getEmbedLink fallback of 560x840.
func embeddableIntrinsicSize(raw string) (float64, float64) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return 560, 840
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	switch host {
	case "youtube.com", "youtu.be", "vimeo.com", "player.vimeo.com":
		return 560, 315
	case "figma.com":
		return 550, 550
	case "twitter.com", "x.com", "reddit.com":
		return 480, 480
	case "gist.github.com":
		return 550, 720
	default:
		return 560, 840
	}
}
