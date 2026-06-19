package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
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
}

type longmemevalHistory struct {
	ID       string   `json:"question_id"`
	Dates    []string `json:"haystack_dates"`
	Sessions [][]struct {
		Role    string `json:"role"`
		Content string `json:"content"`
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

func (longmemeval) questions(path string) ([]question, error) {
	var out []question
	err := longmemevalEach(path, func(item longmemevalItem) error {
		out = append(out, question{
			ID: item.ID, Scope: item.ID, Group: item.Type,
			Question: item.Question, Gold: jsonText(item.Answer), Date: item.Date,
		})
		return nil
	})
	return out, err
}

func (longmemeval) documents(path string, scopes map[string]bool, put func(document) error) error {
	return longmemevalEach(path, func(history longmemevalHistory) error {
		if !scopes[history.ID] {
			return nil
		}
		if len(history.Dates) != len(history.Sessions) {
			return fmt.Errorf("%s: %d dates for %d sessions", history.ID, len(history.Dates), len(history.Sessions))
		}
		// Session IDs in the dataset show which sessions contain the answer.
		// Name the documents by their position in time and not by these IDs.
		order := make([]int, len(history.Sessions))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return history.Dates[order[a]] < history.Dates[order[b]] })
		for position, i := range order {
			date := history.Dates[i]
			var doc strings.Builder
			doc.WriteString(frontmatter("Chat session on "+date, date))
			fmt.Fprintf(&doc, "# Chat session on %s\n\n", date)
			for _, turn := range history.Sessions[i] {
				// PostgreSQL text cannot contain NUL.
				fmt.Fprintf(&doc, "[%s]: %s\n\n", turn.Role, strings.ReplaceAll(turn.Content, "\x00", ""))
			}
			name := fmt.Sprintf("/conversations/%s/session-%03d.md", history.ID, position+1)
			if err := put(document{Path: name, Content: doc.String()}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (longmemeval) judgeRules(r record) string {
	rules := "A user had many chat sessions with an assistant. The question asks about information from these sessions.\n\n"
	switch {
	case strings.HasSuffix(r.ID, "_abs"):
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
