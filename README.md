# webshadow
Agent-friendly web generator

## Capture benchmark

`webshadow bench` measures how well an agent turns a recorded browsing session
(a Burp Suite proxy capture) into a shadow that another agent can use to complete a task. See
[benchmarks/README.md](benchmarks/README.md) for the scenario format.

```
go run ./cmd/webshadow bench validate benchmarks/
go test ./...
```

### Running a benchmark with make

`make bench` runs one scenario end to end: it builds the llama binary,
downloads the model if it is missing, generates a shadow tree and answers
the goal from it.

```
make bench                                        # qwen2.5-7b on the laptop search scenario
make bench MODEL=llama3.1-8b SCENARIO=benchmarks/<dir>
make bench GENERATOR=qwen2.5-7b READER=qwen2.5-3b REPETITIONS=3
make bench-models                                 # model ids in models.yaml
make bench-download MODEL=qwen2.5-3b              # fetch a model without running
```

Models are stored as `models/<id>.gguf` (set `MODELS_DIR` to keep them
elsewhere). `qwen2.5-7b`, `qwen2.5-3b` and `llama3.1-8b` download Q4_K_M
GGUFs from Hugging Face; for another model, add an entry to `models.yaml`
and put its file at `models/<id>.gguf`. The tree and the scored record land
under `runs/<RUN_ID>/`.

### Generating a shadow tree

`bench generate` runs the generator stage. First, without a model, every
HTML page in the scenario's capture is converted to a Markdown document
(`internal/markdown`): headings, paragraphs, lists, tables, quotes, code and
links, from `<main>` when the page has one. Each document starts with a YAML
header holding its `url`, `title`, `description`, `captured` time, `tags`
(the site's own: `article:tag`, meta keywords and tag links) and
`keywords` (TF-IDF across the capture's pages, with Hebrew prefix folding).

The model then decides only the layout. It lists the documents with
`documents`, reads one with `document`, puts it at a path with `place`, and
writes its own overview files, such as `index.md`, with `write`.
`capture_index` and `capture_entry` show API and JSON responses, whose facts
it writes into files. It never sees the goal. When it finishes, documents it
did not place are filed under `pages/`, and links between captured pages
are rewritten to relative tree paths.

```
WEBSHADOW_LLAMA_MODEL=~/models/model.gguf \
  bin/webshadow bench generate benchmarks/geektime-burp-browse --generator local-llama
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
  bin/webshadow bench answer benchmarks/synthetic-laptop-search --tree path/to/shadow --reader local-llama
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
