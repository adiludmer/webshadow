package route

import (
	"math/rand"
	"strings"
	"testing"
)

func TestStableWordsStayLiteralAndSiblingIDsBecomeASlot(t *testing.T) {
	r := Infer(paths("GET", "shop.test",
		"/products/search",
		"/products/search",
		"/products/B0ABC",
		"/products/B0XYZ",
		"/products/B0123",
	))
	cases := map[string]string{
		"/products/search": "/products/search",
		"/products/B0ABC":  "/products/{slot_1}",
		"/products/B0XYZ":  "/products/{slot_1}",
	}
	for path, want := range cases {
		if got := match(r, "GET", "shop.test", path).Display(); got != want {
			t.Errorf("%s matched %s, want %s", path, got, want)
		}
	}
	if got := match(r, "GET", "shop.test", "/products/B0ABC").Canonical(); got != "/products/{id}" {
		t.Errorf("canonical = %s, want /products/{id}", got)
	}
}

func TestTooFewSiblingsStayLiteral(t *testing.T) {
	// Two products could be a coincidence; the route stays literal until a
	// third value turns up, possibly in another recording.
	r := Infer(paths("GET", "shop.test", "/products/B0ABC", "/products/B0XYZ"))
	if got := match(r, "GET", "shop.test", "/products/B0ABC").Display(); got != "/products/B0ABC" {
		t.Errorf("matched %s, want the literal route", got)
	}
	r = Infer(paths("GET", "shop.test", "/products/B0ABC", "/products/B0XYZ", "/products/B0QRS"))
	if got := match(r, "GET", "shop.test", "/products/B0ABC").Display(); got != "/products/{slot_1}" {
		t.Errorf("with a third sibling matched %s, want a slot", got)
	}
}

func TestPlainWordsNeverBecomeSlots(t *testing.T) {
	r := Infer(paths("GET", "shop.test",
		"/deals", "/cart", "/help", "/account", "/orders", "/search", "/wishlist", "/gift",
	))
	for _, p := range []string{"/deals", "/cart", "/wishlist"} {
		if got := match(r, "GET", "shop.test", p).Display(); got != p {
			t.Errorf("%s matched %s, want it literal", p, got)
		}
	}
}

func TestPositionsThatVaryTogetherBothBecomeSlots(t *testing.T) {
	r := Infer(paths("GET", "shop.test",
		"/Instant-Pot-Duo-7-in-1/dp/B00FLYWNYQ",
		"/Ring-Video-Doorbell/dp/B08N5NQ869",
		"/Keychron-K2-Wireless/dp/B07QBPDWLS",
		"/gp/help",
	))
	got := match(r, "GET", "shop.test", "/Ring-Video-Doorbell/dp/B08N5NQ869")
	if got.Display() != "/{slot_1}/dp/{slot_2}" {
		t.Fatalf("matched %s, want /{slot_1}/dp/{slot_2}", got.Display())
	}
	if got.Canonical() != "/{slug}/dp/{id}" {
		t.Errorf("canonical = %s", got.Canonical())
	}
	if len(got.Positions) != 2 || got.Positions[0] != 0 || got.Positions[1] != 2 {
		t.Errorf("positions = %v, want [0 2]", got.Positions)
	}
	if got := match(r, "GET", "shop.test", "/gp/help").Display(); got != "/gp/help" {
		t.Errorf("/gp/help matched %s", got)
	}
}

func TestDifferentClassesDoNotShareASlot(t *testing.T) {
	r := Infer(paths("GET", "shop.test",
		"/items/101", "/items/102", "/items/103",
		"/items/B0A", "/items/B0B",
	))
	if got := match(r, "GET", "shop.test", "/items/101").Canonical(); got != "/items/{int}" {
		t.Errorf("int sibling = %s", got)
	}
	// Only two id-class values: not enough for a slot of their own.
	if got := match(r, "GET", "shop.test", "/items/B0A").Display(); got != "/items/B0A" {
		t.Errorf("id sibling = %s, want literal", got)
	}
}

