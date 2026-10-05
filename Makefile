# `make` builds a pure-Go webshadow. `make llama` builds one binary with
# llama.cpp linked in statically, so GGUF models run in-process through the
# llama adapter.

LLAMA_DIR   := third_party/llama.cpp
LLAMA_BUILD := $(LLAMA_DIR)/build
LLAMA_LIB   := $(LLAMA_BUILD)/src/libllama.a

# Extra CMake flags for GPU backends, e.g. -DGGML_METAL=ON or -DGGML_CUDA=ON.
LLAMA_CMAKE_FLAGS ?=

.PHONY: build llama llama-lib test test-llama clean-llama bench bench-models bench-download

build:
	go build -o bin/webshadow ./cmd/webshadow

llama: llama-lib
	CGO_ENABLED=1 go build -tags llama -o bin/webshadow ./cmd/webshadow

llama-lib: $(LLAMA_LIB)

$(LLAMA_LIB):
	@test -f $(LLAMA_DIR)/CMakeLists.txt || git submodule update --init --depth 1 $(LLAMA_DIR)
	cmake -S $(LLAMA_DIR) -B $(LLAMA_BUILD) -DCMAKE_BUILD_TYPE=Release \
		-DBUILD_SHARED_LIBS=OFF -DGGML_NATIVE=OFF -DGGML_OPENMP=OFF \
		-DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TOOLS=OFF \
		-DLLAMA_BUILD_SERVER=OFF -DLLAMA_CURL=OFF $(LLAMA_CMAKE_FLAGS)
	cmake --build $(LLAMA_BUILD) --config Release -j --target llama

test:
	go test ./...

# Runs the tests against the embedded llama.cpp. Set WEBSHADOW_LLAMA_MODEL
# to a GGUF file to include the real-model tests.
test-llama: llama-lib
	CGO_ENABLED=1 go test -tags llama ./...

clean-llama:
	rm -rf $(LLAMA_BUILD)

# --- Benchmark -------------------------------------------------------------
#
#   make bench                                   # qwen2.5-7b on the Enso scenario
#   make bench MODEL=llama3.1-8b SCENARIO=benchmarks/<dir>
#   make bench GENERATOR=qwen2.5-7b READER=qwen2.5-3b
#
# Builds the llama binary, downloads any missing GGUF into $(MODELS_DIR),
# generates a shadow tree with GENERATOR, then answers the scenario's goal
# from that tree with READER. Output lands in runs/<RUN_ID>/.

MODEL       ?= qwen2.5-7b
GENERATOR   ?= $(MODEL)
READER      ?= $(MODEL)
SCENARIO    ?= benchmarks/geektime-enso-funding
REPETITIONS ?= 1
MODELS      ?= models.yaml
MODELS_DIR  ?= models
RUNS        ?= runs
RUN_ID      := $(or $(RUN_ID),$(shell date -u +%Y%m%dT%H%M%SZ))

# models.yaml finds the GGUF files through this variable.
export WEBSHADOW_MODELS_DIR := $(abspath $(MODELS_DIR))

# Download URLs for the local models in models.yaml, keyed by model id. A
# model is stored as $(MODELS_DIR)/<id>.gguf; drop your own file there to
# skip the download.
GGUF_URL_qwen2.5-7b  := https://huggingface.co/bartowski/Qwen2.5-7B-Instruct-GGUF/resolve/main/Qwen2.5-7B-Instruct-Q4_K_M.gguf
GGUF_URL_qwen2.5-3b  := https://huggingface.co/bartowski/Qwen2.5-3B-Instruct-GGUF/resolve/main/Qwen2.5-3B-Instruct-Q4_K_M.gguf
GGUF_URL_llama3.1-8b := https://huggingface.co/bartowski/Meta-Llama-3.1-8B-Instruct-GGUF/resolve/main/Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf

bench_ggufs  = $(foreach m,$(sort $(GENERATOR) $(READER)),$(if $(GGUF_URL_$(m)),$(MODELS_DIR)/$(m).gguf))
scenario_id  = $(shell sed -n 's/^id: *//p' $(SCENARIO)/scenario.yaml)
bench_tree   = $(RUNS)/$(RUN_ID)/trees/$(scenario_id)/$(GENERATOR)/tree-01

bench: llama $(bench_ggufs)
	bin/webshadow bench generate $(SCENARIO) --generator $(GENERATOR) \
		--models $(MODELS) --runs $(RUNS) --run-id $(RUN_ID)
	@if grep -Eq '^[[:space:]]+type:[[:space:]]*none[[:space:]]*$$' $(SCENARIO)/scenario.yaml; then \
		echo "$(scenario_id) is generate-only; skipping the answer stage"; \
	else \
		echo bin/webshadow bench answer $(SCENARIO) --tree $(bench_tree) --reader $(READER) ...; \
		bin/webshadow bench answer $(SCENARIO) --tree $(bench_tree) --reader $(READER) \
			--models $(MODELS) --runs $(RUNS) --run-id $(RUN_ID) --repetitions $(REPETITIONS); \
	fi

# Downloads the GGUF for MODEL (or GENERATOR and READER) without running.
bench-download: $(bench_ggufs)

bench-models:
	@sed -n 's/^  - id: *//p' $(MODELS)

$(MODELS_DIR)/%.gguf:
	@test -n "$(GGUF_URL_$*)" || { echo "no download URL for model $*; put the file at $@" >&2; exit 1; }
	@mkdir -p $(MODELS_DIR)
	curl -L --fail --retry 3 -C - -o $@.part "$(GGUF_URL_$*)"
	mv $@.part $@
