package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// The manual is generated from the commands, so the commands are where its
// completeness is enforced: a command without a Long or two examples, or a
// flag without a description, fails here rather than shipping as a thin page.

func documentedCommands(t *testing.T) (commands, topics []*cobra.Command) {
	t.Helper()
	root, _ := NewRootCommand()
	walkDocumented(root, func(cmd *cobra.Command) {
		if isTopic(cmd) {
			topics = append(topics, cmd)
		} else {
			commands = append(commands, cmd)
		}
	})
	if len(commands) < 10 || len(topics) < 3 {
		t.Fatalf("walked %d commands and %d topics; the walk is broken", len(commands), len(topics))
	}
	return commands, topics
}

// exampleCount is the number of command lines in an Example: non-empty lines
// that are not `#` comments.
func exampleCount(example string) int {
	n := 0
	for _, line := range strings.Split(example, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}

func TestEveryCommandIsDocumented(t *testing.T) {
	commands, topics := documentedCommands(t)
	for _, cmd := range commands {
		path := cmd.CommandPath()
		switch {
		case strings.TrimSpace(cmd.Short) == "":
			t.Errorf("%s has no Short", path)
		case strings.HasSuffix(cmd.Short, "."):
			t.Errorf("%s: Short is one line with no full stop: %q", path, cmd.Short)
		case len(cmd.Short) > 160:
			t.Errorf("%s: Short is the page's description and is cut at 160 characters", path)
		}
		if strings.TrimSpace(cmd.Long) == "" {
			t.Errorf("%s has no Long: say what it does, what it changes on the server, and what it never does", path)
		}
		if n := exampleCount(cmd.Example); n < 2 {
			t.Errorf("%s has %d example(s); it needs at least two, with real arguments", path, n)
		}
		check := func(f *pflag.Flag) {
			if strings.TrimSpace(f.Usage) == "" {
				t.Errorf("%s: flag --%s has no description", path, f.Name)
			}
		}
		cmd.LocalNonPersistentFlags().VisitAll(check)
		cmd.PersistentFlags().VisitAll(check)
	}
	for _, topic := range topics {
		if strings.TrimSpace(topic.Short) == "" || strings.TrimSpace(topic.Long) == "" {
			t.Errorf("help topic %s needs a Short and a Long", topic.Name())
		}
	}
}

// helpOf runs `lt <path> --help` and returns what it printed.
func helpOf(t *testing.T, path []string) string {
	t.Helper()
	root, e := NewRootCommand()
	var out bytes.Buffer
	e.out, e.err = &out, &out
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append(append([]string{}, path...), "--help"))
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("lt %s --help: %v", strings.Join(path, " "), err)
	}
	return out.String()
}

// Each page is the same text as the command's --help: its description, every
// example line and every flag line appear in both.
func TestEveryPageMatchesItsHelp(t *testing.T) {
	root, _ := NewRootCommand()
	pages := map[string]string{}
	for _, p := range referencePages(root, defaultDocsBase) {
		pages[p.file] = p.content
	}

	commands, topics := documentedCommands(t)
	for _, cmd := range append(commands, topics...) {
		page, ok := pages[pageName(cmd)+".md"]
		if !ok {
			t.Errorf("no page for %s", cmd.CommandPath())
			continue
		}
		path := strings.Fields(cmd.CommandPath())[1:]
		help := helpOf(t, path)
		for _, line := range strings.Split(help, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasSuffix(line, ":") || strings.HasPrefix(line, "Use \"lt") ||
				strings.HasPrefix(line, "lt ") && strings.Contains(line, "[command]") {
				continue
			}
			// The command and topic lists are links on the page; their Short
			// is what both share. cobra's own help and completion have no page.
			fields := strings.Fields(line)
			if fields[0] == "help" || fields[0] == "completion" {
				continue
			}
			if listed(page, fields, 1) || listed(page, fields, 2) {
				continue
			}
			if !strings.Contains(page, line) {
				t.Errorf("%s: --help prints %q, which its page does not", cmd.CommandPath(), line)
			}
		}
	}
}

// listed reports whether a --help list line, less its first n fields (the
// name), is a link's description on the page.
func listed(page string, fields []string, n int) bool {
	return len(fields) > n && strings.Contains(page, "— "+strings.Join(fields[n:], " "))
}

