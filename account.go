package popnow

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Balance is what this account can spend right now, in credits and in USD
// decimal strings. Amounts are never floats: cents do not survive binary
// floating point.
type Balance struct {
	// AvailableCredits is what a new task can draw on (rounded down to a whole
	// credit), HeldCredits what accepted tasks have already reserved, and
	// TotalCredits the two together.
	AvailableCredits int64 `json:"availableCredits"`
	HeldCredits      int64 `json:"heldCredits"`
	TotalCredits     int64 `json:"totalCredits"`
	// Available, Held and Total are the same amounts in USD.
	Available string `json:"available"`
	Held      string `json:"held"`
	Total     string `json:"total"`
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

// GetBalance reads the available, held and total balance of this account.
// API calls and generations on popnow.ai share this one wallet.
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
	Credits   int64  `json:"credits"`
	Spend     string `json:"spend"`
}

// UsageModel is one model's share of the same window.
type UsageModel struct {
	Model     string `json:"model"`
	Calls     int64  `json:"calls"`
	Succeeded int64  `json:"succeeded"`
	Failed    int64  `json:"failed"`
	Credits   int64  `json:"credits"`
	Spend     string `json:"spend"`
}

// UsageReport is settled spend and call counts over one window.
type UsageReport struct {
	// From and To echo the window actually used. Read them rather than
	// assuming your own: left empty, the server picks.
	From     string `json:"from"`
	To       string `json:"to"`
	Currency string `json:"currency"`
	// TotalCredits and TotalSpend count settled charges only, never holds.
	TotalCalls   int64  `json:"totalCalls"`
	TotalCredits int64  `json:"totalCredits"`
	TotalSpend   string `json:"totalSpend"`
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
