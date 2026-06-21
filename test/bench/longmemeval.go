package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// longmemeval is the LongMemEval dataset: 500 questions, each with its own
// history of chat sessions between a user and an assistant. The scope of a
// question is thus the question itself.
type longmemeval struct{}

type longmemevalItem struct {
	ID       string          `json:"question_id"`
	Type     string          `json:"question_type"`
	Question string          `json:"question"`
	Answer   json.RawMessage `json:"answer"`
	Date     string          `json:"question_date"`
	Dates    []string        `json:"haystack_dates"`
	Sessions [][]struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		// HasAnswer is set on the turns that contain the answer.
		HasAnswer bool `json:"has_answer"`
	} `json:"haystack_sessions"`
}

// The dataset file can be some hundred megabytes. Read one element at a time.
func longmemevalEach[T any](path string, visit func(T) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReaderSize(file, 1<<20))
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for decoder.More() {
		var item T
		if err := decoder.Decode(&item); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if err := visit(item); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	if err == io.EOF {
		return nil
	}
	return err
}

// longmemevalOrder returns the indexes of the sessions in order of time.
// Session IDs in the dataset show which sessions contain the answer, thus the
// documents get their names from this order and not from these IDs.
func longmemevalOrder(dates []string) []int {
	order := make([]int, len(dates))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return dates[order[a]] < dates[order[b]] })
	return order
}

func longmemevalDocument(scope string, position int) string {
	return fmt.Sprintf("/conversations/%s/session-%03d.md", scope, position+1)
}

func longmemevalAbstention(id string) bool { return strings.HasSuffix(id, "_abs") }

// longmemevalText removes NUL, which PostgreSQL text cannot contain.
func longmemevalText(content string) string { return strings.ReplaceAll(content, "\x00", "") }

// cut returns the first bytes of text, to a maximum of n, and does not divide
// a character.
func cut(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

// longmemevalTurnTexts returns texts that show a turn in a tool result: the
// start of the turn as the document has it, and the start of each long line.
// A short text can be in other turns too, thus a short turn has no texts.
func longmemevalTurnTexts(role, content string) []string {
	const short, long = 40, 80
	lines := strings.Split(longmemevalText(content), "\n")
	var texts []string
	if len(strings.TrimSpace(lines[0])) >= short {
		texts = append(texts, cut(fmt.Sprintf("[%s]: %s", role, lines[0]), long))
	}
	for _, line := range lines {
		if line = strings.TrimSpace(line); len(line) >= short {
			texts = append(texts, cut(line, long))
		}
	}
	return texts
}

func (item longmemevalItem) evidence() evidence {
	var e evidence
	// The history of an abstention question does not contain the answer.
	// The documents step gives the error for dates that do not agree.
	if longmemevalAbstention(item.ID) || len(item.Dates) != len(item.Sessions) {
		return e
	}
	for position, i := range longmemevalOrder(item.Dates) {
		found := false
		for _, turn := range item.Sessions[i] {
			if texts := longmemevalTurnTexts(turn.Role, turn.Content); turn.HasAnswer && len(texts) > 0 {
				e.Turns = append(e.Turns, texts)
				found = true
			}
		}
		if found {
			e.Docs = append(e.Docs, longmemevalDocument(item.ID, position))
		}
	}
	return e
}

func (longmemeval) questions(path string) ([]question, error) {
	var out []question
	err := longmemevalEach(path, func(item longmemevalItem) error {
		out = append(out, question{
			ID: item.ID, Scope: item.ID, Group: item.Type,
			Question: item.Question, Gold: jsonText(item.Answer), Date: item.Date,
			Evidence: item.evidence(),
		})
		return nil
	})
	return out, err
}

func (longmemeval) documents(path string, scopes map[string]bool, put func(document) error) error {
	return longmemevalEach(path, func(history longmemevalItem) error {
		if !scopes[history.ID] {
			return nil
		}
		if len(history.Dates) != len(history.Sessions) {
			return fmt.Errorf("%s: %d dates for %d sessions", history.ID, len(history.Dates), len(history.Sessions))
		}
		for position, i := range longmemevalOrder(history.Dates) {
			date := history.Dates[i]
			var doc strings.Builder
			doc.WriteString(frontmatter("Chat session on "+date, date))
			fmt.Fprintf(&doc, "# Chat session on %s\n\n", date)
			for _, turn := range history.Sessions[i] {
				fmt.Fprintf(&doc, "[%s]: %s\n\n", turn.Role, longmemevalText(turn.Content))
			}
			if err := put(document{Path: longmemevalDocument(history.ID, position), Content: doc.String()}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (longmemeval) judgeRules(r record) string {
	rules := "A user had many chat sessions with an assistant. The question asks about information from these sessions.\n\n"
	switch {
	case longmemevalAbstention(r.ID):
		return rules + `The history does not contain the information that the question asks for. The gold answer explains this.

- It is CORRECT if the generated answer says that the information is not available or that the question cannot be answered.
- It is WRONG if the generated answer gives a specific answer.`
	case r.Group == "single-session-preference":
		return rules + `The gold answer is a description of a good personalized response.

- It is CORRECT if the generated answer uses the personal information of the user correctly and agrees with the description. It is not necessary that it contains all points of the description.
- It is WRONG if it ignores or contradicts the personal information of the user, or if it is empty.`
	}
	rules += `- The generated answer can be longer than the gold answer. It is CORRECT if it contains the gold answer or an equivalent of it.
- It is WRONG if it contains only a part of the necessary information, if it gives a different fact, if it says that the information is not available, or if it is empty.`
	switch r.Group {
	case "temporal-reasoning":
		rules += "\n- Do not count a difference of one in a number of days, weeks, or months as an error."
	case "knowledge-update":
		rules += "\n- The information changed in time. It is CORRECT if the generated answer gives the updated value of the gold answer, also when it mentions the earlier value."
	}
	return rules
}
