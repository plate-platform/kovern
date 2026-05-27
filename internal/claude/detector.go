// Package claude provides semantic livelock detection backed by the Anthropic API.
// The Detector interface lets callers swap in a mock without a live API key.
package claude

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/plate-platform/kovern/api/v1alpha1"
)

// Detector evaluates whether recent agent turns indicate a semantic livelock.
type Detector interface {
	IsLivelocked(ctx context.Context, turns []string, spec *v1alpha1.LivelockPolicySpec) (bool, string, error)
}

// NoOpDetector always returns false. Useful in tests and when semantic detection is disabled.
type NoOpDetector struct{}

func NewNoOpDetector() *NoOpDetector { return &NoOpDetector{} }

func (n *NoOpDetector) IsLivelocked(_ context.Context, turns []string, spec *v1alpha1.LivelockPolicySpec) (bool, string, error) {
	if spec.Detection.Semantic == nil || !spec.Detection.Semantic.Enabled {
		return false, "", nil
	}
	if len(turns) < 2 {
		return false, "", nil
	}
	return false, "", nil
}

// AnthropicDetector uses the Anthropic Messages API to evaluate semantic similarity.
// It reads ANTHROPIC_API_KEY from the environment automatically.
type AnthropicDetector struct {
	client *anthropic.Client
}

// New returns an AnthropicDetector using ANTHROPIC_API_KEY from the environment.
func New() *AnthropicDetector {
	c := anthropic.NewClient()
	return &AnthropicDetector{client: &c}
}

// NewWithClient creates an AnthropicDetector with an injected client — used in tests.
func NewWithClient(c *anthropic.Client) *AnthropicDetector {
	return &AnthropicDetector{client: c}
}

// IsLivelocked asks Claude whether a sequence of agent turns is a semantic loop.
// Returns (true, reason, nil) when a livelock is detected.
// Returns (false, "", nil) without calling the API when semantic detection is disabled.
func (d *AnthropicDetector) IsLivelocked(ctx context.Context, turns []string, spec *v1alpha1.LivelockPolicySpec) (bool, string, error) {
	if spec.Detection.Semantic == nil || !spec.Detection.Semantic.Enabled {
		return false, "", nil
	}
	if len(turns) < 2 {
		return false, "", nil
	}

	cfg := spec.Detection.Semantic
	model := cfg.Model
	if model == "" {
		model = "claude-haiku-4-5-20251001"
	}
	threshold := cfg.SimilarityThreshold
	if threshold == "" {
		threshold = "0.92"
	}

	msg, err := d.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: 256,
		System: []anthropic.TextBlockParam{
			{Text: livelockSystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(buildPrompt(turns, threshold))),
		},
	})
	if err != nil {
		return false, "", fmt.Errorf("claude livelock check failed: %w", err)
	}
	if len(msg.Content) == 0 {
		return false, "", fmt.Errorf("empty response from Claude")
	}

	response := strings.TrimSpace(msg.Content[0].Text)
	upper := strings.ToUpper(response)

	if strings.HasPrefix(upper, "LIVELOCK:YES") {
		reason := strings.TrimSpace(strings.TrimPrefix(response, "LIVELOCK:YES"))
		reason = strings.TrimSpace(strings.TrimPrefix(reason, "|"))
		return true, reason, nil
	}
	return false, "", nil
}

const livelockSystemPrompt = `You are a livelock detector for AI agent systems running on Kubernetes.
You will receive a sequence of recent agent turns (tool calls and their results).
Determine if the agent is stuck in a semantic loop — repeating equivalent actions
or receiving functionally identical responses, making no meaningful progress.

Respond ONLY in one of these two formats (no other text):
  LIVELOCK:YES | <one-sentence reason>
  LIVELOCK:NO`

func buildPrompt(turns []string, threshold string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Similarity threshold: %s\n\nRecent agent turns (oldest first):\n\n", threshold)
	for i, t := range turns {
		fmt.Fprintf(&b, "--- Turn %d ---\n%s\n\n", i+1, t)
	}
	b.WriteString("Is this agent stuck in a livelock?")
	return b.String()
}
