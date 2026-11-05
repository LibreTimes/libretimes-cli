package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// The manual's command reference is generated from the command tree: one
// markdown page per command and per help topic, from the same Short, Long,
// Example and flag text `--help` prints. So the page on the web and the help
// in the terminal are one text, and a new flag cannot ship undocumented
// (help_test.go fails on a flag with no description).
//
// The pages carry the frontmatter the documentation site reads (`title`,
// `type`, `description`, `order`) and link to each other under --base, the
// path the site serves them at.

const defaultDocsBase = "/documentation/cli/reference"

func newDocsCommand(e *env) *cobra.Command {
	docs := &cobra.Command{
		Use:    "docs",
		Short:  "Generate the manual's reference pages from the commands",
		Hidden: true,
		Long: "Generate the command reference of lt's manual from the command tree, " +
			"so the pages and `--help` are one text. For the release workflow and the " +
			"documentation site; not something a user needs.",
		Example: "  lt docs generate dist/docs\n" +
			"  lt docs generate --base /documentation/cli/reference dist/docs",
	}

	var base string
	generate := &cobra.Command{
		Use:   "generate <dir>",
		Short: "Write one markdown page per command and help topic into a new directory",
		Long: "Write the command reference into <dir>: one page per command, one per " +
			"help topic, and an index. <dir> is created, and must be empty if it " +
			"exists, so a page for a command that is gone can never linger. Writes " +
			"files only; contacts nothing.",
		Example: "  lt docs generate dist/docs\n" +
			"  lt docs generate --base /docs/cli dist/docs",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pages := referencePages(cmd.Root(), base)
			if err := writePages(args[0], pages); err != nil {
				return &exitError{code: ExitBadArguments, err: err}
			}
			e.info("wrote %d pages to %s", len(pages), args[0])
			return nil
		},
	}
	generate.Flags().StringVar(&base, "base", defaultDocsBase,
		"The path the pages are served under; links between them are built from it.")
	docs.AddCommand(generate)
	return docs
}

// docPage is one generated file.
type docPage struct {
	file    string // e.g. lt-auth-login.md
	content string
}

// documented reports whether a command gets a reference page. Hidden commands
// do not, nor do cobra's own `help` and `completion`, whose text is cobra's.
func documented(cmd *cobra.Command) bool {
	if cmd.Hidden || cmd.Deprecated != "" {
		return false
	}
	if cmd.HasParent() && !cmd.Parent().HasParent() &&
		(cmd.Name() == "help" || cmd.Name() == "completion") {
		return false
	}
	return true
}

// walkDocumented visits every documented command and topic, depth first, in
// cobra's sorted order.
func walkDocumented(cmd *cobra.Command, visit func(*cobra.Command)) {
	if !documented(cmd) {
		return
	}
	visit(cmd)
	for _, child := range cmd.Commands() {
		walkDocumented(child, visit)
	}
}

func pageName(cmd *cobra.Command) string {
	if isTopic(cmd) {
		return cmd.Name()
	}
	return strings.ReplaceAll(cmd.CommandPath(), " ", "-")
}

func pageTitle(cmd *cobra.Command) string {
	if isTopic(cmd) {
		return "lt help " + cmd.Name()
	}
	return cmd.CommandPath()
}

