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

### Models

`models.yaml` lists the models the benchmark can run as generators or
readers. Each entry picks an adapter:

- `openai_compatible` calls any OpenAI-format `/chat/completions` endpoint
  (OpenAI, OpenRouter, vLLM, and others). The API key comes from the
  environment variable named in `api_key_env`; keys never go in the file.
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
