//go:build llama

#include "glue.h"

#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "llama.h"

struct wsl_model {
	struct llama_model *model;
	struct llama_context *ctx;
	const struct llama_vocab *vocab;
	const char *tmpl;
	volatile int abort;
};

// llama.cpp logs a lot at load time. Keep quiet unless WEBSHADOW_LLAMA_LOG
// is set, but remember the latest error so failures can say why.
static pthread_mutex_t log_mu = PTHREAD_MUTEX_INITIALIZER;
static char last_error[1024];
static int verbose;

static void log_cb(enum ggml_log_level level, const char *text, void *ud) {
	(void)ud;
	if (verbose) {
		fputs(text, stderr);
	}
	if (level == GGML_LOG_LEVEL_ERROR) {
		pthread_mutex_lock(&log_mu);
		snprintf(last_error, sizeof last_error, "%s", text);
		size_t n = strlen(last_error);
		while (n > 0 && (last_error[n - 1] == '\n' || last_error[n - 1] == ' ')) {
			last_error[--n] = 0;
		}
		pthread_mutex_unlock(&log_mu);
	}
}

static pthread_once_t init_once = PTHREAD_ONCE_INIT;

static void init(void) {
	const char *v = getenv("WEBSHADOW_LLAMA_LOG");
	verbose = v != NULL && v[0] != 0;
	llama_log_set(log_cb, NULL);
	llama_backend_init();
}

static void set_err(char *err, int errlen, const char *fmt, ...) {
	va_list ap;
	va_start(ap, fmt);
	int n = vsnprintf(err, errlen, fmt, ap);
	va_end(ap);
	pthread_mutex_lock(&log_mu);
	if (last_error[0] && n >= 0 && n < errlen) {
		snprintf(err + n, errlen - n, ": %s", last_error);
	}
	last_error[0] = 0;
	pthread_mutex_unlock(&log_mu);
}

wsl_model *wsl_load(const char *path, int n_ctx, int n_gpu_layers, int n_threads, char *err, int errlen) {
	pthread_once(&init_once, init);

	struct llama_model_params mp = llama_model_default_params();
	if (n_gpu_layers != WSL_DEFAULT) {
		mp.n_gpu_layers = n_gpu_layers;
	}
	struct llama_model *model = llama_model_load_from_file(path, mp);
	if (model == NULL) {
		set_err(err, errlen, "loading %s failed", path);
		return NULL;
	}

	struct llama_context_params cp = llama_context_default_params();
	if (n_ctx > 0) {
		cp.n_ctx = (uint32_t)n_ctx;
	}
	if (n_threads > 0) {
		cp.n_threads = n_threads;
		cp.n_threads_batch = n_threads;
	}
	cp.no_perf = true;
	// The whole prompt is decoded in one batch, so the batch must be able
	// to hold a full context.
	struct llama_context *ctx = llama_init_from_model(model, cp);
	if (ctx != NULL && llama_n_batch(ctx) < llama_n_ctx(ctx)) {
		// llama.cpp may round n_ctx up, so size the batch from the context
		// it actually created.
		cp.n_ctx = llama_n_ctx(ctx);
		cp.n_batch = cp.n_ctx;
		llama_free(ctx);
		ctx = llama_init_from_model(model, cp);
	}
	if (ctx == NULL) {
		set_err(err, errlen, "creating a context for %s failed", path);
		llama_model_free(model);
		return NULL;
	}

	wsl_model *m = calloc(1, sizeof *m);
	m->model = model;
	m->ctx = ctx;
	m->vocab = llama_model_get_vocab(model);
	m->tmpl = llama_model_chat_template(model, NULL);
	return m;
}

int wsl_n_ctx(wsl_model *m) { return (int)llama_n_ctx(m->ctx); }

void wsl_abort(wsl_model *m) { m->abort = 1; }

void wsl_reset_abort(wsl_model *m) { m->abort = 0; }

// format renders the chat with the model's template, falling back to
// ChatML when the model has none or llama.cpp does not support it.
static char *format(wsl_model *m, const struct llama_chat_message *msgs, int n, int *len) {
	const char *tmpls[2] = {m->tmpl, "chatml"};
	for (int t = 0; t < 2; t++) {
		if (tmpls[t] == NULL) {
			continue;
		}
		int need = llama_chat_apply_template(tmpls[t], msgs, n, true, NULL, 0);
		if (need < 0) {
			continue;
		}
		char *buf = malloc((size_t)need + 1);
		llama_chat_apply_template(tmpls[t], msgs, n, true, buf, need + 1);
		buf[need] = 0;
		*len = need;
		return buf;
	}
	return NULL;
}