// referencePages builds every page, the index first.
func referencePages(root *cobra.Command, base string) []docPage {
	base = strings.TrimRight(base, "/")
	link := func(cmd *cobra.Command) string {
		return fmt.Sprintf("[%s](%s/%s)", pageTitle(cmd), base, pageName(cmd))
	}

	var commands, topics []*cobra.Command
	walkDocumented(root, func(cmd *cobra.Command) {
		cmd.InitDefaultHelpFlag()
		if isTopic(cmd) {
			topics = append(topics, cmd)
		} else {
			commands = append(commands, cmd)
		}
	})

	var index strings.Builder
	frontmatterBlock(&index, "Command reference", "overview",
		"Every lt command and help topic, generated from the commands themselves", 0)
	fmt.Fprintf(&index, "cli_version: %s\n---\n\n", Version)
	index.WriteString("Generated from lt's own command definitions, so each page is the " +
		"same text as the command's `--help`.\n\n## Commands\n\n")
	for _, cmd := range commands {
		fmt.Fprintf(&index, "- %s — %s\n", link(cmd), cmd.Short)
	}
	index.WriteString("\n## Help topics\n\n")
	for _, cmd := range topics {
		fmt.Fprintf(&index, "- %s — %s\n", link(cmd), cmd.Short)
	}
	pages := []docPage{{file: "index.md", content: index.String()}}

	for i, cmd := range append(append([]*cobra.Command{}, commands...), topics...) {
		var b strings.Builder
		frontmatterBlock(&b, pageTitle(cmd), "reference", cmd.Short, i+1)
		b.WriteString("---\n\n")
		b.WriteString(strings.TrimSpace(cmd.Long))
		b.WriteString("\n")
		if !isTopic(cmd) {
			writeCommandSections(&b, cmd, link)
		}
		pages = append(pages, docPage{file: pageName(cmd) + ".md", content: b.String()})
	}
	return pages
}

func frontmatterBlock(b *strings.Builder, title, kind, description string, order int) {
	fmt.Fprintf(b, "---\ntitle: %s\ntype: %s\ndescription: %s\norder: %d\n",
		yamlString(title), kind, yamlString(description), order)
}

func writeCommandSections(b *strings.Builder, cmd *cobra.Command, link func(*cobra.Command) string) {
	fmt.Fprintf(b, "\n## Usage\n\n```sh\n%s\n```\n", cmd.UseLine())
	if cmd.Example != "" {
		fmt.Fprintf(b, "\n## Examples\n\n```sh\n%s\n```\n", dedent(cmd.Example))
	}
	if flags := strings.TrimRight(cmd.NonInheritedFlags().FlagUsages(), "\n "); flags != "" {
		fmt.Fprintf(b, "\n## Options\n\n```text\n%s\n```\n", flags)
	}
	if flags := strings.TrimRight(cmd.InheritedFlags().FlagUsages(), "\n "); flags != "" {
		fmt.Fprintf(b, "\n## Options inherited from parent commands\n\n```text\n%s\n```\n", flags)
	}

	var children []*cobra.Command
	for _, child := range cmd.Commands() {
		if documented(child) && !isTopic(child) {
			children = append(children, child)
		}
	}
	if len(children) > 0 {
		b.WriteString("\n## Commands\n\n")
		for _, child := range children {
			fmt.Fprintf(b, "- %s — %s\n", link(child), child.Short)
		}
	}
	var topics []*cobra.Command
	for _, child := range cmd.Commands() {
		if documented(child) && isTopic(child) {
			topics = append(topics, child)
		}
	}
	if len(topics) > 0 {
		b.WriteString("\n## Help topics\n\n")
		for _, topic := range topics {
			fmt.Fprintf(b, "- %s — %s\n", link(topic), topic.Short)
		}
	}
	if cmd.HasParent() {
		fmt.Fprintf(b, "\n## See also\n\n- %s — %s\n", link(cmd.Parent()), cmd.Parent().Short)
	}
}

// dedent removes the indentation every non-empty line shares, which cobra's
// Example convention adds for the terminal.
func dedent(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n "), "\n")
	common := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if common < 0 || indent < common {
			common = indent
		}
	}
	if common <= 0 {
		return strings.Join(lines, "\n")
	}
	for i, line := range lines {
		if len(line) >= common {
			lines[i] = line[common:]
		} else {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// yamlString quotes a frontmatter value, so a colon or a backtick in a
// description cannot change what the YAML means.
func yamlString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// writePages writes into dir, which must not exist or be empty.
func writePages(dir string, pages []docPage) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case len(entries) > 0:
		return fmt.Errorf("%s is not empty; generate into a new directory, so no page "+
			"for a command that is gone can linger", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, page := range pages {
		if err := os.WriteFile(filepath.Join(dir, page.file), []byte(page.content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
