package popnow

import (
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestContractHasNotDrifted pins the contract facts hard-coded in this package to the published
// contract.
//
// The enums, error codes and paths in an SDK are constants copied from the contract. When the
// contract changes and these do not, nothing raises an error: requests still go out and responses
// still parse, only some new option is rejected locally as illegal, or some new error code is
// flattened into "unknown". That failure emits no signal, so this test makes one.
//
// It compares against a pinned copy (contracts/openapi.yaml) and, when the network allows, checks
// that the pinned copy still equals https://popnow.ai/docs/openapi.yaml. Both are public, so CI
// needs no credentials. Set POPNOW_SKIP_LIVE_CONTRACT=1 to skip the live comparison.
func TestContractHasNotDrifted(t *testing.T) {
	raw, err := os.ReadFile("contracts/openapi.yaml")
	if err != nil {
		t.Fatalf("read the pinned contract: %v", err)
	}
	local := string(raw)

	if os.Getenv("POPNOW_SKIP_LIVE_CONTRACT") == "" {
		if live, ok := fetchLiveContract(t); ok && live != local {
			t.Fatalf("contracts/openapi.yaml is stale against the published contract.\n" +
				"refresh it:  curl -sH 'User-Agent: popnow-go-contract-check' " +
				"https://popnow.ai/docs/openapi.yaml | sed '/^#/d' > contracts/openapi.yaml\n" +
				"then re-check every constant this package hard-codes against that diff")
		}
	}

	assertEnum(t, local, "TaskRecord", "state", []string{
		StateQueued, StateRunning, StateSucceeded, StateFailed, StateCanceled, StateExpired,
	})
	assertEnum(t, local, "TaskRecord", "errorCode", []string{
		ErrorCodeInvalidRequest, ErrorCodeUnsupportedCombination, ErrorCodeContentRejected,
		ErrorCodeRateLimited, ErrorCodeUpstreamUnavailable, ErrorCodeGenerationFailed,
		ErrorCodeTimeout, ErrorCodeInvalidAsset, ErrorCodeUpstreamFailed,
	})
	assertEnum(t, local, "UploadURLRequest", "contentType", acceptedUploadTypes)
	assertEnum(t, local, "AssetCreateRequest", "source", []string{
		AssetSourceTaskOutput, AssetSourceUpload, AssetSourceURL,
	})

	codes := []int{
		CodeBadRequest, CodeUploadMismatch, CodeUnsupportedCombination, CodeUnauthorized,
		CodeInsufficientCredits, CodeSpendCapReached, CodeForbidden, CodeModelNotAllowed,
		CodeIPNotAllowed, CodeRegionUnavailable, CodeEmailNotVerified, CodeNotFound, CodeConflict,
		CodeQuoteRejected, CodePayloadTooLarge, CodeRateLimited, CodeServerError,
		CodeDependencyUnavailable, CodeModelUnavailable, CodeGenerationRefunded,
	}
	named := make([]string, 0, len(codes))
	for _, code := range codes {
		named = append(named, strconv.Itoa(code))
	}
	assertEnum(t, local, "ErrorEnvelope", "code", named)

	// Every endpoint this package calls must exist in the contract under that method.
	for _, op := range []struct{ method, path string }{
		{"post", "/api/v1/jobs/quote"},
		{"post", "/api/v1/jobs/createTask"},
		{"get", "/api/v1/jobs/recordInfo"},
		{"post", "/api/v1/jobs/retry"},
		{"post", "/api/v1/jobs/purge"},
		{"get", "/api/v1/jobs"},
		{"get", "/api/v1/models"},
		{"get", "/api/v1/models/{model}"},
		{"get", "/api/v1/usage"},
		{"get", "/api/v1/chat/credit"},
		{"post", "/api/v1/common/upload-url"},
		{"post", "/api/v1/files/{fileId}/commit"},
		{"post", "/api/v1/files/import"},
		{"post", "/api/v1/common/download-url"},
		{"get", "/api/v1/assets"},
		{"post", "/api/v1/assets"},
		{"get", "/api/v1/assets/usage"},
		{"get", "/api/v1/assets/{assetId}"},
		{"patch", "/api/v1/assets/{assetId}"},
		{"delete", "/api/v1/assets/{assetId}"},
		{"get", "/api/v1/assets/entities"},
		{"get", "/api/v1/assets/entities/{entityId}"},
	} {
		block, ok := yamlBlock(local, "\n  "+op.path+":\n", "\n  /")
		if !ok {
			t.Errorf("the contract has no path %s", op.path)
			continue
		}
		if !strings.Contains("\n"+block, "\n    "+op.method+":\n") {
			t.Errorf("the contract has no %s %s", strings.ToUpper(op.method), op.path)
		}
	}

	// The two headers this package writes by name.
	for _, name := range []string{"name: Idempotency-Key", "name: X-PopNow-Retention"} {
		if !strings.Contains(local, name) {
			t.Errorf("the contract no longer declares the %q header", strings.TrimPrefix(name, "name: "))
		}
	}
}

// fetchLiveContract retrieves the published contract, minus its leading generated-comment lines,
// which the pinned copy does not keep. A User-Agent is mandatory: the edge refuses requests with no
// agent or a generic default one.
func fetchLiveContract(t *testing.T) (string, bool) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "https://popnow.ai/docs/openapi.yaml", nil)
	if err != nil {
		t.Fatalf("build the contract request: %v", err)
	}
	request.Header.Set("User-Agent", "popnow-go-contract-check/"+Version)

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Logf("the published contract is unreachable (%v); checked the pinned copy only", err)
		return "", false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the published contract answered HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		t.Fatalf("read the published contract: %v", err)
	}
	text := string(body)
	for strings.HasPrefix(text, "#") {
		end := strings.IndexByte(text, '\n')
		if end < 0 {
			return "", true
		}
		text = text[end+1:]
	}
	return text, true
}

