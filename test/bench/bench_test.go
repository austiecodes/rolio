package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// result is the tool_execution_end event of one call.
func result(text string) string {
	end, _ := json.Marshal(map[string]any{
		"type": "tool_execution_end", "toolName": "bash",
		"result": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}},
	})
	return string(end) + "\n"
}

// The results have the forms of real traces: the agent puts many commands in
// one call.
func TestMeasure(t *testing.T) {
	const (
		tree  = "/\n└── conversations/\n    └── c/\n        ├── session-01.md\n        ├── session-02.md\n        └── session-03.md\n"
		hit   = "/conversations/c/session-01.md    [rank: 0.10, 2.0KB]\n  ---\n  title: \"A and B, session 1\"\n\n"
		other = "/conversations/c/session-03.md    [rank: 0.05, 2.0KB]\n  ---\n\n"
	)
	trace := result(tree) + // rolio tree /
		// rolio cat /conversations/c | head || rolio search x: the search did not run.
		result("(no output)") +
		// rolio search "support group"; rolio tree /: the tree is not a search result.
		result("no results found\n"+tree) +
		// rolio search support; echo ---; grep zzz file: the call fails, the search does not.
		result(hit+other+"---\n\nCommand exited with code 1") +
		// The snippet of a search result is the start of the document. It
		// has the first turn, but the search did not find that turn.
		result("/conversations/c/session-02.md    [rank: 0.10, 2.0KB]\n  ---\n  title: \"A and B, session 2\"\n  ---\n  # Conversation\n  [Melanie] (D2:1): Hi.\n\n") +
		// A loop that reads all documents names none of them.
		// Text of a document is not the output of a search.
		result("[Caroline] (D1:3): I went to the group, but no results found.\n\n[Melanie] (D2:14): Nice.\n\n") +
		result("no results found\n")
	e := evidence{
		Turns: [][]string{{"(D1:3)"}, {"(D2:1)"}, {"(D2:14)"}},
		Docs:  []string{"/conversations/c/session-01.md", "/conversations/c/session-02.md"},
	}
	got := measure(traceResults([]byte(trace)), e)
	want := retrieval{Seen: 2, Listed: 2, Searches: 4, Empty: 2, Repeated: 1}
	if got != want {
		t.Errorf("measure = %+v, want %+v", got, want)
	}
}

func TestRetrievalTotals(t *testing.T) {
	two := evidence{Turns: [][]string{{"a"}, {"b"}}, Docs: []string{"x"}}
	var totals retrievalTotals
	totals.add(retrieval{Seen: 2, Listed: 1, Searches: 3, Empty: 1, Repeated: 1}, two, true)
	totals.add(retrieval{Seen: 1, Searches: 1}, two, false)
	// A question without evidence adds only to the search statistics.
	totals.add(retrieval{Searches: 2, Empty: 2, Repeated: 1}, evidence{}, true)
	var report strings.Builder
	totals.report(&report)
	for _, line := range []string{
		"Evidence, for the 2 questions that have evidence and a trace:",
		"turns seen             75.00% (3/4) of the evidence turns",
		"all turns seen         50.00% (1/2) of the questions",
		"accuracy              100.00% (1/1) with all evidence turns seen",
		"  0.00% (0/1) with some evidence turns not seen",
		"listed by a search     50.00% (1/2) of the evidence documents",
		"Results of rolio search, for the 3 questions that have a trace:",
		"results               2.0 for each question",
		"without a document     50.00% (3/6) of the results",
		"after such a result    33.33% (2/6) of the results",
		"questions with one     66.67% (2/3) of the questions",
	} {
		if !strings.Contains(report.String(), line) {
			t.Errorf("the report does not contain %q:\n%s", line, report.String())
		}
	}
}

func TestCompactTrace(t *testing.T) {
	out := `{"type":"message_start","message":{"role":"assistant"}}
{"type":"message_update","message":{"role":"assistant"}}
{"type":"message_end","message":{"role":"assistant"}}
{"type":"tool_execution_start","toolCallId":"1"}
{"type":"tool_execution_update","toolCallId":"1"}
{"type":"tool_execution_end","toolCallId":"1"}
{"type":"message_end","message":{"role":"toolResult"}}
not JSON
{"type":"turn_end"}
{"type":"agent_end"}
`
	want := `{"type":"message_end","message":{"role":"assistant"}}
{"type":"tool_execution_start","toolCallId":"1"}
{"type":"tool_execution_end","toolCallId":"1"}
`
	if got := string(compactTrace([]byte(out))); got != want {
		t.Errorf("compactTrace = %q, want %q", got, want)
	}
}

