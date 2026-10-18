package popnow

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Asset sources accepted by CreateAsset and reported on Asset.Source.
const (
	AssetSourceTaskOutput = "task_output"
	AssetSourceUpload     = "upload"
	AssetSourceURL        = "url"
)

// Asset is one file in the long-term private asset library.
//
// The library is decided by the key: a personal key reaches your personal
// library, a team workspace key reaches that team's shared library. Saved
// files are kept until they are deleted.
type Asset struct {
	ID string `json:"id"`
	// URI is the stable popnow://a/ast_... reference. It works in any media
	// field of a model input, for keys of the same workspace, and is never a
	// downloadable link. Persist this, not PreviewURL.
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	ContentType string `json:"contentType"`
	Bytes       int64  `json:"bytes"`
	// Width, Height and DurationSeconds are measured by the server, 0 when
	// they do not apply.
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	// Source is task_output, upload or url; SourceID is your task or upload
	// id, or an opaque digest of the address for url.
	Source    string    `json:"source"`
	SourceID  string    `json:"sourceId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// PreviewURL is a private preview signed for at most 20 minutes, empty
	// when signing fails. Do not store or share it.
	PreviewURL          string     `json:"previewUrl"`
	PreviewURLExpiresAt *time.Time `json:"previewUrlExpiresAt,omitempty"`
	// Favorite belongs to the library, so everyone in a team sees the same.
	Favorite bool `json:"favorite"`
	// Model and ModelName name the model that generated the asset; empty for
	// uploads and links.
	Model     string `json:"model"`
	ModelName string `json:"modelName"`
}

// AssetList is one page of the asset library.
type AssetList struct {
	Items []Asset `json:"items"`
	// NextCursor is absent on the last page.
	NextCursor string `json:"nextCursor,omitempty"`
}

// ListAssetsOptions filters and orders the asset library. A zero field means
// "do not filter".
type ListAssetsOptions struct {
	// Kind is image, video or audio.
	Kind string
	// Sources limits the list to task_output, upload and/or url.
	Sources []string
	// Q is a case-insensitive substring of the asset name.
	Q string
	// Favorite, when non-nil, keeps only favorites (true) or only the rest.
	Favorite *bool
	// CollectionID keeps one collection of the same library.
	CollectionID string
	// EntityID keeps the images of one character.
	EntityID string
	// Sort is recent (the default), oldest, name or size. A cursor only works
	// with the Sort it came from.
	Sort string
	// Cursor is the NextCursor of the previous page.
	Cursor string
	// Limit is the page size, 1 to 100.
	Limit int
}

// ListAssets returns one page of the key's asset library. Listing and reading
// assets is limited to 60 requests per minute per account.
func (c *Client) ListAssets(ctx context.Context, opts ListAssetsOptions) (*AssetList, error) {
	if opts.Limit < 0 {
		return nil, errors.New("popnow: Limit must not be negative")
	}
	query := url.Values{}
	setIfNotEmpty(query, "kind", opts.Kind)
	setIfNotEmpty(query, "source", strings.Join(opts.Sources, ","))
	setIfNotEmpty(query, "q", opts.Q)
	if opts.Favorite != nil {
		query.Set("favorite", strconv.FormatBool(*opts.Favorite))
	}
	setIfNotEmpty(query, "collectionId", opts.CollectionID)
	setIfNotEmpty(query, "entityId", opts.EntityID)
	setIfNotEmpty(query, "sort", opts.Sort)
	setIfNotEmpty(query, "cursor", opts.Cursor)
	if opts.Limit > 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}
	out := &AssetList{}
	if err := c.do(ctx, request{method: http.MethodGet, path: withQuery("/assets", query), replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// EachAsset walks every page of ListAssets and calls fn once per asset,
// repeating every filter and moving only the cursor. Return false from fn to
// stop early.
func (c *Client) EachAsset(ctx context.Context, opts ListAssetsOptions, fn func(Asset) bool) error {
	if fn == nil {
		return errors.New("popnow: fn is required")
	}
	for {
		page, err := c.ListAssets(ctx, opts)
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			if !fn(item) {
				return nil
			}
		}
		if page.NextCursor == "" || page.NextCursor == opts.Cursor {
			return nil
		}
		opts.Cursor = page.NextCursor
	}
}

// Assets is EachAsset as a range-over-func iterator. A failed page yields one
// final (zero, err) pair and ends the sequence.
func (c *Client) Assets(ctx context.Context, opts ListAssetsOptions) iter.Seq2[Asset, error] {
	return func(yield func(Asset, error) bool) {
		stopped := false
		err := c.EachAsset(ctx, opts, func(item Asset) bool {
			if !yield(item, nil) {
				stopped = true
				return false
			}
			return true
		})
		if err != nil && !stopped {
			yield(Asset{}, err)
		}
	}
}

// CreateAssetInput chooses what to save. Set Source and the fields that go
// with it:
//
//   - AssetSourceTaskOutput: TaskID and either Key or Index (the zero-based
//     position in output.assets). The task must be one of your own
//     successful tasks.
//   - AssetSourceUpload: FileID of a committed upload (fil_... or
//     popnow://f/fil_...).
//   - AssetSourceURL: URL, a public http or https address on port 80 or 443
//     without credentials. Only JPEG, PNG, WebP and GIF images, MP4 and WebM
//     video and MP3, WAV, FLAC and Ogg audio are accepted.
type CreateAssetInput struct {
	Source string `json:"source"`
	TaskID string `json:"taskId,omitempty"`
	Key    string `json:"key,omitempty"`
	// Index is a pointer because 0, the first output, is a real value.
	Index  *int   `json:"index,omitempty"`
	FileID string `json:"fileId,omitempty"`
	URL    string `json:"url,omitempty"`
	// Name defaults to "Saved asset" on the server; at most 80 characters.
	Name string `json:"name,omitempty"`
}

func (in CreateAssetInput) validate() error {
	switch in.Source {
	case AssetSourceTaskOutput:
		if strings.TrimSpace(in.TaskID) == "" {
			return errors.New("popnow: TaskID is required for source task_output")
		}
		if in.Key == "" && in.Index == nil {
			return errors.New("popnow: Key or Index is required for source task_output")
		}
		if in.Index != nil && *in.Index < 0 {
			return errors.New("popnow: Index must not be negative")
		}
	case AssetSourceUpload:
		if strings.TrimSpace(in.FileID) == "" {
			return errors.New("popnow: FileID is required for source upload")
		}
	case AssetSourceURL:
		parsed, err := url.Parse(in.URL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return fmt.Errorf("popnow: URL must be a public http(s) address for source url, got %q", in.URL)
		}
	case "":
		return errors.New("popnow: Source is required (task_output, upload or url)")
	default:
		return fmt.Errorf("popnow: unknown Source %q; use task_output, upload or url", in.Source)
	}
	return nil
}

// CreateAsset copies a file into the key's asset library and returns it with
// its stable popnow://a/ URI. Saving the same source again returns the
// existing asset without using more space.
//
// idempotencyKey is required by the contract and bound to the request for 24
// hours. Storage unlocks with the owner's lifetime top-ups: without one this
// returns 400 with APIError.Reason "asset_library_locked"; see GetAssetUsage.
func (c *Client) CreateAsset(ctx context.Context, input CreateAssetInput, idempotencyKey string) (*Asset, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	key, err := checkIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	out := &Asset{}
	if err := c.do(ctx, request{
		method:     http.MethodPost,
		path:       "/assets",
		body:       input,
		headers:    map[string]string{"Idempotency-Key": key},
		replayable: true,
		out:        out,
		// A url source is downloaded once inside this call.
		timeout: c.uploadTimeout,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// GetAsset reads one asset with a fresh private preview. An unknown asset and
// one this key's library does not hold return the same 404.
func (c *Client) GetAsset(ctx context.Context, assetID string) (*Asset, error) {
	if err := required("assetID", assetID); err != nil {
		return nil, err
	}
	out := &Asset{}
	if err := c.do(ctx, request{
		method: http.MethodGet, path: "/assets/" + url.PathEscape(assetID), replayable: true, out: out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateAssetInput renames an asset or marks it as a favorite. Only what is
// set changes; both are pointers so that "leave alone" and "clear" differ.
type UpdateAssetInput struct {
	Name     *string `json:"name,omitempty"`
	Favorite *bool   `json:"favorite,omitempty"`
}

// UpdateAsset renames an asset or changes its favorite mark. The URI stays the
// same and nothing is copied. Repeating the same request is safe.
func (c *Client) UpdateAsset(ctx context.Context, assetID string, input UpdateAssetInput) (*Asset, error) {
	if err := required("assetID", assetID); err != nil {
		return nil, err
	}
	if input.Name == nil && input.Favorite == nil {
		return nil, errors.New("popnow: set Name, Favorite or both")
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		return nil, errors.New("popnow: Name must not be empty")
	}
	out := &Asset{}
	if err := c.do(ctx, request{
		method:     http.MethodPatch,
		path:       "/assets/" + url.PathEscape(assetID),
		body:       input,
		replayable: true,
		out:        out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// AssetDeleted confirms DeleteAsset.
type AssetDeleted struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// DeleteAsset removes an asset from the library and every collection; its URI
// stops working in new requests. Deleting an unknown or already deleted asset
// returns the same result, so retries are safe.
func (c *Client) DeleteAsset(ctx context.Context, assetID string) (*AssetDeleted, error) {
	if err := required("assetID", assetID); err != nil {
		return nil, err
	}
	out := &AssetDeleted{}
	if err := c.do(ctx, request{
		method: http.MethodDelete, path: "/assets/" + url.PathEscape(assetID), replayable: true, out: out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// AssetPlan is one storage plan of the asset library. Byte values use binary
// units.
type AssetPlan struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MinTopupUsd  string `json:"minTopupUsd"`
	MaxBytes     int64  `json:"maxBytes"`
	MaxCount     int64  `json:"maxCount"`
	MaxFileBytes int64  `json:"maxFileBytes"`
}

// AssetScope says which library the key reaches and what it may do there.
type AssetScope struct {
	WorkspaceID string `json:"workspaceId"`
	Personal    bool   `json:"personal"`
	Role        string `json:"role"`
	CanWrite    bool   `json:"canWrite"`
	CanTopUp    bool   `json:"canTopUp"`
}

// AssetQuota is the asset library plan, usage and limits.
type AssetQuota struct {
	Tier  AssetPlan `json:"tier"`
	Usage struct {
		Count int64 `json:"count"`
		Bytes int64 `json:"bytes"`
	} `json:"usage"`
	Limits struct {
		Tier           string `json:"tier"`
		MaxCount       int64  `json:"maxCount"`
		MaxBytes       int64  `json:"maxBytes"`
		MaxFileBytes   int64  `json:"maxFileBytes"`
		GlobalMaxBytes int64  `json:"globalMaxBytes"`
		NameMaxChars   int    `json:"nameMaxChars"`
	} `json:"limits"`
	// Full is true when no new asset fits.
	Full bool `json:"full"`
	// NextTier is nil on the highest plan.
	NextTier *AssetPlan `json:"nextTier"`
	// TopupToNextTierUsd and LifetimeTopupUsd are empty when Scope.CanTopUp
	// is false.
	TopupToNextTierUsd string      `json:"topupToNextTierUsd,omitempty"`
	LifetimeTopupUsd   string      `json:"lifetimeTopupUsd,omitempty"`
	Tiers              []AssetPlan `json:"tiers"`
	DailyCreateLimit   int64       `json:"dailyCreateLimit"`
	CreatedToday       int64       `json:"createdToday"`
	Scope              AssetScope  `json:"scope"`
}

// GetAssetUsage reports the asset library plan, how much is stored, its limits
// and what the next plan unlocks.
func (c *Client) GetAssetUsage(ctx context.Context) (*AssetQuota, error) {
	out := &AssetQuota{}
	if err := c.do(ctx, request{method: http.MethodGet, path: "/assets/usage", replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// Character is a named, ordered set of images in the asset library. It
// references those assets instead of copying them. Characters are created and
// edited on popnow.ai; the API only reads them.
type Character struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Note         string `json:"note"`
	CoverAssetID string `json:"coverAssetId"`
	// Cover is nil while the character has no images.
	Cover *Asset `json:"cover"`
	// Assets are the character's images in order.
	Assets    []Asset   `json:"assets"`
	Count     int       `json:"count"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CharacterList is one page of characters.
type CharacterList struct {
	Items []Character `json:"items"`
	// NextCursor is absent on the last page.
	NextCursor string `json:"nextCursor,omitempty"`
}

// ListCharactersOptions filters the character list.
type ListCharactersOptions struct {
	// AssetID keeps only the characters that contain this asset.
	AssetID string
	Cursor  string
	// Limit is the page size, 1 to 100.
	Limit int
}

// ListCharacters returns one page of the characters in the key's asset
// library, newest first.
func (c *Client) ListCharacters(ctx context.Context, opts ListCharactersOptions) (*CharacterList, error) {
	if opts.Limit < 0 {
		return nil, errors.New("popnow: Limit must not be negative")
	}
	query := url.Values{}
	setIfNotEmpty(query, "assetId", opts.AssetID)
	setIfNotEmpty(query, "cursor", opts.Cursor)
	if opts.Limit > 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}
	out := &CharacterList{}
	if err := c.do(ctx, request{
		method: http.MethodGet, path: withQuery("/assets/entities", query), replayable: true, out: out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// GetCharacter reads one character with its images in order, each with a
// fresh private preview.
func (c *Client) GetCharacter(ctx context.Context, characterID string) (*Character, error) {
	if err := required("characterID", characterID); err != nil {
		return nil, err
	}
	out := &Character{}
	if err := c.do(ctx, request{
		method: http.MethodGet, path: "/assets/entities/" + url.PathEscape(characterID), replayable: true, out: out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}
