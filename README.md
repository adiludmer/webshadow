# webshadow
Agent-friendly web generator

## HAR benchmark

`webshadow bench` measures how well an agent turns a recorded browser session
(HAR) into a shadow that another agent can use to complete a task. See
[benchmarks/README.md](benchmarks/README.md) for the scenario format.

```
go run ./cmd/webshadow bench validate benchmarks/
go test ./...
```

### Generating a shadow tree

`bench generate` runs the generator stage: a model reads the scenario's
sanitized HAR through paged `har_index` and `har_entry` tools and writes
Markdown files with a sandboxed `write` tool. It never sees the goal.

```
WEBSHADOW_LLAMA_MODEL=~/models/model.gguf \
  bin/webshadow bench generate benchmarks/geektime-enso-funding --generator local-llama
```

The tree lands in `runs/<run id>/trees/<scenario>/<generator>/tree-NN/`,
with its files made read-only, and `tree-NN.json` next to it records the
digest, file count, status (`generated` or `generation_failed`) and the
generator's full transcript. Writes are limited to `.md` files inside the
tree, 64 KB per file, 500 files and 8 MB in total.

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

Pass a tree from `bench generate` and the record names its generator; a
tree edited since generation is refused. Each repetition writes a record, with the answer, the per-field score, token
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
