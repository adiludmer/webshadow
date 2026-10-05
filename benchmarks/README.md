# Benchmark scenarios

Each subdirectory is one scenario. A run has two agents: a generator reads the
scenario's sanitized Burp capture and writes a Markdown `shadow/` tree, then a reader
gets only `goal.md` and that tree and returns a JSON answer, which is scored
against `expected.json`.

```
<scenario-id>/
  scenario.yaml   benchmark mechanics only (no prompts or model settings)
  goal.md         the task, in natural language (never shown to the generator)
  expected.json   the structured answer the reader must return
  session.xml     sanitized Burp Suite XML export (never commit raw captures)
```

## scenario.yaml

```yaml
id: synthetic-laptop-search      # lowercase, unique across the suite
name: Find the cheapest qualifying laptop
version: 1
tags: [ecommerce, search]

capture: session.xml             # these three default to the names shown
goal: goal.md
expected: expected.json

evaluation:
  type: fields                   # registered scorer
  required_fields: [product_id, price, currency, in_stock]
  allow_extra_fields: true       # default true
  normalizers:                   # optional, per required field
    price: {currency: true, numeric_tolerance: 0.01}
    currency: {case: upper, whitespace: trim}

limits:                          # defaults: generate 200 steps / 600 s,
  generate:                      #           answer 20 steps / 90 s
    max_steps: 60
    timeout_seconds: 300
  answer:
    max_steps: 20
    timeout_seconds: 90
    max_tokens: 0                # optional token budget, 0 = none
```

A scenario with `evaluation: {type: none}` is generate-only: it needs no
`goal.md` or `expected.json`, `bench generate` runs as usual and `bench
answer` refuses it. Use it to watch the generator on a capture you have no
question for yet.

Normalizer options: `case` (`lower` or `upper`), `whitespace` (`trim` or
`collapse`), `numeric_tolerance`, `date` (a Go time layout), `url`, `currency`
and `unordered` (compare arrays as multisets).

## Adding a real capture

Captures come from the Burp Suite proxy, which keeps the raw request and
response of every item, including pages you navigated away from. Browse
the site through Burp, then in Proxy > HTTP history show all MIME types,
select the items, choose "Save items" and keep base64 encoding on. Sanitize
the export before it goes anywhere near the repo. Raw captures stay out of
git (`*.raw.xml` is ignored).

```
go run ./cmd/webshadow bench sanitize capture.raw.xml --out session.xml \
  --drop media,font,telemetry,stylesheet,script
go run ./cmd/webshadow bench inspect session.xml --names
```

`sanitize` decodes chunked and gzip or deflate bodies, then redacts cookies,
auth and CSRF headers, API keys, session and tracking IDs in URLs, form
bodies and JSON bodies. The output is still a Burp export, so Burp can load
it again. `inspect --names` lists every header and parameter name that is
left, so you can spot anything site-specific before committing. `validate`
refuses a capture that still carries cookies or auth headers.

Check the suite with:

```
go run ./cmd/webshadow bench validate benchmarks/
```
