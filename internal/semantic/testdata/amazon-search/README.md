# amazon-search

`webshadow cluster -hosts amazon.com` output for five recordings made on
2026-10-11 with the request/response-only recorder (no browser events):
Adi's `rec_20261011_001` (3 searches, 5 product pages) and the scripted
sessions `rec_20261011_002` to `rec_20261011_005` (homepage, typed searches
with autocomplete, the first result of each). 1,620 amazon.com exchanges
(3,642 third-party ones skipped), 83 request families (27 static), 972
value links, 39 page views, 3,844 sequence edges.

The files went through `webshadow analyze redact -gz` before they were
committed: every cookie, Set-Cookie, credential header, JWT and email
address (also URL-encoded) is a `{{redacted:<location>:<hash>}}` tag. Equal
values share a tag, so value links and flows still join.
`TestFixtureHoldsNoSecrets` checks that loading it finds nothing left to
redact.

`expected-ir.json` is the benchmark's ground truth for this result: the
roles of 49 families, the product, parent product, suggestion and search
scope entities, the search, product page, product API and suggestion
operations with their required inputs and outputs, and which prerequisites
on those operations are real. It labels only what a reviewer is sure of;
anything it leaves out is not scored. `make analyze-bench MODEL="qwen3-1.7b
qwen3-4b"` scores models against it.
