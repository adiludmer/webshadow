// Command verify-amazon checks a recording of the scripted Amazon session
// (scripts/amazon-session.py) and its clustering: the capture is complete
// and intact, the searches and the product page returned real pages, the
// Sec-Fetch headers are there, and clustering filtered to Amazon's own
// hosts finds the page views a backend would see. It prints one line per
// check and exits 1 if any fails. It prints no header, cookie or body
// values.
//
//	go run ./scripts/verify-amazon <recording dir>
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/adiludmer/webshadow/internal/cluster"
	"github.com/adiludmer/webshadow/internal/cluster/model"
	"github.com/adiludmer/webshadow/internal/cluster/normalize"
	"github.com/adiludmer/webshadow/internal/recording"
)

var failed bool

func check(ok bool, format string, args ...any) {
	mark := "ok  "
	if !ok {
		mark, failed = "FAIL", true
	}
	fmt.Printf("%s %s\n", mark, fmt.Sprintf(format, args...))
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: verify-amazon <recording dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	rec, err := recording.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	checkRecording(dir, rec)
	checkClustering(rec)
	if failed {
		os.Exit(1)
	}
}

var asin = regexp.MustCompile(`data-asin="([A-Z0-9]{10})"`)

func checkRecording(dir string, rec *recording.Recording) {
	check(rec.Session.Status == "complete" && !rec.Truncated, "session %s is %s, truncated=%v", rec.Session.ID, rec.Session.Status, rec.Truncated)
	_, err := os.Stat(filepath.Join(dir, "browser.jsonl"))
	check(err != nil, "no browser.jsonl written")

	var incomplete, badBodies, bodies, secFetch int
	var searches, products []string
	checkpoints := 0
	for i := range rec.Exchanges {
		ex := &rec.Exchanges[i]
		if ex.Request.Method == "" || ex.Request.URL == "" || ex.Response == nil || ex.Response.Status == 0 || ex.Timing.RequestStart == 0 {
			incomplete++
		}
		for _, h := range ex.Request.Headers {
			if strings.EqualFold(h.Name, "Sec-Fetch-Dest") {
				secFetch++
				break
			}
		}
		for _, b := range []*recording.Body{ex.Request.Body, responseBody(ex)} {
			if b == nil {
				continue
			}
			bodies++
			data, err := os.ReadFile(filepath.Join(dir, b.Ref))
			sum := sha256.Sum256(data)
			if err != nil || hex.EncodeToString(sum[:]) != b.SHA256 || int64(len(data)) != b.Size {
				badBodies++
			}
		}
		if ex.Request.Method != "GET" || !ownHost(ex.Request.Host) || ex.Response == nil {
			continue
		}
		path := ex.Request.URL
		if i := strings.Index(path, ex.Request.Host); i >= 0 {
			path = path[i+len(ex.Request.Host):]
		}
		switch {
		case isSearch(path) && isDocument(ex):
			searches = append(searches, describeSearch(rec, ex))
		case strings.HasPrefix(path, "/checkpoint/") && isDocument(ex):
			checkpoints++
		case strings.Contains(path, "/dp/") && isDocument(ex):
			products = append(products, fmt.Sprintf("%d", ex.Response.Status))
		}
	}
	n := len(rec.Exchanges)
	check(n > 0 && incomplete == 0, "%d exchanges, %d missing method, url, status or request_start", n, incomplete)
	check(badBodies == 0, "%d bodies, %d missing or not matching their sha256", bodies, badBodies)
	good := 0
	for _, s := range searches {
		if strings.HasPrefix(s, "200 ") && !strings.Contains(s, "captcha") {
			if hasProducts(s) {
				good++
			}
		}
	}
	check(good >= 2, "search pages: %s (want 2 with 200, >=10 products, no captcha)", strings.Join(searches, "; "))
	check(contains(products, "200"), "product pages: %v (want a 200)", products)
	check(checkpoints == 0, "%d bot checkpoint pages", checkpoints)
	pct := 100 * float64(secFetch) / float64(max(n, 1))
	check(pct >= 99, "Sec-Fetch-Dest on %d of %d exchanges (%.1f%%)", secFetch, n, pct)
}

func hasProducts(s string) bool {
	var status, products int
	fmt.Sscanf(s, "%d with %d products", &status, &products)
	return products >= 10
}