// The pages carry the frontmatter the documentation site requires, start
// their sections at ##, and link only to each other.
func TestGeneratedPagesAreValidForTheSite(t *testing.T) {
	root, _ := NewRootCommand()
	pages := referencePages(root, defaultDocsBase)
	commands, topics := documentedCommands(t)
	if want := len(commands) + len(topics) + 1; len(pages) != want {
		t.Errorf("%d pages, want %d (one per command and topic, and the index)", len(pages), want)
	}

	known := map[string]bool{}
	for _, p := range pages {
		known[defaultDocsBase+"/"+strings.TrimSuffix(p.file, ".md")] = true
	}
	frontmatterRE := regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n`)
	linkRE := regexp.MustCompile(`\]\((/[^)\s]*)\)`)
	for _, p := range pages {
		m := frontmatterRE.FindStringSubmatch(p.content)
		if m == nil {
			t.Errorf("%s has no frontmatter block", p.file)
			continue
		}
		for _, key := range []string{"title: \"", "type: ", "description: \""} {
			if !strings.Contains(m[1], key) {
				t.Errorf("%s: frontmatter lacks %s", p.file, strings.TrimSpace(key))
			}
		}
		inFence := false
		for _, line := range strings.Split(p.content[len(m[0]):], "\n") {
			if strings.HasPrefix(line, "```") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			if strings.HasPrefix(line, "# ") {
				t.Errorf("%s has an H1 in the body; the title is the H1", p.file)
			}
			for _, link := range linkRE.FindAllStringSubmatch(line, -1) {
				if !known[link[1]] {
					t.Errorf("%s links to %s, which is no generated page", p.file, link[1])
				}
			}
		}
		if inFence {
			t.Errorf("%s leaves a code fence open", p.file)
		}
	}
}

func TestGenerateRefusesANonEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := writePages(dir, []docPage{{file: "a.md", content: "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := writePages(dir, []docPage{{file: "b.md", content: "y"}}); err == nil {
		t.Error("wrote into a directory that already held pages")
	}
	fresh := filepath.Join(dir, "new", "ref")
	if err := writePages(fresh, []docPage{{file: "b.md", content: "y"}}); err != nil {
		t.Errorf("a new directory should be created: %v", err)
	}
}

// sources returns the text of every non-test Go file in the module.
func sources(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, dir := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(path)
			b.Write(raw)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return b.String()
}

// Every environment variable lt or its installer reads is on the
// environment page.
func TestEveryEnvironmentVariableIsDocumented(t *testing.T) {
	names := map[string]bool{}
	for _, m := range regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("([A-Z_]+)"\)`).FindAllStringSubmatch(sources(t), -1) {
		names[m[1]] = true
	}
	install, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`\$\{?(LT_[A-Z_]+)`).FindAllStringSubmatch(string(install), -1) {
		names[m[1]] = true
	}
	if len(names) < 5 {
		t.Fatalf("found %d variables; the scan is broken", len(names))
	}
	for name := range names {
		if !strings.Contains(environmentTopic, "`"+name) {
			t.Errorf("%s is read but not on `lt help environment`", name)
		}
	}
}

// Every error code the source can emit is on the exit-codes page.
func TestEveryErrorCodeIsDocumented(t *testing.T) {
	codes := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?i)\bcode\w*\s*[:=]\s*"([a-z_]+)"`).FindAllStringSubmatch(sources(t), -1) {
		codes[m[1]] = true
	}
	for _, exit := range []int{ExitAPIError, ExitBadArguments, ExitNeedsConfirm, ExitNotSignedIn} {
		codes[slugFor(exit)] = true
	}
	local, _ := codeAndMessage(errors.New("unreadable"))
	codes[local] = true
	if len(codes) < 15 {
		t.Fatalf("found %d codes; the scan is broken", len(codes))
	}

	page := exitCodesTopic()
	for code := range codes {
		if !strings.Contains(page, "| `"+code+"` |") {
			t.Errorf("%s can be emitted but is not on `lt help exit-codes`", code)
		}
	}
	for _, r := range exitCodeRows {
		if !strings.Contains(page, "| "+string(rune('0'+r.code))+" |") {
			t.Errorf("exit code %d is not on the page", r.code)
		}
	}
}
