// Command bench runs public question benchmarks against rolio with the pi
// agent.
//
// The procedure has four steps: ingest the conversation history, let the agent
// answer each question in a new session, let a model grade each answer against
// the gold answer, then calculate the statistics.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/austiecodes/rolio/internal/client"
	"github.com/austiecodes/rolio/internal/store"
)

// A question belongs to one scope. The documents of that scope are the only
// history that the agent can use for the question.
type question struct {
	ID       string
	Scope    string
	Group    string
	Question string
	Gold     string
	Date     string
}

type document struct {
	Path    string
	Content string
}

type dataset interface {
	// questions returns all questions in dataset order.
	questions(path string) ([]question, error)
	// documents calls put for each document of the given scopes.
	documents(path string, scopes map[string]bool, put func(document) error) error
	// judgeRules returns the grading rules for one answer.
	judgeRules(r record) string
}

var datasets = map[string]dataset{"locomo": locomo{}, "longmemeval": longmemeval{}}

func main() {
	steps := map[string]func(dataset, []string) error{"ingest": ingest, "qa": answer, "judge": judge, "stat": stat}
	if len(os.Args) < 3 || datasets[os.Args[1]] == nil || steps[os.Args[2]] == nil {
		fmt.Fprintln(os.Stderr, "usage: bench <locomo|longmemeval> <ingest|qa|judge|stat> [options]")
		os.Exit(2)
	}
	if err := steps[os.Args[2]](datasets[os.Args[1]], os.Args[3:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func frontmatter(title, date string) string {
	t, _ := json.Marshal(title)
	d, _ := json.Marshal(date)
	return fmt.Sprintf("---\ntitle: %s\ndate: %s\ntags: [conversation]\n---\n\n", t, d)
}

// selection is the set of options that select questions. The ingest step and
// the qa step must get the same options.
type selection struct {
	data, scopes, groups *string
	count                *int
}

func selectionFlags(flags *flag.FlagSet) selection {
	return selection{
		data:   flags.String("data", "", "path of the dataset file"),
		scopes: flags.String("scopes", "", "comma-separated scopes; default: all"),
		groups: flags.String("groups", "", "comma-separated question groups; default: all"),
		count:  flags.Int("count", 0, "maximum number of questions, at equal intervals; default: all"),
	}
}

func set(list string) map[string]bool {
	out := map[string]bool{}
	for item := range strings.SplitSeq(list, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out[item] = true
		}
	}
	return out
}

func (s selection) questions(d dataset) ([]question, error) {
	all, err := d.questions(*s.data)
	if err != nil {
		return nil, err
	}
	scopes, groups := set(*s.scopes), set(*s.groups)
	var selected []question
	for _, q := range all {
		if (len(scopes) == 0 || scopes[q.Scope]) && (len(groups) == 0 || groups[q.Group]) {
			selected = append(selected, q)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("no question agrees with --scopes and --groups")
	}
	if n := *s.count; n > 0 && n < len(selected) {
		sampled := make([]question, n)
		for i := range sampled {
			sampled[i] = selected[i*len(selected)/n]
		}
		selected = sampled
	}
	return selected, nil
}

// --- ingest ---

func ingest(d dataset, args []string) error {
	flags := flag.NewFlagSet("ingest", flag.ExitOnError)
	selected := selectionFlags(flags)
	dir := flags.String("dir", "", "write files into this directory and do not use rolio")
	flags.Parse(args)
	questions, err := selected.questions(d)
	if err != nil {
		return err
	}
	scopes := map[string]bool{}
	for _, q := range questions {
		scopes[q.Scope] = true
	}
	var rolio *client.Client
	if *dir == "" {
		if os.Getenv("ROLIO_SERVER") == "" {
			return errors.New("set ROLIO_SERVER or use --dir")
		}
		rolio = client.New(os.Getenv("ROLIO_SERVER"))
	}
	count := 0
	started := time.Now()
	err = d.documents(*selected.data, scopes, func(doc document) error {
		var err error
		if rolio != nil {
			_, err = rolio.Put(context.Background(), store.PutRequest{Path: doc.Path, Content: doc.Content})
		} else {
			target := filepath.Join(*dir, filepath.FromSlash(doc.Path))
			if err = os.MkdirAll(filepath.Dir(target), 0o755); err == nil {
				err = os.WriteFile(target, []byte(doc.Content), 0o644)
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", doc.Path, err)
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("ingested %d documents of %d scopes in %.1fs\n", count, len(scopes), time.Since(started).Seconds())
	return nil
}

// --- qa ---

func answer(d dataset, args []string) error {
	flags := flag.NewFlagSet("qa", flag.ExitOnError)
	selected := selectionFlags(flags)
	out := flags.String("out", "", "result directory")
	workspace := flags.String("workspace", "", "directory in which pi runs")
	mode := flags.String("mode", "rolio", "rolio or files: the location of the history")
	model := flags.String("model", "zai-coding-cn/glm-5.3", "pi model")
	thinking := flags.String("thinking", "", "pi thinking level; default: the pi default")
	parallel := flags.Int("parallel", 4, "number of parallel pi sessions")
	timeout := flags.Duration("timeout", 10*time.Minute, "time limit for one question")
	flags.Parse(args)
	questions, err := selected.questions(d)
	if err != nil {
		return err
	}
	path := filepath.Join(*out, "qa.jsonl")
	previous, err := readRecords(path)
	if err != nil {
		return err
	}
	answered := map[string]bool{}
	for _, r := range previous {
		answered[r.ID] = r.Error == ""
	}
	var todo []question
	for _, q := range questions {
		if !answered[q.ID] {
			todo = append(todo, q)
		}
	}
	fmt.Printf("%d questions, %d to run\n", len(questions), len(todo))
	return each(path, todo, *parallel, func(q question) record {
		history := "/conversations/" + q.Scope
		prompt := "The history of earlier conversations is in rolio under " + history + ". Use rolio to find the information that you need."
		if *mode == "files" {
			prompt = "The history of earlier conversations is in the directory ." + history + ". Read these files to find the information that you need."
		}
		prompt += "\n\n"
		if q.Date != "" {
			prompt += "Current date: " + q.Date + ". "
		}
		prompt += "Answer the question directly: " + q.Question
		result := runPi(*workspace, prompt, *timeout, piOptions(*model, *thinking)...)
		return record{
			ID: q.ID, Scope: q.Scope, Group: q.Group, Question: q.Question, Gold: q.Gold,
			Response: result.Text, Input: result.Input, Output: result.Output, CacheRead: result.CacheRead,
			ToolCalls: result.ToolCalls, RolioCalls: result.RolioCalls, Seconds: result.Seconds, Error: result.Error,
		}
	})
}

// --- judge ---

const judgePrompt = `You grade one answer of a memory benchmark as CORRECT or WRONG against the gold answer.

%s

Question: %s
Gold answer: %s
Generated answer: %s

Respond with JSON only: {"is_correct": "CORRECT" or "WRONG", "reasoning": "one sentence"}`

func judge(d dataset, args []string) error {
	flags := flag.NewFlagSet("judge", flag.ExitOnError)
	out := flags.String("out", "", "result directory")
	model := flags.String("model", "zai-coding-cn/glm-5.3", "pi model of the judge")
	thinking := flags.String("thinking", "low", "pi thinking level of the judge")
	parallel := flags.Int("parallel", 4, "number of parallel judge calls")
	flags.Parse(args)
	answers, err := readRecords(filepath.Join(*out, "qa.jsonl"))
	if err != nil {
		return err
	}
	path := filepath.Join(*out, "judged.jsonl")
	previous, err := readRecords(path)
	if err != nil {
		return err
	}
	// A new answer for the same question makes the old grade invalid.
	graded := map[string]string{}
	for _, r := range previous {
		if r.Error == "" {
			graded[r.ID] = r.Response
		}
	}
	var todo []record
	for _, r := range answers {
		if response, ok := graded[r.ID]; r.Error == "" && (!ok || response != r.Response) {
			todo = append(todo, r)
		}
	}
	fmt.Printf("%d answers, %d to grade\n", len(answers), len(todo))
	// The judge must not read context files or use tools.
	dir, err := os.MkdirTemp("", "bench-judge")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	options := append(piOptions(*model, *thinking), "--no-tools", "--no-context-files")
	return each(path, todo, *parallel, func(r record) record {
		wrong := false
		if r.Response == "" {
			r.Correct, r.Reason = &wrong, "empty answer"
			return r
		}
		prompt := fmt.Sprintf(judgePrompt, d.judgeRules(r), r.Question, r.Gold, r.Response)
		result := runPi(dir, prompt, 5*time.Minute, options...)
		if result.Error != "" {
			r.Error = "judge: " + result.Error
			return r
		}
		var verdict struct {
			IsCorrect string `json:"is_correct"`
			Reasoning string `json:"reasoning"`
		}
		start, end := strings.Index(result.Text, "{"), strings.LastIndex(result.Text, "}")
		if start < 0 || end < start || json.Unmarshal([]byte(result.Text[start:end+1]), &verdict) != nil {
			r.Error = "judge: no JSON verdict: " + result.Text
			return r
		}
		correct := strings.EqualFold(strings.TrimSpace(verdict.IsCorrect), "CORRECT")
		r.Correct, r.Reason = &correct, verdict.Reasoning
		return r
	})
}

// --- stat ---

func stat(_ dataset, args []string) error {
	flags := flag.NewFlagSet("stat", flag.ExitOnError)
	out := flags.String("out", "", "result directory")
	flags.Parse(args)
	records, err := readRecords(filepath.Join(*out, "judged.jsonl"))
	if err != nil {
		return err
	}
	answers, err := readRecords(filepath.Join(*out, "qa.jsonl"))
	if err != nil {
		return err
	}
	type tally struct{ correct, total int }
	var all tally
	groups := map[string]*tally{}
	var input, output, cacheRead, toolCalls, rolioCalls, failed int
	var seconds float64
	for _, r := range answers {
		if r.Error != "" {
			failed++
		}
	}
	for _, r := range records {
		if r.Correct == nil {
			failed++
			continue
		}
		group := groups[r.Group]
		if group == nil {
			group = &tally{}
			groups[r.Group] = group
		}
		all.total++
		group.total++
		if *r.Correct {
			all.correct++
			group.correct++
		}
		input, output, cacheRead = input+r.Input, output+r.Output, cacheRead+r.CacheRead
		toolCalls, rolioCalls = toolCalls+r.ToolCalls, rolioCalls+r.RolioCalls
		seconds += r.Seconds
	}
	if all.total == 0 {
		return errors.New("no graded answers")
	}
	percent := func(t tally) string {
		return fmt.Sprintf("%6.2f%% (%d/%d)", 100*float64(t.correct)/float64(t.total), t.correct, t.total)
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	n := float64(all.total)
	var report strings.Builder
	fmt.Fprintf(&report, "Accuracy: %s\n", percent(all))
	for _, name := range names {
		fmt.Fprintf(&report, "  %-28s %s\n", name, percent(*groups[name]))
	}
	fmt.Fprintf(&report, "\nAverage for each question:\n")
	fmt.Fprintf(&report, "  input tokens   %.0f (and %.0f read from cache)\n", float64(input)/n, float64(cacheRead)/n)
	fmt.Fprintf(&report, "  output tokens  %.0f\n", float64(output)/n)
	fmt.Fprintf(&report, "  seconds        %.1f\n", seconds/n)
	fmt.Fprintf(&report, "  tool calls     %.1f (%.1f to rolio)\n", float64(toolCalls)/n, float64(rolioCalls)/n)
	fmt.Fprintf(&report, "\nNot graded because of errors: %d\n", failed)
	fmt.Print(report.String())
	return os.WriteFile(filepath.Join(*out, "summary.txt"), []byte(report.String()), 0o644)
}
