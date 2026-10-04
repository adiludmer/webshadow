# `make` builds a pure-Go webshadow. `make llama` builds one binary with
# llama.cpp linked in statically, so GGUF models run in-process through the
# llama adapter.

LLAMA_DIR   := third_party/llama.cpp
LLAMA_BUILD := $(LLAMA_DIR)/build
LLAMA_LIB   := $(LLAMA_BUILD)/src/libllama.a

# Extra CMake flags for GPU backends, e.g. -DGGML_METAL=ON or -DGGML_CUDA=ON.
LLAMA_CMAKE_FLAGS ?=

.PHONY: build llama llama-lib test test-llama clean-llama

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
