package popnow

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Balance is the API balance behind this key: an independent USD wallet,
// separate from the credits used on popnow.ai. Amounts are exact USD decimal
// strings with up to nine decimal places, never floats: binary floating point
// does not hold them exactly.
type Balance struct {
	// AvailableUsd is what a new task can draw on, HeldUsd what accepted
	// tasks have already reserved, and TotalUsd the two together.
	// AvailableUsd can be negative on an account with approved credit.
	AvailableUsd string `json:"availableUsd"`
	HeldUsd      string `json:"heldUsd"`
	TotalUsd     string `json:"totalUsd"`
	// Available, Held and Total are older names for the same USD amounts.
	Available string `json:"available"`
	Held      string `json:"held"`
	Total     string `json:"total"`

	// AvailableCredits is AvailableUsd truncated to whole credits at
	// 1 credit = $0.001, kept for display compatibility only.
	//
	// Deprecated: use AvailableUsd.
	AvailableCredits int64 `json:"availableCredits"`
	// HeldCredits is HeldUsd rounded up to whole credits.
	//
	// Deprecated: use HeldUsd.
	HeldCredits int64 `json:"heldCredits"`
	// TotalCredits is the sum of the two credit figures, not an exact
	// balance.
	//
	// Deprecated: use TotalUsd.
	TotalCredits int64 `json:"totalCredits"`
	// Funding breaks the balance down by where the money came from. It is
	// absent on accounts with nothing but a plain wallet.
	Funding *FundingOverview `json:"funding,omitempty"`
}

// FundingOverview separates the sources behind a balance, because they do not
// behave alike: a grant can be restricted to certain models and credit is an
// approved ceiling rather than money on hand.
type FundingOverview struct {
	// BalanceUsd is the net wallet balance. It can be negative.
	BalanceUsd string `json:"balanceUsd"`
	HeldUsd    string `json:"heldUsd"`
	// PrepaidAvailableUsd is unrestricted; GrantAvailableUsd may not pay for
	// every model, which is decided when a task is admitted.
	PrepaidAvailableUsd string `json:"prepaidAvailableUsd"`
	GrantAvailableUsd   string `json:"grantAvailableUsd"`
	// CashShortfallUsd is a payment recovery gap. Non-zero blocks new tasks.
	CashShortfallUsd string          `json:"cashShortfallUsd"`
	Credit           *CreditFacility `json:"credit,omitempty"`
	Grants           []FundingGrant  `json:"grants,omitempty"`
	// GrantsHasMore says Grants was truncated.
	GrantsHasMore bool `json:"grantsHasMore"`
}

// CreditFacility is approved pay-later capacity. LimitUsd is a ceiling, not
// funds.
type CreditFacility struct {
	Enabled      bool       `json:"enabled"`
	LimitUsd     string     `json:"limitUsd"`
	AvailableUsd string     `json:"availableUsd"`
	UsedUsd      string     `json:"usedUsd"`
	HeldUsd      string     `json:"heldUsd"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	Status       string     `json:"status"`
	Version      int64      `json:"version"`
}

// FundingGrant is one promotional grant.
type FundingGrant struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	AmountUsd    string     `json:"amountUsd"`
	AvailableUsd string     `json:"availableUsd"`
	HeldUsd      string     `json:"heldUsd"`
	SpentUsd     string     `json:"spentUsd"`
	Status       string     `json:"status"`
	StartsAt     time.Time  `json:"startsAt"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	// ModelSlugs restricts this grant. Empty means every model, not none.
	ModelSlugs   []string  `json:"modelSlugs,omitempty"`
	CustomerMemo string    `json:"customerMemo"`
	CreatedAt    time.Time `json:"createdAt"`
}

// GetBalance reads the available, held and total USD of the API balance.
//
// Only API funds count here. API-key calls, the developer MCP and API
// Playground runs spend this balance; generations on popnow.ai spend website
// credits, which are separate and cannot be moved into it. Add funds at
// https://popnow.ai/api/console/billing. The path name /chat/credit is
// historical.
func (c *Client) GetBalance(ctx context.Context) (*Balance, error) {
	out := &Balance{}
	if err := c.do(ctx, request{method: http.MethodGet, path: "/chat/credit", replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// UsageDay is one day of activity. Day is a UTC calendar date, YYYY-MM-DD.
type UsageDay struct {
	Day       string `json:"day"`
	Calls     int64  `json:"calls"`
	Succeeded int64  `json:"succeeded"`
	Failed    int64  `json:"failed"`
	// Spend is the settled USD charge, an exact decimal string.
	Spend string `json:"spend"`
	// Credits is Spend rounded up to whole credits at 1 credit = $0.001.
	//
	// Deprecated: use Spend.
	Credits int64 `json:"credits"`
}

// UsageModel is one model's share of the same window.
type UsageModel struct {
	Model     string `json:"model"`
	Calls     int64  `json:"calls"`
	Succeeded int64  `json:"succeeded"`
	Failed    int64  `json:"failed"`
	// Spend is the settled USD charge, an exact decimal string.
	Spend string `json:"spend"`
	// Credits is Spend rounded up to whole credits at 1 credit = $0.001.
	//
	// Deprecated: use Spend.
	Credits int64 `json:"credits"`
}

// UsageReport is settled spend and call counts over one window.
type UsageReport struct {
	// From and To echo the window actually used. Read them rather than
	// assuming your own: left empty, the server picks.
	From     string `json:"from"`
	To       string `json:"to"`
	Currency string `json:"currency"`
	// TotalSpend is the settled USD spend, an exact decimal string. It
	// counts settled charges only, never holds.
	TotalCalls int64  `json:"totalCalls"`
	TotalSpend string `json:"totalSpend"`
	// TotalCredits is TotalSpend rounded up to whole credits at
	// 1 credit = $0.001.
	//
	// Deprecated: use TotalSpend.
	TotalCredits int64 `json:"totalCredits"`
	// Days and Models are sparse: a day or a model with no activity is absent.
	Days   []UsageDay   `json:"days"`
	Models []UsageModel `json:"models"`
}

// UsageOptions bounds a usage query to a half-open UTC interval [From, To) of
// at most 92 days, both as YYYY-MM-DD. Left empty the server uses the last
// seven days.
type UsageOptions struct {
	From string
	To   string
}

// GetUsage reports this API key's settled spend and call counts.
//
// Numbers are attributed to when a task was created, and settlement can land
// late, so a figure read today can still change tomorrow. This endpoint sits
// behind an account-wide bucket of thirty requests a minute shared by every
// key, and that limiter fails closed: follow tasks with GetTask or
// WaitForTerminal, never by polling usage.
func (c *Client) GetUsage(ctx context.Context, opts UsageOptions) (*UsageReport, error) {
	query := url.Values{}
	setIfNotEmpty(query, "from", opts.From)
	setIfNotEmpty(query, "to", opts.To)
	out := &UsageReport{}
	if err := c.do(ctx, request{method: http.MethodGet, path: withQuery("/usage", query), replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}
