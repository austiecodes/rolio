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
	// The output of rolio search: one block for each document, then the
	// directories, or one line when there is no document. A block is the
	// line of the document and the indented lines below it.
	searchLine  = regexp.MustCompile(`(?m)^(/\S+)    \[rank: `)
	searchBlock = regexp.MustCompile(`(?m)^/\S+    \[rank: .*\n(  .*\n)*`)
	// A passage is a line of the document that the search found.
	searchPassage = regexp.MustCompile(`(?m)^  > .*\n`)
	// The abstract of a document, the number of its other lines that agree
	// with the query, and the lines of the directories are not text of a
	// document. A filter such as grep can show them without the line of
	// their document, thus they are removed in all positions.
	searchSummary = regexp.MustCompile(`(?m)^(  abstract: .*|  \(\d+\+? more lines match; rolio cat .* shows the document\)|directories:|  /\S*/(    .*)?)\n`)
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
	// Seen is the number of evidence turns in the results. In the output of
	// rolio search, only the passages count: an abstract is not text of the
	// document, and a search of an earlier version showed the start of the
	// document and not the text that it found.
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
		content = append(content, searchSummary.ReplaceAllString(searchBlock.ReplaceAllStringFunc(result, func(block string) string {
			return strings.Join(searchPassage.FindAllString(block, -1), "")
		}), ""))
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