func TestPiResultRead(t *testing.T) {
	stream := `{"type":"message_update","message":{"role":"assistant"}}
{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse","usage":{"input":10,"output":2,"cacheRead":5},"content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"rolio search adoption"}}]}}
{"type":"message_end","message":{"role":"toolResult","toolCallId":"c1","content":[{"type":"text","text":"no results found"}]}}
{"type":"message_end","message":{"role":"assistant","stopReason":"toolUse","usage":{"input":20,"output":3,"cacheRead":0},"content":[{"type":"toolCall","id":"c2","name":"read","arguments":{"path":"missing.md"}}]}}
{"type":"message_end","message":{"role":"assistant","stopReason":"stop","usage":{"input":30,"output":4,"cacheRead":0},"content":[{"type":"text","text":" done "}]}}
`
	var result piResult
	stop, modelError := result.read([]byte(stream))
	if stop != "stop" || modelError != "" {
		t.Errorf("stop = %q, error = %q", stop, modelError)
	}
	if result.Text != "done" || result.Input != 60 || result.Output != 9 || result.CacheRead != 5 || result.ToolCalls != 2 || result.RolioCalls != 1 {
		t.Errorf("result = %+v", result)
	}
}

// The stat step reads the evidence from the dataset and the tool results
// from the traces.
func TestStatWithTraces(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "locomo.json")
	dataset := `[{"sample_id":"c","conversation":{"speaker_a":"A","speaker_b":"B","session_1_date_time":"1 May 2023",
 "session_1":[{"speaker":"A","dia_id":"D1:1","text":"Hi."},{"speaker":"B","dia_id":"D1:2","text":"I moved in May."}]},
 "qa":[{"question":"When?","answer":"May","evidence":["D1:2"],"category":2},{"question":"Who?","answer":"B","evidence":["D1:1"],"category":4}]}]`
	records := `{"id":"c:0","scope":"c","group":"2-temporal","response":"May","correct":true}
{"id":"c:1","scope":"c","group":"4-single-hop","response":"A","correct":false}
`
	files := map[string]string{
		data: dataset, filepath.Join(dir, "qa.jsonl"): records, filepath.Join(dir, "judged.jsonl"): records,
		tracePath(dir, "c:0"): result("/conversations/c/session-01.md    [rank: 0.10, 1.0KB]\n  ---\n\n") + result("[B] (D1:2): I moved in May.\n"),
		tracePath(dir, "c:1"): result("no results found\n"),
	}
	if err := os.MkdirAll(filepath.Join(dir, "traces"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := stat(locomo{}, []string{"--out", dir, "--data", data}); err != nil {
		t.Fatal(err)
	}
	summary, _ := os.ReadFile(filepath.Join(dir, "summary.txt"))
	for _, line := range []string{
		"turns seen             50.00% (1/2) of the evidence turns",
		"accuracy              100.00% (1/1) with all evidence turns seen",
		"  0.00% (0/1) with some evidence turns not seen",
		"listed by a search     50.00% (1/2) of the evidence documents",
		"without a document     50.00% (1/2) of the results",
	} {
		if !strings.Contains(string(summary), line) {
			t.Errorf("the summary does not contain %q:\n%s", line, summary)
		}
	}
	// A dataset that does not contain the questions of the run is an error.
	other := filepath.Join(dir, "other.json")
	if err := os.WriteFile(other, []byte(strings.ReplaceAll(dataset, `"sample_id":"c"`, `"sample_id":"d"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stat(locomo{}, []string{"--out", dir, "--data", other}); err == nil {
		t.Error("stat with the wrong dataset gives no error")
	}
}

// A result directory of an earlier version has records without evidence and
// no traces. The stat step must give the report without the new statistics.
func TestStatWithoutTraces(t *testing.T) {
	out := t.TempDir()
	line := `{"id":"conv-26:0","scope":"conv-26","group":"2-temporal","response":"7 May 2023","tool_calls":3,"correct":true}` + "\n"
	for _, name := range []string{"qa.jsonl", "judged.jsonl"} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := stat(nil, []string{"--out", out}); err != nil {
		t.Fatal(err)
	}
	summary, err := os.ReadFile(filepath.Join(out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), "Accuracy: 100.00% (1/1)") || strings.Contains(string(summary), "Evidence") || strings.Contains(string(summary), "rolio search") {
		t.Errorf("summary:\n%s", summary)
	}
}

func TestLocomoEvidence(t *testing.T) {
	turns := func(ids ...string) []locomoTurn {
		var out []locomoTurn
		for _, id := range ids {
			out = append(out, locomoTurn{ID: id})
		}
		return out
	}
	sessions := []locomoSession{
		{Number: 1, Turns: turns("D1:3", "D1:5")}, {Number: 8, Turns: turns("D8:6")},
		{Number: 9, Turns: turns("D9:1", "D9:17")}, {Number: 11, Turns: turns("D11:26")},
	}
	// The entries have the forms of the dataset. D4:4 and D8:99 are not in
	// this conversation.
	got := locomoEvidence("conv-26", sessions, []string{"D1:3", "D8:6; D9:17", "D", "D:11:26", "D9:1 D4:4", "D8:99", "D1:3"})
	want := evidence{
		Turns: [][]string{{"(D1:3)"}, {"(D8:6)"}, {"(D9:17)"}, {"(D11:26)"}, {"(D9:1)"}},
		Docs: []string{
			"/conversations/conv-26/session-01.md", "/conversations/conv-26/session-08.md",
			"/conversations/conv-26/session-09.md", "/conversations/conv-26/session-11.md",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("locomoEvidence = %+v, want %+v", got, want)
	}
}

// The evidence of a question must be the names that the ingest step gives to
// the documents of the answer sessions.
func TestLongmemevalEvidence(t *testing.T) {
	long := "The GPS system of the car did not function correctly after the first service in March."
	first := "I think that I had the first problem with the car in the month of March."
	data := `[
{"question_id":"q1","question_type":"multi-session","question":"Q?","answer":"A","question_date":"2023/05/01",
 "haystack_dates":["2023/04/03","2023/04/01","2023/04/02"],
 "haystack_session_ids":["answer_a","noise_b","answer_c"],
 "answer_session_ids":["answer_a","answer_c"],
 "haystack_sessions":[
  [{"role":"user","content":"third"},{"role":"assistant","content":"Yes.\n` + long + `","has_answer":true}],
  [{"role":"user","content":"first"}],
  [{"role":"user","content":"` + first + `","has_answer":true},{"role":"assistant","content":"OK.","has_answer":true}]]},
{"question_id":"q2_abs","question_type":"multi-session","question":"Q?","answer":"not available","question_date":"2023/05/01",
 "haystack_dates":["2023/04/01"],"haystack_session_ids":["answer_d"],"answer_session_ids":["answer_d"],
 "haystack_sessions":[[{"role":"user","content":"other","has_answer":true}]]}
]`
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	questions, err := longmemeval{}.questions(path)
	if err != nil {
		t.Fatal(err)
	}
	want := evidence{
		// A short turn and a short first line have no text.
		Turns: [][]string{{"[user]: " + first, first}, {long[:80]}},
		Docs:  []string{"/conversations/q1/session-002.md", "/conversations/q1/session-003.md"},
	}
	if !reflect.DeepEqual(questions[0].Evidence, want) {
		t.Errorf("evidence = %+v, want %+v", questions[0].Evidence, want)
	}
	if len(questions[1].Evidence.Turns) != 0 {
		t.Errorf("an abstention question has evidence: %+v", questions[1].Evidence)
	}
	content := map[string]string{}
	err = longmemeval{}.documents(path, map[string]bool{"q1": true}, func(doc document) error {
		content[doc.Path] = doc.Content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range want.Turns[0] {
		want.Turns[0][i] = cut(want.Turns[0][i], 80)
	}
	// Each evidence document must contain the texts of its turns.
	for i, doc := range want.Docs {
		for _, text := range want.Turns[i] {
			if !strings.Contains(content[doc], text) {
				t.Errorf("%s does not contain %q:\n%s", doc, text, content[doc])
			}
		}
	}
}

func TestCut(t *testing.T) {
	if got := cut("aé", 2); got != "a" {
		t.Errorf("cut = %q, want %q", got, "a")
	}
}
