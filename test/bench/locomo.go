package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// locomo is the LoCoMo dataset: 10 long conversations between two persons,
// each with questions. The scope of a question is its conversation.
type locomo struct{}

type locomoSample struct {
	ID           string                     `json:"sample_id"`
	Conversation map[string]json.RawMessage `json:"conversation"`
	QA           []struct {
		Question string          `json:"question"`
		Answer   json.RawMessage `json:"answer"`
		Category int             `json:"category"`
	} `json:"qa"`
}

type locomoSession struct {
	Number int
	Date   string
	Turns  []struct {
		Speaker string `json:"speaker"`
		ID      string `json:"dia_id"`
		Text    string `json:"text"`
		Caption string `json:"blip_caption"`
	}
}

var (
	locomoSessionKey = regexp.MustCompile(`^session_(\d+)$`)
	locomoCategories = map[int]string{1: "1-multi-hop", 2: "2-temporal", 3: "3-open-domain", 4: "4-single-hop"}
)

func (locomo) load(path string) ([]locomoSample, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var samples []locomoSample
	if err := json.Unmarshal(data, &samples); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return samples, nil
}

// jsonText returns a JSON string without quotes, and other JSON values as text.
func jsonText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func (s locomoSample) sessions() ([]locomoSession, error) {
	var out []locomoSession
	for key, raw := range s.Conversation {
		m := locomoSessionKey.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		session := locomoSession{Number: n, Date: jsonText(s.Conversation[key+"_date_time"])}
		if err := json.Unmarshal(raw, &session.Turns); err != nil {
			return nil, fmt.Errorf("%s %s: %w", s.ID, key, err)
		}
		if len(session.Turns) > 0 {
			out = append(out, session)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out, nil
}

func (d locomo) questions(path string) ([]question, error) {
	samples, err := d.load(path)
	if err != nil {
		return nil, err
	}
	var out []question
	for _, s := range samples {
		sessions, err := s.sessions()
		if err != nil {
			return nil, err
		}
		date := ""
		if len(sessions) > 0 {
			date = sessions[len(sessions)-1].Date
		}
		for i, qa := range s.QA {
			// Category 5 contains adversarial questions that have no gold answer.
			if qa.Category == 5 {
				continue
			}
			out = append(out, question{
				ID: fmt.Sprintf("%s:%d", s.ID, i), Scope: s.ID, Group: locomoCategories[qa.Category],
				Question: qa.Question, Gold: jsonText(qa.Answer), Date: date,
			})
		}
	}
	return out, nil
}

func (d locomo) documents(path string, scopes map[string]bool, put func(document) error) error {
	samples, err := d.load(path)
	if err != nil {
		return err
	}
	for _, s := range samples {
		if !scopes[s.ID] {
			continue
		}
		sessions, err := s.sessions()
		if err != nil {
			return err
		}
		a, b := jsonText(s.Conversation["speaker_a"]), jsonText(s.Conversation["speaker_b"])
		for _, session := range sessions {
			var doc strings.Builder
			doc.WriteString(frontmatter(fmt.Sprintf("%s and %s, session %d", a, b, session.Number), session.Date))
			fmt.Fprintf(&doc, "# Conversation between %s and %s\n\nDate: %s\n\n", a, b, session.Date)
			for _, t := range session.Turns {
				fmt.Fprintf(&doc, "[%s] (%s): %s", t.Speaker, t.ID, t.Text)
				if t.Caption != "" {
					fmt.Fprintf(&doc, " (shared image: %s)", t.Caption)
				}
				doc.WriteString("\n\n")
			}
			name := fmt.Sprintf("/conversations/%s/session-%02d.md", s.ID, session.Number)
			if err := put(document{Path: name, Content: doc.String()}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (locomo) judgeRules(record) string {
	return `Two persons had many conversations. The question asks about a fact from these conversations.

- The gold answer is short. The generated answer can be longer. It is CORRECT if it contains the same fact as the gold answer.
- For dates and times, it is CORRECT if it refers to the same date or period, in any format, also as a relative time that agrees with the gold answer.
- It is WRONG if it gives a different fact, if it says that the information is not available, or if it is empty.`
}