func TestHexLookingIDsStayWithTheirSiblings(t *testing.T) {
	// B0AAA11111 uses only hex letters; it must still share a slot with
	// B0XYZ12345.
	r := Infer(paths("GET", "shop.test", "/dp/B0AAA11111", "/dp/B0XYZ12345", "/dp/B0QRS67890"))
	for _, p := range []string{"/dp/B0AAA11111", "/dp/B0XYZ12345"} {
		if got := match(r, "GET", "shop.test", p).Canonical(); got != "/dp/{id}" {
			t.Errorf("%s = %s, want /dp/{id}", p, got)
		}
	}
}

func TestHostsAndMethodsAreSeparate(t *testing.T) {
	var all []Path
	all = append(all, paths("GET", "a.test", "/p/101", "/p/102")...)
	all = append(all, paths("GET", "b.test", "/p/103")...)
	all = append(all, paths("POST", "a.test", "/p/104")...)
	r := Infer(all)
	if got := match(r, "GET", "a.test", "/p/101").Display(); got != "/p/101" {
		t.Errorf("values from other hosts or methods generalized a.test: %s", got)
	}
}

func TestInferenceIgnoresInputOrder(t *testing.T) {
	base := paths("GET", "shop.test",
		"/products/search", "/products/B0ABC", "/products/B0XYZ", "/products/B0123",
		"/Instant-Pot/dp/B00FLYWNYQ", "/Ring-Doorbell/dp/B08N5NQ869", "/Keychron-K2/dp/B07QBPDWLS",
		"/images/I/61xJcNKKLXL.js", "/images/I/01rGP6HIADL.js", "/images/I/21Yni.js", "/images/I/logo.png",
		"/", "/s", "/category/altshuler/",
	)
	want := renderAll(Infer(base), base)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		shuffled := append([]Path(nil), base...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		if got := renderAll(Infer(shuffled), base); got != want {
			t.Fatalf("permutation %d changed templates:\n%s\nwant:\n%s", i, got, want)
		}
	}
}

func TestFilesAreSiblingsOnlyWithinAnExtension(t *testing.T) {
	r := Infer(paths("GET", "cdn.test",
		"/images/I/61xJcNKKLXL.js", "/images/I/01rGP6HIADL.js", "/images/I/21Yni.js",
		"/images/I/logo.png", "/images/I/hero.png",
	))
	if got := match(r, "GET", "cdn.test", "/images/I/21Yni.js").Canonical(); got != "/images/I/{file:js}" {
		t.Errorf("js = %s", got)
	}
	if got := match(r, "GET", "cdn.test", "/images/I/logo.png").Display(); got != "/images/I/logo.png" {
		t.Errorf("png = %s, want literal", got)
	}
}

func TestUnseenPathFallsBackToLiterals(t *testing.T) {
	r := Infer(paths("GET", "shop.test", "/products/B0A1", "/products/B0B2", "/products/B0C3"))
	if got := match(r, "GET", "shop.test", "/products/B0Z9/reviews").Display(); got != "/products/{slot_1}/reviews" {
		t.Errorf("unseen = %s", got)
	}
	if got := match(r, "GET", "other.test", "/x/y").Display(); got != "/x/y" {
		t.Errorf("unknown host = %s", got)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"":                                     ClassEmpty,
		"12345":                                ClassInt,
		"550e8400-e29b-41d4-a716-446655440000": ClassUUID,
		"deadbeef01":                           ClassID,
		"B0FV975MJK":                           ClassID,
		"search":                               ClassWord,
		"Instant-Pot-Duo-7-in-1":               ClassSlug,
		"enso-ai-agents-growth-hacking":        ClassSlug,
		"61xJcNKKLXL.js":                       "file:js",
		"ref=sr_1_1":                           ClassOther,
	}
	for seg, want := range cases {
		if got := Classify(seg); got != want {
			t.Errorf("Classify(%q) = %s, want %s", seg, got, want)
		}
	}
}

func paths(method, host string, list ...string) []Path {
	var out []Path
	for _, p := range list {
		out = append(out, Path{Method: method, Host: host, Segments: segments(p)})
	}
	return out
}

func segments(p string) []string {
	if p == "/" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(p, "/"), "/")
}

func match(r *Router, method, host, path string) Template {
	return r.Match(method, host, segments(path))
}

func renderAll(r *Router, ps []Path) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString(r.Match(p.Method, p.Host, p.Segments).Canonical())
		b.WriteByte('\n')
	}
	return b.String()
}
