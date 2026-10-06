# webshadow
Agent-friendly web generator

## Recording a browser session

`webshadow browser` launches an isolated Chromium that sends all its
traffic through a local recording proxy, and records the session until you
press Ctrl-C or close the browser:

```
make release                     # bin/webshadow with Chromium embedded
bin/webshadow browser https://www.amazon.com/
```

HTTPS is decrypted with a local Webshadow CA (`~/.webshadow/ca/`). Only the
recording browser trusts it, through `--ignore-certificate-errors-spki-list`;
the system trust store is never changed, and the proxy still verifies every
site's real certificate. Each session gets a fresh browser profile, deleted
when it stops (`-keep-profile` keeps it).

A recording is a directory under `~/.webshadow/recordings/<session>/`:
`session.json`, `http.jsonl` (one request/response per line),
`browser.jsonl` and `bodies/`. Recordings hold cookies, tokens and
form data, so they are readable only by you and never leave the machine.

```
webshadow recordings list
webshadow recordings show <id>
webshadow recordings timeline <id>
webshadow recordings delete <id>
```

`make release` embeds the open-source Chromium snapshot pinned in
`internal/chromium/chromium.lock` for linux/amd64, darwin/amd64,
darwin/arm64 or windows/amd64, and unpacks it to
`~/.webshadow/runtime/chromium/` on first run. A plain `make build` has no
embedded Chromium; it uses `-chromium PATH`, `WEBSHADOW_CHROMIUM`, or a
Chromium or Chrome found on the system. Set `WEBSHADOW_HOME` to keep
everything somewhere other than `~/.webshadow`.

## HAR benchmark

`webshadow bench` measures how well an agent turns a recorded browser session
(HAR) into a shadow that another agent can use to complete a task. See
[benchmarks/README.md](benchmarks/README.md) for the scenario format.

```
go run ./cmd/webshadow bench validate benchmarks/
go test ./...
```

### Answering from a shadow tree

`bench answer` runs the reader stage: a model gets the scenario's goal and
a shadow tree, explores the tree with read-only `list`, `read` and `search`
tools, and finishes with a JSON answer that is scored against
`expected.json`.

```
make llama
WEBSHADOW_LLAMA_MODEL=~/models/model.gguf \
  bin/webshadow bench answer benchmarks/geektime-enso-funding --tree path/to/shadow --reader local-llama
```

Each repetition writes a record, with the answer, the per-field score, token
usage and the full transcript, to
`runs/<run id>/cases/<scenario>/<generator>/<reader>/repetition-NN.json`.
Its status is `success`, `wrong_answer`, `invalid_output`, `step_limit`,
`token_limit`, `timeout` or `error`.

### Models

`models.yaml` lists the models the benchmark can run as generators or
readers. Each entry picks an adapter:

- `llama` runs a local GGUF model in-process through llama.cpp, which is
  linked into the binary. No model server is needed.
- `fake` replies with a fixed script, for tests.

The `llama` adapter needs llama.cpp built from the pinned submodule
(`third_party/llama.cpp`), which takes CMake and a C++ compiler:

```
make llama                # bin/webshadow with llama.cpp linked in statically
make test-llama           # all tests against the embedded llama.cpp
WEBSHADOW_LLAMA_MODEL=~/models/qwen2.5-0.5b-instruct-q4_k_m.gguf make test-llama
```

The tests use a tiny generated model, so they run without downloading
anything. Set `WEBSHADOW_LLAMA_MODEL` to a real GGUF file to also run the
real-model test. For GPU backends, pass CMake flags through
`LLAMA_CMAKE_FLAGS`, for example `LLAMA_CMAKE_FLAGS=-DGGML_METAL=ON` or
`-DGGML_CUDA=ON`. A plain `go build` still works without llama.cpp; its
binary reports that it was built without llama.cpp if a `llama` model is
opened.
