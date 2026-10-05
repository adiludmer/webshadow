package markdown

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// linkTarget matches the URL part of a Markdown link, which is not text a
// reader sees and would otherwise feed URL slugs into the keywords.
var linkTarget = regexp.MustCompile(`\]\([^)]*\)`)

// Tokens splits Markdown text into lowercase words for keyword scoring:
// runs of letters and digits of at least two runes, without stopwords or
// bare numbers. Link targets are ignored.
func Tokens(text string) []string {
	text = linkTarget.ReplaceAllString(text, "]")
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := words[:0]
	for _, w := range words {
		if utf8.RuneCountInString(w) < 2 || stopwords[w] || isNumber(w) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func isNumber(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// Keywords picks up to k keywords for each document in a corpus by TF-IDF:
// a word scores by how often it appears in the document, weighted by
// log(N/df), so a word every document shares, such as site navigation,
// scores zero. A word must appear at least twice in its document. With a
// single document, words are ranked by frequency alone. A Hebrew word with
// a one-letter prefix (ה, ו, ב, ל, מ, ש, כ) counts as the bare word when
// the bare word also appears in the corpus. Ties break
// alphabetically, so the result is deterministic.
func Keywords(docs [][]string, k int) [][]string {
	docs = foldPrefixes(docs)
	df := map[string]int{}
	for _, toks := range docs {
		seen := map[string]bool{}
		for _, t := range toks {
			if !seen[t] {
				seen[t] = true
				df[t]++
			}
		}
	}
	n := float64(len(docs))
	out := make([][]string, len(docs))
	for i, toks := range docs {
		if len(toks) == 0 {
			continue
		}
		tf := map[string]int{}
		for _, t := range toks {
			tf[t]++
		}
		type scored struct {
			word  string
			score float64
		}
		var ws []scored
		for w, c := range tf {
			if c < 2 {
				continue
			}
			idf := 1.0
			if len(docs) > 1 {
				idf = math.Log(n / float64(df[w]))
			}
			if s := float64(c) / float64(len(toks)) * idf; s > 0 {
				ws = append(ws, scored{w, s})
			}
		}
		sort.Slice(ws, func(a, b int) bool {
			if ws[a].score != ws[b].score {
				return ws[a].score > ws[b].score
			}
			return ws[a].word < ws[b].word
		})
		for _, w := range ws[:min(k, len(ws))] {
			out[i] = append(out[i], w.word)
		}
	}
	return out
}

// hebrewPrefixes are the one-letter prefixes Hebrew attaches to words.
const hebrewPrefixes = "הובלמשכ"

// minFoldedRunes is the shortest bare word a prefixed word folds into, so
// short words are not stripped into other words.
const minFoldedRunes = 3

func foldPrefixes(docs [][]string) [][]string {
	vocab := map[string]bool{}
	for _, toks := range docs {
		for _, t := range toks {
			vocab[t] = true
		}
	}
	out := make([][]string, len(docs))
	for i, toks := range docs {
		out[i] = make([]string, len(toks))
		for j, t := range toks {
			r, size := utf8.DecodeRuneInString(t)
			if bare := t[size:]; strings.ContainsRune(hebrewPrefixes, r) && vocab[bare] && utf8.RuneCountInString(bare) >= minFoldedRunes {
				t = bare
			}
			out[i][j] = t
		}
	}
	return out
}

// stopwords are common English and Hebrew function words.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		a about above after again against all also am an and any are as at be
		because been before being below between both but by can could did do
		does doing down during each few for from further had has have having he
		her here hers herself him himself his how if in into is it its itself
		just me more most my myself no nor not now of off on once only or other
		our ours out over own same she should so some such than that the their
		theirs them themselves then there these they this those through to too
		under until up very was we were what when where which while who whom
		why will with would you your yours yourself yourselves one two new get
		got like may might must said says say us via per www http https com
		של את על עם זה זו זאת הוא היא הם הן אני אתה את אנחנו אתם לא כי גם אבל
		או אם יותר כל אחד אחת היה הייתה היו יש אין מה מי כמו אחרי לפני בין עד
		רק עוד כבר לו לה להם להן שלו שלה שלהם שלהן אלה אלו כך כן אז כאשר מאוד
		הזה הזאת האלה שהוא שהיא שהם לפי ידי בו בה בהם בהן עליו עליה עליהם אותו
		אותה אותם אותן מול תוך דרך פי כדי בגלל למרות אף שם פה כאן היום אמר
		אומרת אומר אמרה וגם וכן ולא ואת וזה שלא שזה שיש מאז בכל לכל מכל הכל
		כמה איך למה מתי איפה היכן אצל ללא בלי תחת מעל ליד אולי עדיין כלל בעיקר
		הרבה פחות כאלה כזה כזו כמעט אותי אותך לי לך לנו לכם שלי שלך שלנו שלכם
		בזמן בשנה בשנים בעוד הייתי יהיה תהיה להיות ישנו ישנה בתוך
	`) {
		m[w] = true
	}
	return m
}()
