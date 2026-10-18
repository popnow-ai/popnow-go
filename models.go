package popnow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// ModelCredits is a model's price in credits (1 credit = $0.01), the same
// price popnow.ai shows. Use Quote for the exact amount of one request.
type ModelCredits struct {
	// Unit is how the price scales: per_image, per_second, per_request, ...
	// Treat it as open.
	Unit string `json:"unit"`
	// PerUnit is the starting price per unit. It can be fractional.
	PerUnit float64 `json:"perUnit"`
	// Rules lists the price tiers.
	Rules []ModelCreditRule `json:"rules,omitempty"`
}

// ModelCreditRule is one price tier: it applies when the request matches its
// When values, where "*" matches the field that sets the tier.
type ModelCreditRule struct {
	When    map[string]any `json:"when"`
	PerUnit float64        `json:"perUnit"`
	Unit    string         `json:"unit,omitempty"`
}

// ModelExample is an input that validates against the model's current schema.
type ModelExample struct {
	ID         string         `json:"id"`
	Input      map[string]any `json:"input"`
	SortWeight int            `json:"sortWeight"`
}

// Model is one callable endpoint from the catalogue.
type Model struct {
	// Model is the exact call name to pass as CreateTaskInput.Model, in
	// <publisher>/<model>/<task> form.
	Model  string `json:"model"`
	Family string `json:"family"`
	// DisplayName is for your UI, not for calls.
	DisplayName string `json:"displayName"`
	// Provider is the model's creator, the lab that built it.
	Provider string `json:"provider"`
	// Modality is image, video or audio.
	Modality string `json:"modality"`
	// Tasks holds exactly one classification and is not always the suffix of
	// Model: editing endpoints publish as .../edit and classify as
	// image-to-image (or image-edit for single-purpose tools). Call with
	// Model; group with Tasks. The set is open.
	Tasks []string `json:"tasks"`
	Async bool     `json:"async"`
	// Toolkit groups related endpoints for display (subject-swap,
	// studio-tools). Empty when the model belongs to no toolkit.
	Toolkit string `json:"toolkit,omitempty"`
	// TaskTimeoutSeconds is the platform execution deadline, not the length
	// of the media that comes out.
	TaskTimeoutSeconds int `json:"taskTimeoutSeconds"`
	// MaxOutputDurationSeconds is the longest output the schema allows, 0
	// when the model declares none.
	MaxOutputDurationSeconds int `json:"maxOutputDurationSeconds,omitempty"`
	// Enabled and Available both have to be true before a model can be called.
	Enabled   bool `json:"enabled"`
	Available bool `json:"available"`
	// QuantityField names the input field that multiplies the price.
	QuantityField string        `json:"quantityField"`
	Credits       *ModelCredits `json:"credits,omitempty"`
	// InputSchema is JSON Schema 2020-12 and stays raw on purpose: it is what
	// validates your input and what a form generator renders. Request it with
	// ListModelsOptions.IncludeSchema or read it from GetModel.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	// Version changes whenever the schema or metadata changes; cache on it.
	Version       string         `json:"version"`
	Availability  string         `json:"availability"`
	Badges        []string       `json:"badges"`
	RelatedModels []string       `json:"relatedModels"`
	Examples      []ModelExample `json:"examples,omitempty"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}

// ModelList is the ListModels response.
type ModelList struct {
	Total int     `json:"total"`
	Items []Model `json:"items"`
}

// ListModelsOptions filters the catalogue. A zero field means "do not filter".
type ListModelsOptions struct {
	// Modality is image, video or audio.
	Modality string
	// Provider is the model's creator.
	Provider string
	// Task is an exact task, for example text-to-image or image-to-video.
	// "edit" and "image-to-image" select the same editing endpoints.
	Task string
	// Search matches names and descriptions.
	Search string
	// IncludeSchema adds each model's input JSON Schema. It makes the
	// response much larger.
	IncludeSchema bool
	// IncludeExamples adds inputs that already validate against that schema,
	// the fastest way to get a first call working.
	IncludeExamples bool
}

// ListModels returns every image, video and audio model this API key may
// call, with its price in credits. The list is not paginated.
func (c *Client) ListModels(ctx context.Context, opts ListModelsOptions) (*ModelList, error) {
	query := url.Values{}
	setIfNotEmpty(query, "modality", opts.Modality)
	setIfNotEmpty(query, "provider", opts.Provider)
	setIfNotEmpty(query, "task", opts.Task)
	setIfNotEmpty(query, "search", opts.Search)
	if opts.IncludeSchema {
		query.Set("includeSchema", "1")
	}
	if opts.IncludeExamples {
		query.Set("includeExamples", "1")
	}

	out := &ModelList{}
	if err := c.do(ctx, request{method: http.MethodGet, path: withQuery("/models", query), replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// GetModel reads one model, always including its input schema.
//
// A model identifier contains slashes, so it is escaped into a single path
// segment here; sending them raw would address a different resource.
func (c *Client) GetModel(ctx context.Context, model string) (*Model, error) {
	if err := required("model", model); err != nil {
		return nil, err
	}
	out := &Model{}
	path := "/models/" + url.PathEscape(model)
	if err := c.do(ctx, request{method: http.MethodGet, path: path, replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}
