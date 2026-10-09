package decide

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/adiludmer/webshadow/internal/benchmark/model"
)

// PromptVersion is the version of the prompt templates. It is part of
// every task id, so changing a template means bumping it and decisions
// from two versions never mix.
const PromptVersion = "v1"

//go:embed prompts/*.tmpl
var promptFiles embed.FS

// Template is a task type's rendered prompt pair, identified by name and
// content hash.
type Template struct {
	ID     string
	system *template.Template
	user   *template.Template
}

// LoadTemplate returns the templates for a task type at PromptVersion.
func LoadTemplate(taskType string) (*Template, error) {
	sysName := "prompts/system." + PromptVersion + ".tmpl"
	userName := "prompts/" + taskType + "." + PromptVersion + ".tmpl"
	sysText, err := promptFiles.ReadFile(sysName)
	if err != nil {
		return nil, err
	}
	userText, err := promptFiles.ReadFile(userName)
	if err != nil {
		return nil, fmt.Errorf("no prompt for task type %q: %w", taskType, err)
	}
	sys, err := template.New(sysName).Parse(string(sysText))
	if err != nil {
		return nil, err
	}
	user, err := template.New(userName).Parse(string(userText))
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(sysText)
	h.Write([]byte{0})
	h.Write(userText)
	return &Template{
		ID:     taskType + "." + PromptVersion + "@" + hex.EncodeToString(h.Sum(nil))[:12],
		system: sys,
		user:   user,
	}, nil
}

// Render builds the chat messages for a task.
func (t *Template) Render(task Task) ([]model.Message, error) {
	viewJSON, err := json.MarshalIndent(task.view(), "", "  ")
	if err != nil {
		return nil, err
	}
	evidence := make([]Evidence, len(task.Evidence))
	for i, e := range task.Evidence {
		evidence[i] = Evidence{ID: e.ID, Ref: e.Ref, Text: quoteData(e.Text)}
	}
	var sys, user bytes.Buffer
	if err := t.system.Execute(&sys, map[string]any{"MaxRefs": MaxEvidenceRefs}); err != nil {
		return nil, err
	}
	if err := t.user.Execute(&user, map[string]any{
		"TaskJSON": string(viewJSON),
		"Evidence": evidence,
		"Question": task.Question,
	}); err != nil {
		return nil, err
	}
	return []model.Message{
		{Role: model.RoleSystem, Content: sys.String()},
		{Role: model.RoleUser, Content: user.String()},
	}, nil
}

// quoteData keeps recorded text on one line and unable to close the
// evidence block it is quoted in.
func quoteData(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s)
	return strings.ReplaceAll(s, "</evidence", "<\\/evidence")
}

// repairMessage is the one follow-up an invalid answer gets.
func repairMessage(task Task, problem string) model.Message {
	ids := make([]string, len(task.Choices))
	for i, c := range task.Choices {
		ids[i] = c.ID
	}
	refs := make([]string, len(task.Evidence))
	for i, e := range task.Evidence {
		refs[i] = e.ID
	}
	return model.Message{Role: model.RoleUser, Content: fmt.Sprintf(
		"That reply was not valid: %s. Reply with only {\"choice_id\": ..., \"evidence_refs\": [...]}, where choice_id is one of %s and evidence_refs holds 1 to %d of %s.",
		problem, strings.Join(ids, ", "), MaxEvidenceRefs, strings.Join(refs, ", "))}
}
