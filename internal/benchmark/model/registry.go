package model

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Adapter names a models.yaml entry can use.
const (
	AdapterFake             = "fake"
	AdapterOpenAICompatible = "openai_compatible"
	AdapterLlama            = "llama"
)

// Adapters lists the adapters this build knows.
var Adapters = []string{AdapterFake, AdapterOpenAICompatible, AdapterLlama}

// Defaults applied when an entry leaves a value out.
const (
	DefaultTimeoutSeconds = 120
	DefaultMaxRetries     = 2
)

// Config is one models.yaml entry.
type Config struct {
	ID      string `yaml:"id"`
	Adapter string `yaml:"adapter"`
	// Model is the provider's model name, sent in each request.
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
	// APIKeyEnv names the environment variable that holds the API key.
	// Leave it out for servers that need no key.
	APIKeyEnv      string   `yaml:"api_key_env"`
	MaxTokens      int      `yaml:"max_tokens"`
	Temperature    *float64 `yaml:"temperature"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
	MaxRetries     *int     `yaml:"max_retries"`
	// Replies is the script for the fake adapter.
	Replies []string `yaml:"replies"`

	// The llama adapter's settings. ModelPath is the GGUF file and may
	// reference environment variables, as in ${WEBSHADOW_LLAMA_MODEL}.
	ModelPath   string `yaml:"model_path"`
	ContextSize int    `yaml:"context_size"`
	GPULayers   *int   `yaml:"gpu_layers"`
	Threads     int    `yaml:"threads"`
	Seed        uint32 `yaml:"seed"`

	// APIKey exists only so a literal key gets a clear error instead of
	// being silently accepted or reported as an unknown field.
	APIKey string `yaml:"api_key"`
}

// Registry is a parsed models.yaml.
type Registry struct {
	Models []Config `yaml:"models"`
}

var (
	modelIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// LoadRegistry reads and validates a models.yaml file.
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := ParseRegistry(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// ParseRegistry decodes models.yaml content, applies defaults and
// validates every entry. All problems are reported together.
func ParseRegistry(data []byte) (*Registry, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var r Registry
	if err := dec.Decode(&r); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty models file")
		}
		return nil, err
	}
	for i := range r.Models {
		c := &r.Models[i]
		if c.TimeoutSeconds == 0 {
			c.TimeoutSeconds = DefaultTimeoutSeconds
		}
		if c.MaxRetries == nil {
			n := DefaultMaxRetries
			c.MaxRetries = &n
		}
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *Registry) validate() error {
	var errs []error
	fail := func(i int, id, format string, args ...any) {
		name := id
		if name == "" {
			name = fmt.Sprintf("#%d", i+1)
		}
		errs = append(errs, fmt.Errorf("model %s: %s", name, fmt.Sprintf(format, args...)))
	}
	if len(r.Models) == 0 {
		errs = append(errs, errors.New("models must list at least one model"))
	}
	seen := map[string]bool{}
	for i, c := range r.Models {
		switch {
		case c.ID == "":
			fail(i, c.ID, "id is required")
		case !modelIDPattern.MatchString(c.ID):
			fail(i, c.ID, "id must be lowercase letters, digits, '.', '_' or '-'")
		case seen[c.ID]:
			fail(i, c.ID, "id is used twice")
		}
		seen[c.ID] = true

		if c.APIKey != "" {
			fail(i, c.ID, "api_key is not allowed; put the key in an environment variable and name it in api_key_env")
		}
		if c.APIKeyEnv != "" && !envNamePattern.MatchString(c.APIKeyEnv) {
			fail(i, c.ID, "api_key_env must be an environment variable name, not %q", c.APIKeyEnv)
		}
		if c.MaxTokens < 0 {
			fail(i, c.ID, "max_tokens must not be negative")
		}
		if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > 2) {
			fail(i, c.ID, "temperature must be between 0 and 2")
		}
		if c.TimeoutSeconds < 0 {
			fail(i, c.ID, "timeout_seconds must not be negative")
		}
		if *c.MaxRetries < 0 {
			fail(i, c.ID, "max_retries must not be negative")
		}

		if c.Adapter != AdapterFake && len(c.Replies) > 0 {
			fail(i, c.ID, "replies is only used by the fake adapter")
		}
		if c.Adapter != AdapterLlama && (c.ModelPath != "" || c.ContextSize != 0 || c.GPULayers != nil || c.Threads != 0 || c.Seed != 0) {
			fail(i, c.ID, "model_path, context_size, gpu_layers, threads and seed are only used by the llama adapter")
		}

		switch c.Adapter {
		case "":
			fail(i, c.ID, "adapter is required (one of %s)", strings.Join(Adapters, ", "))
		case AdapterLlama:
			if c.ModelPath == "" {
				fail(i, c.ID, "model_path is required")
			}
			if c.BaseURL != "" || c.APIKeyEnv != "" || c.Model != "" {
				fail(i, c.ID, "model, base_url and api_key_env are not used by the llama adapter, which runs model_path in-process")
			}
			if c.ContextSize < 0 || c.Threads < 0 {
				fail(i, c.ID, "context_size and threads must not be negative")
			}
		case AdapterFake:
			if len(c.Replies) == 0 {
				fail(i, c.ID, "the fake adapter needs at least one entry in replies")
			}
		case AdapterOpenAICompatible:
			if c.Model == "" {
				fail(i, c.ID, "model is required")
			}
			if !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://") {
				fail(i, c.ID, "base_url must be an http or https URL")
			}
		default:
			fail(i, c.ID, "adapter %q is not known (one of %s)", c.Adapter, strings.Join(Adapters, ", "))
		}
	}
	return errors.Join(errs...)
}

// IDs returns the registered model ids, sorted.
func (r *Registry) IDs() []string {
	ids := make([]string, len(r.Models))
	for i, c := range r.Models {
		ids[i] = c.ID
	}
	sort.Strings(ids)
	return ids
}

// Get returns the entry with the given id.
func (r *Registry) Get(id string) (Config, bool) {
	for _, c := range r.Models {
		if c.ID == id {
			return c, true
		}
	}
	return Config{}, false
}

// Open builds the model with the given id. getenv resolves api_key_env and
// variables in model_path (pass os.Getenv); a variable that is unset or
// empty is an error, so a run never starts with a missing key or model.
// For llama entries, Open loads the model file into memory.
func (r *Registry) Open(id string, getenv func(string) string) (Model, error) {
	c, ok := r.Get(id)
	if !ok {
		return nil, fmt.Errorf("model %q is not in the registry (known: %s)", id, strings.Join(r.IDs(), ", "))
	}
	var key string
	if c.APIKeyEnv != "" {
		key = getenv(c.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("model %s: environment variable %s is not set", c.ID, c.APIKeyEnv)
		}
	}
	switch c.Adapter {
	case AdapterFake:
		return NewFake(c.ID, c.Replies...), nil
	case AdapterOpenAICompatible:
		return NewOpenAICompatible(c, key), nil
	case AdapterLlama:
		path, err := expand(c.ModelPath, getenv)
		if err != nil {
			return nil, fmt.Errorf("model %s: model_path: %w", c.ID, err)
		}
		m, err := OpenLlama(c, path)
		if err != nil {
			return nil, fmt.Errorf("model %s: %w", c.ID, err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("model %s: adapter %q is not known", c.ID, c.Adapter)
}

// expand substitutes $VAR and ${VAR} in s, failing on any variable that is
// unset or empty.
func expand(s string, getenv func(string) string) (string, error) {
	var missing []string
	out := os.Expand(s, func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("environment variable %s is not set", strings.Join(missing, ", "))
	}
	return out, nil
}
