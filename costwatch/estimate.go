package costwatch

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// RandomEstimator invents a price. It is the demo's stand-in for a real
// lookup: enough to show values arriving in the data model.
type RandomEstimator struct{}

func (RandomEstimator) Estimate(context.Context, Part) (Estimate, error) {
	return Estimate{Price: randomPrice(), Note: "random guess"}, nil
}

// randomPrice is between $0.50 and $250, in whole cents.
func randomPrice() float64 {
	return math.Round((0.5+rand.Float64()*249.5)*100) / 100
}

// claudeModel is the model that reads the search results. Sonnet is
// what the demo asked for.
const claudeModel = "claude-sonnet-5-5"

const claudeSystem = `You estimate what one unit of a mechanical or electrical part costs to buy, for a bill of materials.

You are given what a CAD document records about the part: its name, description, part number and material. Search the web for the part or its closest purchasable equivalent and read the prices suppliers list for a single unit. When listings disagree, prefer a typical single-unit price from a mainstream supplier over the cheapest bulk price. When nothing matching is for sale, estimate from comparable parts, the material and the likely manufacturing process, and say that you did.

The fields are often sparse or generic, such as a document called "Untitled" with no description. Still give your best single number; a rough estimate is more useful here than no answer.

End your reply with exactly these two lines and nothing after them:
PRICE_USD: <number, no currency symbol or thousands separator>
SOURCE: <the supplier or page the price came from, or "estimate" with a few words on its basis>`

// ClaudeEstimator has Claude search the web for the part and read a
// unit price out of the results.
type ClaudeEstimator struct {
	client anthropic.Client
}

// NewClaudeEstimator builds an estimator. An empty key leaves the SDK
// to find credentials itself (ANTHROPIC_API_KEY and so on).
func NewClaudeEstimator(apiKey string) *ClaudeEstimator {
	if apiKey == "" {
		return &ClaudeEstimator{client: anthropic.NewClient()}
	}
	return &ClaudeEstimator{client: anthropic.NewClient(option.WithAPIKey(apiKey))}
}

// maxContinuations bounds how often a paused server-tool turn is resumed.
const maxContinuations = 4

func (c *ClaudeEstimator) Estimate(ctx context.Context, p Part) (Estimate, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     claudeModel,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: claudeSystem}},
		// A refusal from the safety classifiers is re-served by the
		// default fallback model within the same call.
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortLow,
		},
		Tools: []anthropic.BetaToolUnionParam{
			{OfWebSearchTool20260209: &anthropic.BetaWebSearchTool20260209Param{MaxUses: anthropic.Int(3)}},
		},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(partPrompt(p))),
		},
	}

	for i := 0; ; i++ {
		resp, err := c.client.Beta.Messages.New(ctx, params)
		if err != nil {
			return Estimate{}, claudeError(err)
		}
		switch resp.StopReason {
		case anthropic.BetaStopReasonRefusal:
			return Estimate{}, fmt.Errorf("Claude declined the request (%s)", resp.StopDetails.Category)
		case anthropic.BetaStopReasonPauseTurn:
			// The server paused a long search turn; send it back to go on.
			if i >= maxContinuations {
				return Estimate{}, errors.New("Claude did not finish the search")
			}
			params.Messages = append(params.Messages, resp.ToParam())
			continue
		}
		var text strings.Builder
		for _, block := range resp.Content {
			if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
				text.WriteString(t.Text)
			}
		}
		return parseEstimate(text.String())
	}
}

func partPrompt(p Part) string {
	field := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "(none)"
		}
		return s
	}
	return fmt.Sprintf("Name: %s\nDescription: %s\nPart number: %s\nMaterial: %s",
		field(p.Name), field(p.Description), field(p.PartNumber), field(p.Material))
}

var (
	priceRE  = regexp.MustCompile(`(?m)^\s*PRICE_USD:\s*\$?\s*([0-9][0-9,]*(?:\.[0-9]+)?)`)
	sourceRE = regexp.MustCompile(`(?m)^\s*SOURCE:\s*(.+?)\s*$`)
)

// parseEstimate reads the two closing lines the system prompt asks for.
func parseEstimate(text string) (Estimate, error) {
	m := priceRE.FindAllStringSubmatch(text, -1)
	if len(m) == 0 {
		return Estimate{}, errors.New("Claude's reply had no PRICE_USD line")
	}
	price, err := strconv.ParseFloat(strings.ReplaceAll(m[len(m)-1][1], ",", ""), 64)
	if err != nil || price <= 0 {
		return Estimate{}, fmt.Errorf("Claude's reply had an unusable price %q", m[len(m)-1][1])
	}
	note := "Claude web search"
	if s := sourceRE.FindAllStringSubmatch(text, -1); len(s) > 0 {
		note = "Claude: " + s[len(s)-1][1]
	}
	return Estimate{Price: math.Round(price*100) / 100, Note: note}, nil
}

// claudeError names the failures a user can act on.
func claudeError(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 401, 403:
			return fmt.Errorf("the Anthropic API key was rejected (HTTP %d)", apiErr.StatusCode)
		case 429:
			return errors.New("the Anthropic API rate limit was reached")
		default:
			return fmt.Errorf("Anthropic API error (HTTP %d): %w", apiErr.StatusCode, err)
		}
	}
	return fmt.Errorf("Anthropic API request failed: %w", err)
}

// WithRandomFallback prices with primary and, when that fails, with a
// random guess whose note says why.
func WithRandomFallback(primary Estimator) Estimator { return fallback{primary} }

type fallback struct{ primary Estimator }

func (f fallback) Estimate(ctx context.Context, p Part) (Estimate, error) {
	est, err := f.primary.Estimate(ctx, p)
	if err == nil || ctx.Err() != nil {
		return est, err
	}
	return Estimate{Price: randomPrice(), Note: fmt.Sprintf("random guess; %v", err)}, nil
}
