// Package reader runs the answer stage: an agent that gets a scenario's
// goal and a shadow tree, explores the tree with read-only tools, and
// returns a JSON answer that is scored against expected.json.
package reader

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/adiludmer/webshadow/internal/benchmark/agent"
	"github.com/adiludmer/webshadow/internal/benchmark/model"
	"github.com/adiludmer/webshadow/internal/benchmark/scenario"
	"github.com/adiludmer/webshadow/internal/benchmark/score"
)

// PromptVersion names the instructions below; run records carry it so
// results from different prompts are never mixed.
const PromptVersion = "reader-v1"

const instructions = `You answer a question about a website using only a shadow tree: a
folder of Markdown files that another agent wrote from a recorded browsing
session. You cannot browse the web; everything you know about the site is
in those files.

Explore the tree with the tools, find the facts that answer the question,
then finish. Prefer searching for key terms over reading every file.

Your answer must be a JSON object with exactly these keys: %s.
Follow the question's instructions for units and formats. If the files do
not contain something, still finish, using null for what you could not find.`

// Status is the outcome of an answer run, as recorded and reported.
type Status string

const (
	Success       Status = "success"        // every required field matched
	WrongAnswer   Status = "wrong_answer"   // finished, but the score failed
	InvalidOutput Status = "invalid_output" // finished with a non-object answer
	StepLimit     Status = "step_limit"
	TokenLimit    Status = "token_limit"
	Timeout       Status = "timeout"
	Error         Status = "error"
)

// Result is one answer run.
type Result struct {
	Status Status
	// Answer is the decoded answer when it was a JSON object.
	Answer map[string]any
	Score  *score.Result
	Agent  agent.Result
}

// Options tune a run beyond the scenario's limits.
type Options struct {
	Now func() time.Time
}

// Run answers s's goal from tree with m and scores the answer.
func Run(ctx context.Context, m model.Model, s *scenario.Scenario, tree fs.FS, opts Options) (Result, error) {
	goal, err := os.ReadFile(s.GoalPath())
	if err != nil {
		return Result{}, err
	}
	expectedData, err := os.ReadFile(s.ExpectedPath())
	if err != nil {
		return Result{}, err
	}
	expected, err := score.DecodeJSON(expectedData)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", s.Expected, err)
	}
	expectedObj, ok := expected.(map[string]any)
	if !ok {
		return Result{}, fmt.Errorf("%s must be a JSON object", s.Expected)
	}

	keys := strings.Join(s.Evaluation.RequiredFields, ", ")
	ar := agent.Run(ctx, m, agent.Config{
		Instructions: fmt.Sprintf(instructions, keys),
		Task:         fmt.Sprintf("Question:\n\n%s\n\nAnswer with a JSON object with the keys %s.", strings.TrimSpace(string(goal)), keys),
		Tools:        Tools(tree),
		MaxSteps:     s.Limits.Answer.MaxSteps,
		Timeout:      time.Duration(s.Limits.Answer.TimeoutSeconds) * time.Second,
		MaxTokens:    s.Limits.Answer.MaxTokens,
		Now:          opts.Now,
	})

	res := Result{Agent: ar}
	switch ar.Outcome {
	case agent.Finished:
		answer, err := score.DecodeJSON(ar.Answer)
		obj, isObj := answer.(map[string]any)
		if err != nil || !isObj {
			res.Status = InvalidOutput
			return res, nil
		}
		res.Answer = obj
		sc := score.Fields(expectedObj, obj, s.Evaluation)
		res.Score = &sc
		res.Status = WrongAnswer
		if sc.Pass {
			res.Status = Success
		}
	case agent.StepLimit:
		res.Status = StepLimit
	case agent.TokenLimit:
		res.Status = TokenLimit
	case agent.Timeout:
		res.Status = Timeout
	default:
		res.Status = Error
	}
	return res, nil
}
