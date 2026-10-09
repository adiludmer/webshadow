//go:build llama

package llama

/*
#cgo CFLAGS: -O2 -I${SRCDIR}/../../third_party/llama.cpp/include -I${SRCDIR}/../../third_party/llama.cpp/ggml/include
#cgo LDFLAGS: -L${SRCDIR}/../../third_party/llama.cpp/build/src -L${SRCDIR}/../../third_party/llama.cpp/build/ggml/src
#cgo LDFLAGS: -lllama -lggml -lggml-cpu -lggml-base
#cgo linux LDFLAGS: -Wl,-Bstatic -lstdc++ -Wl,-Bdynamic -static-libgcc -lm -lpthread -ldl
#cgo darwin LDFLAGS: -L${SRCDIR}/../../third_party/llama.cpp/build/ggml/src/ggml-metal -L${SRCDIR}/../../third_party/llama.cpp/build/ggml/src/ggml-blas -lggml-metal -lggml-blas
#cgo darwin LDFLAGS: -lc++ -framework Accelerate -framework Foundation -framework Metal -framework MetalKit
#include <stdlib.h>
#include "glue.h"
*/
import "C"

import (
	"context"
	"errors"
	"unsafe"
)

// Available reports whether this binary has llama.cpp linked in.
const Available = true

type handle = *C.wsl_model

const errLen = 2048

// Load reads a GGUF file and creates its context.
func Load(path string, opts Options) (*Model, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	errBuf := (*C.char)(C.calloc(errLen, 1))
	defer C.free(unsafe.Pointer(errBuf))

	gpu := C.int(C.WSL_DEFAULT)
	if opts.GPULayers != nil {
		gpu = C.int(*opts.GPULayers)
	}
	h := C.wsl_load(cpath, C.int(opts.ContextSize), gpu, C.int(opts.Threads), errBuf, errLen)
	if h == nil {
		return nil, errors.New("llama: " + C.GoString(errBuf))
	}
	return &Model{handle: h}, nil
}

// ContextSize is the number of tokens the context holds.
func (m *Model) ContextSize() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handle == nil {
		return 0
	}
	return int(C.wsl_n_ctx(m.handle))
}

// Chat renders msgs with the model's chat template and generates a reply
// of at most maxTokens tokens (zero means until the context is full).
// temperature <= 0 decodes greedily, so equal inputs give equal outputs.
// A non-empty grammar is GBNF with a "root" rule; generation then only
// produces text the grammar accepts. Cancelling ctx stops generation after
// the current token and returns ctx's error.
func (m *Model) Chat(ctx context.Context, msgs []Message, maxTokens int, temperature float32, seed uint32, grammar string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handle == nil {
		return Result{}, errors.New("llama: model is closed")
	}

	n := len(msgs)
	roles := (*[1 << 20]*C.char)(C.malloc(C.size_t(max(n, 1)) * C.size_t(unsafe.Sizeof(uintptr(0)))))[:n:n]
	contents := (*[1 << 20]*C.char)(C.malloc(C.size_t(max(n, 1)) * C.size_t(unsafe.Sizeof(uintptr(0)))))[:n:n]
	for i, msg := range msgs {
		roles[i] = C.CString(msg.Role)
		contents[i] = C.CString(msg.Content)
	}
	defer func() {
		for i := range n {
			C.free(unsafe.Pointer(roles[i]))
			C.free(unsafe.Pointer(contents[i]))
		}
		C.free(unsafe.Pointer(unsafe.SliceData(roles)))
		C.free(unsafe.Pointer(unsafe.SliceData(contents)))
	}()
	errBuf := (*C.char)(C.calloc(errLen, 1))
	defer C.free(unsafe.Pointer(errBuf))
	var cgrammar *C.char
	if grammar != "" {
		cgrammar = C.CString(grammar)
		defer C.free(unsafe.Pointer(cgrammar))
	}

	h := m.handle
	C.wsl_reset_abort(h)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			C.wsl_abort(h)
		case <-done:
		}
	}()

	var out C.wsl_result
	rc := C.wsl_chat(h, (**C.char)(unsafe.SliceData(roles)), (**C.char)(unsafe.SliceData(contents)), C.int(n),
		C.int(maxTokens), C.float(temperature), C.uint32_t(seed), cgrammar, &out, errBuf, errLen)
	if rc != 0 {
		return Result{}, errors.New("llama: " + C.GoString(errBuf))
	}
	defer C.free(unsafe.Pointer(out.text))
	res := Result{
		Text:         C.GoString(out.text),
		PromptTokens: int(out.prompt_tokens),
		OutputTokens: int(out.output_tokens),
		StopReason:   StopLength,
	}
	switch out.stop {
	case C.WSL_STOP_EOG:
		res.StopReason = StopEOG
	case C.WSL_STOP_ABORTED:
		return res, ctx.Err()
	}
	return res, nil
}

// Close frees the model and its context. It is safe to call twice.
func (m *Model) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handle != nil {
		C.wsl_free(m.handle)
		m.handle = nil
	}
	return nil
}
