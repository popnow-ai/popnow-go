package popnow

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The README is this package's front page. The Go in it is not illustration: it is the first call
// most people will write, and they will copy it wholesale. Nothing else compiles Markdown, so a
// renamed type or a changed signature would leave the README broken with CI still green, and the
// first person to notice would be a new user at the moment they form their impression of the SDK.
//
// It is a *_test.go rather than a script because CI runs exactly gofmt, go vet and go test; a check
// that nothing executes is a check switched off.

// readmeSnippet is the compilation context for one Go block in the README.
type readmeSnippet struct {
	// name appears in failure messages.
	name string
	// anchor is text unique to that block. It ties this table entry to that block, so reordering or
	// inserting blocks is reported instead of compiling the wrong code against the wrong context.
	anchor string
	// program marks a block that is a complete package main, compiled as written.
	program bool
	// prelude supplies what a fragment assumes already exists. The README reads continuously,
	// each section carrying on with the previous one's variables.
	prelude string
	// sink consumes variables a fragment declares but does not go on to use.
	sink string
}

// readmeSnippets registers every Go block in the README, in order. Adding a block without
// registering it turns the check red rather than silently leaving that block unchecked.
var readmeSnippets = []readmeSnippet{
	{name: "Quickstart", anchor: "fox-teacup-1", program: true},
	{name: "What it covers", anchor: "client.ListModels(ctx"},
	{name: "Show the price first", anchor: "client.Quote(ctx, request)"},
	{
		name:   "Skip the first poll",
		anchor: "WaitSeconds: &seconds",
		prelude: "request := popnow.CreateTaskInput{Model: \"m\", Input: map[string]any{\"prompt\": \"p\"}}\n" +
			"key := \"job-1\"\n",
	},
	{
		name:    "Start from a local file",
		anchor:  "client.UploadFile",
		prelude: "request := popnow.CreateTaskInput{Model: \"m\", Input: map[string]any{}}\n",
		sink:    "_ = request",
	},
	{
		name:    "Keep results in the asset library",
		anchor:  "client.CreateAsset",
		prelude: "final := &popnow.Task{TaskID: \"job_1\"}\n",
	},
	{name: "Webhooks instead of polling", anchor: "popnow.VerifyWebhook", program: true},
	{name: "Balance, usage and history", anchor: "client.Tasks(ctx"},
	{name: "Errors and retries", anchor: "var apiErr *popnow.APIError"},
}

var readmeGoFence = regexp.MustCompile("(?ms)^```go[^\n]*\n(.*?)^```$")

// snippetHarness gives a fragment a function body to compile inside. The fragment sits in an inner
// block so a short declaration such as `client, err := popnow.New(...)` holds as written instead
// of colliding with the identically named parameters.
const snippetHarness = `package main

import (
	"context"
	"errors"
	"log"

	popnow "github.com/popnow-ai/popnow-go"
)

// Imports a fragment does not use are consumed here, so "imported and not used" does not drown
// out the errors this check exists to surface.
var (
	_ = context.Background
	_ = errors.As
	_ = log.Println
	_ = popnow.APIBaseURL
)

func snippet(ctx context.Context, client *popnow.Client, err error) error {
	{
@@BODY@@
	}
	return nil
}

func main() { _ = snippet }
`

// TestREADMEGoBlocksCompile compiles every Go block in the README.
func TestREADMEGoBlocksCompile(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain on PATH; this check needs one to compile the README")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("locate the repository: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}

	matches := readmeGoFence.FindAllStringSubmatch(string(readme), -1)
	if len(matches) != len(readmeSnippets) {
		t.Fatalf("README.md has %d Go code block(s) but readmeSnippets declares %d.\n"+
			"every Go block must be registered there, or it goes uncompiled and nothing says so",
			len(matches), len(readmeSnippets))
	}

	for i, match := range matches {
		declared := readmeSnippets[i]
		block := match[1]
		if !strings.Contains(block, declared.anchor) {
			t.Errorf("Go block #%d does not contain the anchor %q declared for %q; "+
				"the blocks and readmeSnippets are out of step", i+1, declared.anchor, declared.name)
			continue
		}
		if declared.program != strings.HasPrefix(block, "package main") {
			t.Errorf("Go block #%d (%q): program=%v does not match the block", i+1, declared.name, declared.program)
			continue
		}
		source := block
		if !declared.program {
			body := indent(declared.prelude) + "\n" + indent(block) + "\n" + indent(declared.sink)
			source = strings.Replace(snippetHarness, "@@BODY@@", body, 1)
		}
		t.Run(declared.name, func(t *testing.T) {
			compileSnippet(t, root, source)
		})
	}
}

// compileSnippet builds one source file inside a throwaway module that replaces back to this
// repository.
func compileSnippet(t *testing.T, root, source string) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, contents string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("go.mod", "module popnowreadmecheck\n\ngo 1.23\n\n"+
		"require github.com/popnow-ai/popnow-go v0.0.0\n\n"+
		"replace github.com/popnow-ai/popnow-go => "+root+"\n")
	write("main.go", source)

	// go vet builds the package and also catches mistakes a plain build accepts (a wrong Printf
	// verb, for one). GOPROXY=off and GOWORK=off keep the check off the network and out of reach
	// of any enclosing go.work.
	vet := exec.Command("go", "vet", "./...")
	vet.Dir = dir
	vet.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off")
	if output, err := vet.CombinedOutput(); err != nil {
		t.Errorf("this README code block does not compile:\n%s\n"+
			"fix the README against the real signatures, not the other way round",
			strings.TrimSpace(string(output)))
	}
}

// indent shifts a fragment to the harness's nesting level while keeping blank lines blank.
func indent(block string) string {
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = "\t\t" + line
	}
	return strings.Join(lines, "\n")
}
