// Package llamatest builds a tiny GGUF model for tests, since a real model
// may not be downloadable where tests run.
package llamatest

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	ggufTypeUint32  = 4
	ggufTypeFloat32 = 6
	ggufTypeString  = 8
	ggufTypeArray   = 9
	ggmlTypeF32     = 0
	ggufAlignment   = 32
)

type ggufKV struct {
	key string
	raw []byte // type tag followed by the encoded value
}

// WriteTinyModel writes tiny.gguf into dir and returns its path: the Llama
// SentencePiece vocabulary checked into llama.cpp plus a two-layer Llama
// with seeded random weights and an 8192-token context. Its output is
// gibberish, but it exercises loading, templating, tokenizing, decoding and
// every stop condition in milliseconds.
func WriteTinyModel(dir string) (string, error) {
	kvs, err := readGGUFKVs(filepath.Join(llamaDir(), "models", "ggml-vocab-llama-spm.gguf"))
	if err != nil {
		return "", fmt.Errorf("reading vocabulary: %w", err)
	}

	const (
		nEmbd  = 32
		nHead  = 4
		nFF    = 64
		nLayer = 2
		nCtx   = 8192
	)
	var out []ggufKV
	for _, kv := range kvs {
		if !strings.HasPrefix(kv.key, "llama.") && kv.key != "general.architecture" {
			out = append(out, kv)
		}
	}
	nVocab := 0
	for _, kv := range kvs {
		if kv.key == "tokenizer.ggml.tokens" {
			nVocab = int(binary.LittleEndian.Uint64(kv.raw[8:16]))
		}
	}
	if nVocab == 0 {
		return "", errors.New("vocabulary has no tokens")
	}
	out = append(out,
		strKV("general.architecture", "llama"),
		u32KV("llama.context_length", nCtx),
		u32KV("llama.embedding_length", nEmbd),
		u32KV("llama.block_count", nLayer),
		u32KV("llama.feed_forward_length", nFF),
		u32KV("llama.attention.head_count", nHead),
		u32KV("llama.attention.head_count_kv", nHead),
		u32KV("llama.rope.dimension_count", nEmbd/nHead),
		f32KV("llama.attention.layer_norm_rms_epsilon", 1e-5),
	)

	type tensor struct {
		name string
		dims []uint64
		norm bool
	}
	tensors := []tensor{{"token_embd.weight", []uint64{nEmbd, uint64(nVocab)}, false}, {"output_norm.weight", []uint64{nEmbd}, true}}
	for l := range nLayer {
		p := fmt.Sprintf("blk.%d.", l)
		tensors = append(tensors,
			tensor{p + "attn_norm.weight", []uint64{nEmbd}, true},
			tensor{p + "attn_q.weight", []uint64{nEmbd, nEmbd}, false},
			tensor{p + "attn_k.weight", []uint64{nEmbd, nEmbd}, false},
			tensor{p + "attn_v.weight", []uint64{nEmbd, nEmbd}, false},
			tensor{p + "attn_output.weight", []uint64{nEmbd, nEmbd}, false},
			tensor{p + "ffn_norm.weight", []uint64{nEmbd}, true},
			tensor{p + "ffn_gate.weight", []uint64{nEmbd, nFF}, false},
			tensor{p + "ffn_up.weight", []uint64{nEmbd, nFF}, false},
			tensor{p + "ffn_down.weight", []uint64{nFF, nEmbd}, false},
		)
	}

	path := filepath.Join(dir, "tiny.gguf")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	le := binary.LittleEndian
	w.WriteString("GGUF")
	binary.Write(w, le, uint32(3))
	binary.Write(w, le, uint64(len(tensors)))
	binary.Write(w, le, uint64(len(out)))
	for _, kv := range out {
		writeString(w, kv.key)
		w.Write(kv.raw)
	}
	var offset uint64
	sizes := make([]uint64, len(tensors))
	for i, tt := range tensors {
		writeString(w, tt.name)
		binary.Write(w, le, uint32(len(tt.dims)))
		n := uint64(1)
		for _, d := range tt.dims {
			binary.Write(w, le, d)
			n *= d
		}
		binary.Write(w, le, uint32(ggmlTypeF32))
		binary.Write(w, le, offset)
		sizes[i] = n * 4
		offset += pad(sizes[i])
	}
	w.Flush()
	pos, _ := f.Seek(0, io.SeekCurrent)
	w.Write(make([]byte, pad(uint64(pos))-uint64(pos)))

	rng := rand.New(rand.NewSource(1))
	for i, tt := range tensors {
		buf := make([]byte, 4)
		for range sizes[i] / 4 {
			v := float32(1)
			if !tt.norm {
				v = float32(rng.NormFloat64() * 0.2)
			}
			le.PutUint32(buf, math.Float32bits(v))
			w.Write(buf)
		}
		w.Write(make([]byte, pad(sizes[i])-sizes[i]))
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return path, f.Close()
}

// llamaDir is the llama.cpp submodule, found relative to this file.
func llamaDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "third_party", "llama.cpp")
}

func pad(n uint64) uint64 { return (n + ggufAlignment - 1) / ggufAlignment * ggufAlignment }

func writeString(w io.Writer, s string) {
	binary.Write(w, binary.LittleEndian, uint64(len(s)))
	io.WriteString(w, s)
}

func strKV(key, v string) ggufKV {
	raw := binary.LittleEndian.AppendUint32(nil, ggufTypeString)
	raw = binary.LittleEndian.AppendUint64(raw, uint64(len(v)))
	return ggufKV{key, append(raw, v...)}
}

func u32KV(key string, v uint32) ggufKV {
	raw := binary.LittleEndian.AppendUint32(nil, ggufTypeUint32)
	return ggufKV{key, binary.LittleEndian.AppendUint32(raw, v)}
}

func f32KV(key string, v float32) ggufKV {
	raw := binary.LittleEndian.AppendUint32(nil, ggufTypeFloat32)
	return ggufKV{key, binary.LittleEndian.AppendUint32(raw, math.Float32bits(v))}
}

// readGGUFKVs returns the metadata of a GGUF file with each value kept in
// its encoded form, so it can be copied into another file.
func readGGUFKVs(path string) ([]ggufKV, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 24 || string(data[:4]) != "GGUF" {
		return nil, fmt.Errorf("%s is not a GGUF file", path)
	}
	le := binary.LittleEndian
	n := le.Uint64(data[16:24])
	pos := 24
	readStr := func() string {
		l := int(le.Uint64(data[pos:]))
		s := string(data[pos+8 : pos+8+l])
		pos += 8 + l
		return s
	}
	var skip func(typ uint32)
	skip = func(typ uint32) {
		switch typ {
		case 0, 1, 7:
			pos++
		case 2, 3:
			pos += 2
		case 4, 5, 6:
			pos += 4
		case 10, 11, 12:
			pos += 8
		case ggufTypeString:
			readStr()
		case ggufTypeArray:
			elem := le.Uint32(data[pos:])
			count := le.Uint64(data[pos+4:])
			pos += 12
			for range count {
				skip(elem)
			}
		default:
			panic(fmt.Sprintf("unknown GGUF type %d", typ))
		}
	}
	kvs := make([]ggufKV, 0, n)
	for range n {
		key := readStr()
		start := pos
		typ := le.Uint32(data[pos:])
		pos += 4
		skip(typ)
		kvs = append(kvs, ggufKV{key, data[start:pos]})
	}
	return kvs, nil
}
