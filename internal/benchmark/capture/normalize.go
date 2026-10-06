package capture

import (
	"net/url"
	"sort"
	"strings"
)

// DefaultVolatileKeys are query keys that only bust caches or carry
// timestamps, so they are dropped when comparing or displaying URLs.
var DefaultVolatileKeys = []string{"_", "_t", "cb", "cachebust", "cachebuster", "nocache", "rnd", "rand", "random", "timestamp", "ts"}

// NormalizeURL returns a stable form of rawURL: lowercase scheme and host,
// default ports removed, an empty path as "/", volatile query keys dropped
// and the remaining query sorted by key. It returns rawURL unchanged if it
// does not parse.
func NormalizeURL(rawURL string, volatile []string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if (u.Scheme == "https" && strings.HasSuffix(host, ":443")) || (u.Scheme == "http" && strings.HasSuffix(host, ":80")) {
		host = host[:strings.LastIndex(host, ":")]
	}
	u.Host = host
	u.User = nil
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	q := u.Query()
	for _, k := range volatile {
		q.Del(k)
	}
	u.RawQuery = encodeSorted(q)
	return u.String()
}

// encodeSorted encodes values sorted by key, keeping each key's values in
// their original order so duplicate keys stay meaningful.
func encodeSorted(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range q[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
		}
	}
	return b.String()
}
