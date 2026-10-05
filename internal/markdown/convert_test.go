package markdown

import (
	"strings"
	"testing"
)

var articlePage = `<!DOCTYPE html><html lang="he"><head>
<title>Enso raises $15M | Geektime</title>
<meta property="og:title" content="Enso raises $15M">
<meta name="description" content="The startup closed a Series A.">
<meta property="article:tag" content="Funding">
<meta name="keywords" content="security, funding, Series A">
<script>var tracker = "noise";</script><style>body{color:red}</style>
</head><body>
<nav><a href="/">Home</a> <a href="/tag/sidebar/">Sidebar tag</a></nav>
<main>
<h1>Enso raises <b>$15M</b></h1>
<p>Enso, which builds   <strong>security</strong> tools, raised
$15 million in a <a href="/tag/series-a/">Series A</a> round led by <a href="https://vc.test/">VC Fund</a>.</p>
<p>The same line.</p><p>The same line.</p>
<ul><li>Founded 2019</li><li>Tel Aviv</li></ul>
<ol start="3"><li>third</li><li>fourth</li></ol>
<table><tr><th>Round</th><th>Amount</th></tr><tr><td>A</td><td>$15M</td></tr></table>
<blockquote><p>We are <em>thrilled</em>.</p><p>Second line.</p></blockquote>
<pre>go run  ./cmd
  --flag</pre>
<p>Run <code>make  bench</code> to try it.</p>
<div hidden>secret hidden text</div><div style="display: none">also hidden</div>
<svg><text>icon</text></svg><button>Subscribe</button>
<a href="/related/"><img alt="photo"><h3>Related story</h3><span>teaser</span></a>
<a href="javascript:void(0)">menu</a> <a href="#top">top</a>
<p>` + strings.Repeat("filler ", 80) + `</p>
</main>
<footer>Copyright footer</footer>
</body></html>`

func TestConvert(t *testing.T) {
	p := Convert([]byte(articlePage), "https://www.geektime.co.il/enso/")
	if p.Title != "Enso raises $15M" || p.Description != "The startup closed a Series A." {
		t.Errorf("title %q, description %q", p.Title, p.Description)
	}
	if got := strings.Join(p.Tags, ","); got != "Funding,security,Series A" {
		t.Errorf("tags: %q", got)
	}
	for _, want := range []string{
		"# Enso raises **$15M**\n",
		"Enso, which builds **security** tools, raised $15 million in a [Series A](https://www.geektime.co.il/tag/series-a/) round led by [VC Fund](https://vc.test/).",
		"- Founded 2019\n- Tel Aviv\n",
		"3. third\n4. fourth\n",
		"| Round | Amount |\n| --- | --- |\n| A | $15M |\n",
		"> We are *thrilled*.\n>\n> Second line.",
		"```\ngo run  ./cmd\n  --flag\n```",
		"Run `make bench` to try it.",
		"### Related story",
		"[Related story](https://www.geektime.co.il/related/)",
		"[image: photo]",
	} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("missing %q in:\n%s", want, p.Body)
		}
	}
	for _, unwanted := range []string{"tracker", "color:red", "secret", "also hidden", "icon", "Subscribe", "javascript", "#top", "Copyright", "Sidebar"} {
		if strings.Contains(p.Body, unwanted) {
			t.Errorf("rendered %q:\n%s", unwanted, p.Body)
		}
	}
	if strings.Count(p.Body, "The same line.") != 1 {
		t.Errorf("repeated line kept:\n%s", p.Body)
	}
	if again := Convert([]byte(articlePage), "https://www.geektime.co.il/enso/"); again.Body != p.Body {
		t.Error("conversion is not deterministic")
	}
}

func TestConvertFallsBackToBody(t *testing.T) {
	p := Convert([]byte(`<html><head><title>T</title></head><body><nav>Menu</nav><main><p>tiny</p></main><p>Body text</p></body></html>`), "https://a.test/")
	if !strings.Contains(p.Body, "Menu") || !strings.Contains(p.Body, "Body text") || p.Title != "T" {
		t.Errorf("short <main> should fall back to <body>: %+v", p)
	}
}

func TestIsPage(t *testing.T) {
	if !IsPage([]byte("<!DOCTYPE html><html><body>x</body></html>")) || !IsPage([]byte("<TITLE>x</TITLE>")) {
		t.Error("whole pages not recognized")
	}
	if IsPage([]byte(`<div class="card">{{title}}</div>`)) {
		t.Error("template fragment taken for a page")
	}
}