int wsl_chat(wsl_model *m, const char **roles, const char **contents, int n_msgs,
	int max_tokens, float temp, uint32_t seed, const char *grammar, wsl_result *out, char *err, int errlen) {
	memset(out, 0, sizeof *out);

	struct llama_chat_message *msgs = malloc(sizeof *msgs * (size_t)(n_msgs > 0 ? n_msgs : 1));
	for (int i = 0; i < n_msgs; i++) {
		msgs[i].role = roles[i];
		msgs[i].content = contents[i];
	}
	int plen = 0;
	char *prompt = format(m, msgs, n_msgs, &plen);
	free(msgs);
	if (prompt == NULL) {
		set_err(err, errlen, "applying the chat template failed");
		return -1;
	}

	int n_prompt = -llama_tokenize(m->vocab, prompt, plen, NULL, 0, true, true);
	llama_token *tokens = malloc(sizeof *tokens * (size_t)(n_prompt > 0 ? n_prompt : 1));
	if (llama_tokenize(m->vocab, prompt, plen, tokens, n_prompt, true, true) < 0) {
		free(prompt);
		free(tokens);
		set_err(err, errlen, "tokenizing the prompt failed");
		return -1;
	}
	free(prompt);

	int n_ctx = (int)llama_n_ctx(m->ctx);
	if (n_prompt >= n_ctx) {
		free(tokens);
		set_err(err, errlen, "prompt is %d tokens but the context holds %d", n_prompt, n_ctx);
		return -1;
	}

	// Each call starts from an empty context: callers send the full
	// transcript every time.
	llama_memory_clear(llama_get_memory(m->ctx), true);

	struct llama_sampler *smpl = llama_sampler_chain_init(llama_sampler_chain_default_params());
	// A grammar goes first in the chain, so every later sampler only sees
	// tokens the grammar allows.
	if (grammar != NULL && grammar[0] != 0) {
		struct llama_sampler *g = llama_sampler_init_grammar(m->vocab, grammar, "root");
		if (g == NULL) {
			llama_sampler_free(smpl);
			free(tokens);
			set_err(err, errlen, "the grammar does not parse");
			return -1;
		}
		llama_sampler_chain_add(smpl, g);
	}
	if (temp <= 0) {
		llama_sampler_chain_add(smpl, llama_sampler_init_greedy());
	} else {
		llama_sampler_chain_add(smpl, llama_sampler_init_min_p(0.05f, 1));
		llama_sampler_chain_add(smpl, llama_sampler_init_temp(temp));
		llama_sampler_chain_add(smpl, llama_sampler_init_dist(seed));
	}

	size_t cap = 4096, len = 0;
	char *text = malloc(cap);
	int rc = 0, n_out = 0, stop = WSL_STOP_LENGTH;
	int budget = n_ctx - n_prompt;
	if (max_tokens > 0 && max_tokens < budget) {
		budget = max_tokens;
	}

	struct llama_batch batch = llama_batch_get_one(tokens, n_prompt);
	llama_token tok;
	for (;;) {
		if (m->abort) {
			stop = WSL_STOP_ABORTED;
			break;
		}
		int d = llama_decode(m->ctx, batch);
		if (d != 0) {
			set_err(err, errlen, "llama_decode returned %d", d);
			rc = -1;
			break;
		}
		if (n_out >= budget) {
			break;
		}
		tok = llama_sampler_sample(smpl, m->ctx, -1);
		if (llama_vocab_is_eog(m->vocab, tok)) {
			stop = WSL_STOP_EOG;
			break;
		}
		n_out++;
		char piece[256];
		int n = llama_token_to_piece(m->vocab, tok, piece, sizeof piece, 0, false);
		if (n < 0) {
			set_err(err, errlen, "token %d does not fit the piece buffer", tok);
			rc = -1;
			break;
		}
		if (len + (size_t)n + 1 > cap) {
			cap = (cap + (size_t)n) * 2;
			text = realloc(text, cap);
		}
		memcpy(text + len, piece, (size_t)n);
		len += (size_t)n;
		batch = llama_batch_get_one(&tok, 1);
	}
	text[len] = 0;

	llama_sampler_free(smpl);
	free(tokens);
	if (rc != 0) {
		free(text);
		return rc;
	}
	out->text = text;
	out->prompt_tokens = n_prompt;
	out->output_tokens = n_out;
	out->stop = stop;
	return 0;
}

void wsl_free(wsl_model *m) {
	if (m == NULL) {
		return;
	}
	llama_free(m->ctx);
	llama_model_free(m->model);
	free(m);
}
