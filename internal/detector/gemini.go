package detector

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"

	"github.com/plate-platform/kovern/api/v1alpha1"
)

// GeminiDetector uses the Google Gemini API to evaluate semantic similarity.
// Construct with NewGemini, passing GOOGLE_API_KEY.
type GeminiDetector struct {
	client *genai.Client
}

// NewGemini returns a GeminiDetector authenticated with the provided API key.
func NewGemini(apiKey string) (*GeminiDetector, error) {
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("gemini client: %w", err)
	}
	return &GeminiDetector{client: client}, nil
}

// IsLivelocked asks Gemini whether a sequence of agent turns is a semantic loop.
func (d *GeminiDetector) IsLivelocked(ctx context.Context, turns []string, spec *v1alpha1.LivelockPolicySpec) (bool, string, error) {
	if spec.Detection.Semantic == nil || !spec.Detection.Semantic.Enabled {
		return false, "", nil
	}
	if len(turns) < 2 {
		return false, "", nil
	}

	cfg := spec.Detection.Semantic
	model := cfg.Model
	if model == "" {
		model = "gemini-2.5-flash"
	}
	threshold := cfg.SimilarityThreshold
	if threshold == "" {
		threshold = "0.92"
	}

	result, err := d.client.Models.GenerateContent(ctx, model,
		[]*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: buildPrompt(turns, threshold)}}},
		},
		&genai.GenerateContentConfig{
			SystemInstruction: &genai.Content{
				Parts: []*genai.Part{{Text: livelockSystemPrompt}},
			},
			MaxOutputTokens: 256,
		},
	)
	if err != nil {
		return false, "", fmt.Errorf("gemini livelock check failed: %w", err)
	}

	text := strings.TrimSpace(result.Text())
	return parseResponse(text)
}
