// Thin C layer over llama.h: load a GGUF model, run one chat completion
// from a fresh context state, free. Keeping the token loop in C means one
// cgo call per completion.
#ifndef WEBSHADOW_LLAMA_GLUE_H
#define WEBSHADOW_LLAMA_GLUE_H

#include <stdint.h>

typedef struct wsl_model wsl_model;

// WSL_DEFAULT as n_gpu_layers keeps llama.cpp's default.
#define WSL_DEFAULT INT32_MIN

enum {
	WSL_STOP_EOG = 0,     // the model ended its turn
	WSL_STOP_LENGTH = 1,  // max_tokens or the context ran out
	WSL_STOP_ABORTED = 2, // wsl_abort was called
};

typedef struct {
	char *text;        // malloc'd, NUL-terminated; free with free()
	int prompt_tokens;
	int output_tokens;
	int stop;
} wsl_result;

wsl_model *wsl_load(const char *path, int n_ctx, int n_gpu_layers, int n_threads, char *err, int errlen);

// wsl_chat applies the model's chat template to the messages, generates up
// to max_tokens tokens and returns 0, or -1 with err set. temp <= 0 means
// greedy decoding. A non-empty grammar is GBNF with a "root" rule that
// every generated token must keep satisfiable.
int wsl_chat(wsl_model *m, const char **roles, const char **contents, int n_msgs,
	int max_tokens, float temp, uint32_t seed, const char *grammar, wsl_result *out, char *err, int errlen);

// wsl_abort makes a running wsl_chat stop after the current token. The
// flag stays set until wsl_reset_abort, so an abort that lands just before
// wsl_chat starts is not lost.
void wsl_abort(wsl_model *m);
void wsl_reset_abort(wsl_model *m);

int wsl_n_ctx(wsl_model *m);

void wsl_free(wsl_model *m);

#endif