// yamlBlock returns the text from start up to the next occurrence of next, so lookups stay inside
// one schema or one path instead of matching a same-named field further down.
func yamlBlock(contract, start, next string) (string, bool) {
	at := strings.Index(contract, start)
	if at < 0 {
		return "", false
	}
	rest := contract[at+len(start):]
	if end := strings.Index(rest, next); end >= 0 {
		rest = rest[:end]
	}
	return rest, true
}

// assertEnum compares a block-style YAML enum under components.schemas.<schema>.<field> with the
// values this package hard-codes.
func assertEnum(t *testing.T, contract, schema, field string, ours []string) {
	t.Helper()
	// Schemas sit at four spaces of indentation; the next line at that depth ends this one.
	start := strings.Index(contract, "\n    "+schema+":\n")
	if start < 0 {
		t.Fatalf("the contract has no %s schema", schema)
	}
	block := untilNextSibling(contract[start+len("\n    "+schema+":\n"):], 4)

	at := -1
	for _, line := range strings.SplitAfter(block, "\n") {
		if strings.TrimSpace(line) == field+":" {
			at = strings.Index(block, line)
			break
		}
	}
	if at < 0 {
		t.Fatalf("%s has no %s property", schema, field)
	}
	fieldBlock := block[at:]
	enumAt := strings.Index(fieldBlock, "enum:\n")
	if enumAt < 0 {
		t.Fatalf("%s.%s has no enum", schema, field)
	}

	var theirs []string
	dashIndent := -1
	for _, line := range strings.Split(fieldBlock[enumAt+len("enum:\n"):], "\n") {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		if !strings.HasPrefix(trimmed, "- ") || (dashIndent >= 0 && indent != dashIndent) {
			break
		}
		dashIndent = indent
		theirs = append(theirs, strings.Trim(strings.TrimSpace(trimmed[2:]), `'"`))
	}
	if len(theirs) == 0 {
		t.Fatalf("%s.%s has an empty enum", schema, field)
	}

	mine := append([]string(nil), ours...)
	sort.Strings(mine)
	sort.Strings(theirs)
	if strings.Join(mine, ",") != strings.Join(theirs, ",") {
		t.Errorf("%s.%s drifted:\n  package:  %v\n  contract: %v", schema, field, mine, theirs)
	}
}

// untilNextSibling cuts a schema body at the first line indented exactly depth spaces, which is
// where the next schema starts.
func untilNextSibling(block string, depth int) string {
	offset := 0
	for _, line := range strings.SplitAfter(block, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if trimmed != "" && trimmed != "\n" && len(line)-len(trimmed) == depth {
			return block[:offset]
		}
		offset += len(line)
	}
	return block
}
