package capture

import (
	"strings"
	"testing"
)

var articlePage = `<!DOCTYPE html><html lang="he"><head>
<title>Enso raises $15M | Geektime</title>
<meta name="description" content="The startup closed a Series A.">
<script>var tracker = "noise";</script><style>body{color:red}</style>
</head><body>
<nav><a href="/">Home</a> <a href="/funding/">Funding</a></nav>
<main>
<h1>Enso raises <b>$15M</b></h1>
<p>Enso, which builds   security tools, raised
$15 million in a <a href="/tag/series-a/">Series A</a> round led by <a href="https://vc.test/">VC Fund</a>.</p>
<p>The same line.</p><p>The same line.</p>
<ul><li>Founded 2019</li><li>Tel Aviv</li></ul>
<table><tr><th>Round</th><th>Amount</th></tr><tr><td>A</td><td>$15M</td></tr></table>
<div hidden>secret hidden text</div><div style="display: none">also hidden</div>
<svg><text>icon</text></svg><button>Subscribe</button>
<a href="/related/"><h3>Related story</h3><span>teaser</span></a>
<a href="javascript:void(0)">menu</a> <a href="#top">top</a>
<p>` + strings.Repeat("filler ", 80) + `</p>
</main>
<footer>Copyright footer</footer>
</body></html>`

func TestHTMLText(t *testing.T) {
	got := HTMLText([]byte(articlePage), "https://www.geektime.co.il/enso/")
	for _, want := range []string{
		"# Enso raises $15M | Geektime\n\nThe startup closed a Series A.",
		"# Enso raises $15M\n",
		"Enso, which builds security tools, raised $15 million in a [Series A](https://www.geektime.co.il/tag/series-a/) round led by [VC Fund](https://vc.test/).",
		"- Founded 2019\n",
		"| Round | Amount |\n| A | $15M |",
		"### Related story",
		"(link: https://www.geektime.co.il/related/)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"tracker", "color:red", "secret", "also hidden", "icon", "Subscribe", "javascript", "#top", "Copyright", "Funding"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("rendered %q:\n%s", unwanted, got)
		}
	}
	if strings.Count(got, "The same line.") != 1 {
		t.Errorf("repeated line kept:\n%s", got)
	}
}

func TestHTMLTextFallsBackToBody(t *testing.T) {
	got := HTMLText([]byte(`<html><body><nav>Menu</nav><main><p>tiny</p></main><p>Body text</p></body></html>`), "https://a.test/")
	if !strings.Contains(got, "Menu") || !strings.Contains(got, "Body text") {
		t.Errorf("short <main> should fall back to <body>:\n%s", got)
	}
}

func TestParseRendersHTMLResponses(t *testing.T) {
	trace := parseFixture(t)
	var html, other int
	for _, e := range trace.Entries {
		if e.ResponseText != "" {
			html++
			if !IsHTML(e.ResponseMIME) {
				t.Errorf("seq %d: text for %s", e.Sequence, e.ResponseMIME)
			}
			if string(e.Readable()) != e.ResponseText {
				t.Errorf("seq %d: Readable is not the text", e.Sequence)
			}
		} else if len(e.ResponseBody) > 0 {
			other++
			if string(e.Readable()) != string(e.ResponseBody) {
				t.Errorf("seq %d: Readable is not the body", e.Sequence)
			}
		}
	}
	if html == 0 || other == 0 {
		t.Errorf("fixture should have HTML and other responses: %d html, %d other", html, other)
	}
}
