package har

import (
	"path"
	"strings"
)

// Class is the benchmark's coarse category for an entry.
type Class string

const (
	ClassDocument   Class = "document"
	ClassAPI        Class = "api"
	ClassScript     Class = "script"
	ClassStylesheet Class = "stylesheet"
	ClassMedia      Class = "media"
	ClassFont       Class = "font"
	ClassTelemetry  Class = "telemetry"
	ClassUnknown    Class = "unknown"
)

// Classes lists every class, in display order.
var Classes = []Class{ClassDocument, ClassAPI, ClassScript, ClassStylesheet, ClassMedia, ClassFont, ClassTelemetry, ClassUnknown}

// ParseClass returns the class with the given name.
func ParseClass(s string) (Class, bool) {
	for _, c := range Classes {
		if string(c) == s {
			return c, true
		}
	}
	return "", false
}

// telemetryHosts are analytics and monitoring endpoints. A host matches if
// it equals an entry or is a subdomain of it.
var telemetryHosts = []string{
	"google-analytics.com",
	"analytics.google.com",
	"googletagmanager.com",
	"doubleclick.net",
	"googlesyndication.com",
	"nr-data.net",
	"hotjar.com",
	"segment.io",
	"segment.com",
	"mixpanel.com",
	"amplitude.com",
	"facebook.net",
	"clarity.ms",
	"sentry.io",
	"trc-events.taboola.com",
	"il-trc-events.taboola.com",
	"beacon.taboola.com",
	"cds.taboola.com",
	"jnn-pa.googleapis.com",
	"csp.withgoogle.com",
}

// telemetryPaths are path fragments that mark beacons on any host.
var telemetryPaths = []string{"/collect", "/cdn-cgi/rum", "/beacon", "/pixel", "/track", "/recaptcha"}

// Classify assigns a class from, in order: known telemetry hosts and
// paths, Chrome's _resourceType, the response MIME type, and finally the
// URL's file extension.
func Classify(e *Entry) Class {
	if isTelemetry(e) {
		return ClassTelemetry
	}
	switch e.ResourceType {
	case "document":
		return ClassDocument
	case "xhr", "fetch":
		return ClassAPI
	case "script":
		return ClassScript
	case "stylesheet":
		return ClassStylesheet
	case "image", "media":
		return ClassMedia
	case "font":
		return ClassFont
	case "ping", "beacon", "csp_violation_report":
		return ClassTelemetry
	}
	if c := classifyMIME(e.ResponseMIME); c != ClassUnknown {
		return c
	}
	return classifyExtension(e.Path)
}

func isTelemetry(e *Entry) bool {
	host := e.Host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	for _, h := range telemetryHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	p := strings.ToLower(e.Path)
	for _, frag := range telemetryPaths {
		if p == frag || strings.HasPrefix(p, frag+"/") || strings.HasSuffix(p, frag) || strings.Contains(p, frag+"/") {
			return true
		}
	}
	return false
}

func classifyMIME(mime string) Class {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	switch {
	case mime == "":
		return ClassUnknown
	case mime == "text/html" || mime == "application/xhtml+xml":
		return ClassDocument
	case strings.Contains(mime, "json") || strings.HasSuffix(mime, "+xml") || mime == "application/xml" || mime == "text/xml":
		return ClassAPI
	case strings.Contains(mime, "javascript") || strings.Contains(mime, "ecmascript"):
		return ClassScript
	case mime == "text/css":
		return ClassStylesheet
	case strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/") || strings.HasPrefix(mime, "audio/"):
		return ClassMedia
	case strings.HasPrefix(mime, "font/") || strings.Contains(mime, "font"):
		return ClassFont
	}
	return ClassUnknown
}

func classifyExtension(p string) Class {
	switch strings.ToLower(path.Ext(p)) {
	case ".html", ".htm":
		return ClassDocument
	case ".json":
		return ClassAPI
	case ".js", ".mjs":
		return ClassScript
	case ".css":
		return ClassStylesheet
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico", ".avif", ".mp4", ".webm", ".mp3":
		return ClassMedia
	case ".woff", ".woff2", ".ttf", ".otf", ".eot":
		return ClassFont
	}
	return ClassUnknown
}
