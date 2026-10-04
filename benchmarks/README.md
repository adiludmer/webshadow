# Benchmark scenarios

Each subdirectory is one scenario. A run has two agents: a generator reads the
scenario's sanitized HAR and writes a Markdown `shadow/` tree, then a reader
gets only `goal.md` and that tree and returns a JSON answer, which is scored
against `expected.json`.

```
<scenario-id>/
  scenario.yaml   benchmark mechanics only (no prompts or model settings)
  goal.md         the task, in natural language (never shown to the generator)
  expected.json   the structured answer the reader must return
  session.har     sanitized HAR (never commit raw captures)
```

## scenario.yaml

```yaml
id: synthetic-laptop-search      # lowercase, unique across the suite
name: Find the cheapest qualifying laptop
version: 1
tags: [ecommerce, search]

har: session.har                 # these three default to the names shown
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

Normalizer options: `case` (`lower` or `upper`), `whitespace` (`trim` or
`collapse`), `numeric_tolerance`, `date` (a Go time layout), `url`, `currency`
and `unordered` (compare arrays as multisets).

## Adding a real capture

Record the session in Chrome DevTools (Network tab, "Export HAR"), then
sanitize it before it goes anywhere near the repo. Raw captures stay out of
git (`*.raw.har` is ignored).

```
go run ./cmd/webshadow bench sanitize capture.raw.har --out session.har \
  --drop media,font,telemetry,stylesheet,script --trim-initiators
go run ./cmd/webshadow bench inspect-har session.har --names
```

`sanitize` redacts cookies, auth and CSRF headers, API keys, session and
tracking IDs in URLs, form bodies and JSON bodies. `inspect-har --names` lists
every header and parameter name that is left, so you can spot anything
site-specific before committing. `validate` refuses a HAR that still carries
cookies or auth headers.

Check the suite with:

```
go run ./cmd/webshadow bench validate benchmarks/
```
