package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// A trace is the event stream of pi for one question, without the events
// that the statistics do not use. The stat step calculates the retrieval
// statistics from the traces, thus a change of the rules below does not make
// a new run necessary.
//
// The agent frequently puts many commands in one call, for example
// "rolio search x; rolio tree /". Thus the rules use the text of the results
// and not the text of the commands.

// evidence tells where the answer of a question is in its history.
type evidence struct {
	// Turns are the turns that contain the answer. Each turn is a list of
	// texts from the document. The agent saw the turn when a tool result
	// contains one of them.
	Turns [][]string
	// Docs are the documents of these turns.
	Docs []string
}

var (
	// The events that have the tool calls and the answers.
	traceEvents = []string{"tool_execution_start", "tool_execution_end"}
	// The output of rolio search: one line for each document, each with a
	// snippet below it, or one line when there is no document.
	searchLine    = regexp.MustCompile(`(?m)^(/\S+)    \[rank: `)
	searchSnippet = regexp.MustCompile(`(?m)^/\S+    \[rank: .*\n(  .*\n)*`)
	noResults     = regexp.MustCompile(`(?m)^no results found$`)
)

func compactTrace(out []byte) []byte {
	var trace []byte
	for line := range bytes.SplitSeq(out, []byte("\n")) {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if slices.Contains(traceEvents, event.Type) || (event.Type == "message_end" && event.Message.Role == "assistant") {
			trace = append(append(trace, line...), '\n')
		}
	}
	return trace
}

// traceResults returns the text of each tool result of a trace, in order.
func traceResults(trace []byte) []string {
	var results []string
	for line := range bytes.SplitSeq(trace, []byte("\n")) {
		var event struct {
			Type   string `json:"type"`
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "tool_execution_end" {
			continue
		}
		var text strings.Builder
		for _, block := range event.Result.Content {
			text.WriteString(block.Text)
		}
		results = append(results, text.String())
	}
	return results
}

// retrieval is what the tool results of one question say about its evidence.
// A result counts also when its call failed: the exit status of a call is
// that of its last command, and the agent saw the text in each case.
type retrieval struct {
	// Seen is the number of evidence turns in the results. The snippet of a
	// search result is the start of a document and not the text that the
	// search found, thus it does not count.
	Seen int
	// Listed is the number of evidence documents in the lines of search
	// results.
	Listed int
	// Searches are the results that contain the output of rolio search.
	// Empty are those in which no search listed a document. Repeated are
	// those for which the search result before them was empty.
	Searches, Empty, Repeated int
}

func measure(results []string, e evidence) retrieval {
	var r retrieval
	var listed, content []string
	afterEmpty := false
	for _, result := range results {
		content = append(content, searchSnippet.ReplaceAllString(result, ""))
		lines := searchLine.FindAllStringSubmatch(result, -1)
		if len(lines) == 0 && !noResults.MatchString(result) {
			continue
		}
		r.Searches++
		if afterEmpty {
			r.Repeated++
		}
		if afterEmpty = len(lines) == 0; afterEmpty {
			r.Empty++
		}
		for _, line := range lines {
			listed = append(listed, line[1])
		}
	}
	for _, doc := range e.Docs {
		if slices.Contains(listed, doc) {
			r.Listed++
		}
	}
	for _, texts := range e.Turns {
		if slices.ContainsFunc(content, func(result string) bool {
			return slices.ContainsFunc(texts, func(text string) bool { return strings.Contains(result, text) })
		}) {
			r.Seen++
		}
	}
	return r
}