func describeSearch(rec *recording.Recording, ex *recording.HTTPExchange) string {
	body, err := normalize.ReadBody(rec, ex.Response.Body)
	if err != nil || ex.Response.Body == nil {
		return fmt.Sprintf("%d with no readable body", ex.Response.Status)
	}
	ids := map[string]bool{}
	for _, m := range asin.FindAllSubmatch(body, -1) {
		ids[string(m[1])] = true
	}
	s := fmt.Sprintf("%d with %d products", ex.Response.Status, len(ids))
	low := strings.ToLower(string(body))
	if strings.Contains(low, "captcha") || strings.Contains(low, "robot check") {
		s += " captcha"
	}
	return s
}

func checkClustering(rec *recording.Recording) {
	opts := cluster.DefaultOptions()
	opts.Hosts = []string{"amazon.com"}
	res, err := cluster.Run([]*recording.Recording{rec}, opts)
	if err != nil {
		check(false, "cluster: %v", err)
		return
	}
	in := res.Manifest.Inputs[0]
	fmt.Printf("info clustered %d exchanges on amazon.com hosts, skipped %d on other hosts\n", in.Exchanges, in.Skipped)
	navURL := map[string]string{}
	for _, tr := range res.Traces {
		for _, n := range tr.Navigations {
			navURL[n.ID] = n.URL
		}
	}
	var views []string
	var home, product bool
	userSearches, searches := 0, 0
	for _, ep := range res.Episodes {
		if ep.Trigger == nil {
			views = append(views, "untriggered")
			continue
		}
		// A page view is named by its document's URL after redirects.
		u := navURL[ep.NavigationID]
		if u == "" {
			u = ep.Trigger.URL
		}
		path := pathOf(u)
		views = append(views, fmt.Sprintf("%s %s (%d requests)", ep.Trigger.Kind, shorten(path), len(ep.ExchangeIDs)))
		home = home || path == "/"
		product = product || strings.Contains(path, "/dp/")
		if isSearch(path) {
			searches++
			if ep.Trigger.Kind == model.TriggerUser {
				userSearches++
			}
		}
	}
	fmt.Printf("info page views:\n")
	for _, v := range views {
		fmt.Printf("       %s\n", v)
	}
	check(len(views) >= 4 && home && product && searches >= 2, "%d page views: home=%v product=%v searches=%d (want >=4 with home, product and 2 searches)", len(views), home, product, searches)
	check(searches >= 2 && userSearches == searches, "%d of %d search page views triggered as %s", userSearches, searches, model.TriggerUser)
	// Amazon's search box submits /s/ref=...?field-keywords=; links and
	// later searches use /s?k=.
	var sFamilies, withK int
	for _, f := range res.Families {
		if f.Method == "GET" && isSearch(f.PathTemplate+"?") && !f.Static {
			sFamilies++
			for _, s := range f.Slots {
				if s.Location == "query.k" || s.Location == "query.field-keywords" {
					withK++
					break
				}
			}
		}
	}
	check(sFamilies > 0 && withK == sFamilies, "%d GET search families, %d with a k or field-keywords slot", sFamilies, withK)
}

func responseBody(ex *recording.HTTPExchange) *recording.Body {
	if ex.Response == nil {
		return nil
	}
	return ex.Response.Body
}

func isDocument(ex *recording.HTTPExchange) bool {
	for _, h := range ex.Request.Headers {
		if strings.EqualFold(h.Name, "Sec-Fetch-Dest") {
			return h.Value == "document"
		}
	}
	return false
}

func isSearch(path string) bool {
	return strings.HasPrefix(path, "/s?") || strings.HasPrefix(path, "/s/")
}

func ownHost(host string) bool { return host == "amazon.com" || strings.HasSuffix(host, ".amazon.com") }

func pathOf(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
		if j := strings.Index(u, "/"); j >= 0 {
			return u[j:]
		}
		return "/"
	}
	return u
}

// shorten keeps a path readable and drops query values, which can carry
// session state.
func shorten(p string) string {
	path, query, ok := strings.Cut(p, "?")
	if len(path) > 40 {
		path = path[:40] + "..."
	}
	if !ok {
		return path
	}
	var names []string
	for _, kv := range strings.Split(query, "&") {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	return path + "?" + strings.Join(names, ",")
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
