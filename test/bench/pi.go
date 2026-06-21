package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type piResult struct {
	Text       string
	Error      string
	Input      int
	Output     int
	CacheRead  int
	ToolCalls  int
	RolioCalls int
	Seconds    float64
	// Events is the JSON event stream of pi.
	Events []byte
}

var rolioCall = regexp.MustCompile(`(^|[^[:alnum:]_/-])rolio `)

// runPi runs one non-interactive pi session and reads its JSON event stream.
func runPi(dir, prompt string, timeout time.Duration, options ...string) piResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := append([]string{"--mode", "json", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates"}, options...)
	cmd := exec.CommandContext(ctx, "pi", append(args, "--", prompt)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	started := time.Now()
	out, err := cmd.Output()
	result := piResult{Seconds: time.Since(started).Seconds(), Events: out}
	stop, modelError := result.read(out)
	switch {
	case ctx.Err() != nil:
		result.Error = "timeout"
	case err != nil:
		result.Error = strings.TrimSpace(fmt.Sprintf("%v %s", err, stderr.String()))
	case stop != "stop":
		// pi exits 0 when the model provider fails.
		result.Error = strings.TrimSpace("model did not finish: " + stop + " " + modelError)
	}
	return result
}

// read reads the JSON event stream of pi. It returns the stop reason and the
// error message of the last assistant message.
func (r *piResult) read(out []byte) (stop, modelError string) {
	for line := range bytes.SplitSeq(out, []byte("\n")) {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Role         string `json:"role"`
				StopReason   string `json:"stopReason"`
				ErrorMessage string `json:"errorMessage"`
				Usage        struct {
					Input, Output, CacheRead int
				} `json:"usage"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &event) != nil || event.Type != "message_end" || event.Message.Role != "assistant" {
			continue
		}
		var blocks []struct {
			Type      string `json:"type"`
			Text      string `json:"text"`
			Name      string `json:"name"`
			Arguments struct {
				Command string `json:"command"`
			} `json:"arguments"`
		}
		json.Unmarshal(event.Message.Content, &blocks)
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "toolCall":
				r.ToolCalls++
				if b.Name == "bash" && rolioCall.MatchString(b.Arguments.Command) {
					r.RolioCalls++
				}
			}
		}
		r.Text = strings.TrimSpace(strings.Join(parts, "\n"))
		stop, modelError = event.Message.StopReason, event.Message.ErrorMessage
		r.Input += event.Message.Usage.Input
		r.Output += event.Message.Usage.Output
		r.CacheRead += event.Message.Usage.CacheRead
	}
	return stop, modelError
}

func piOptions(model, thinking string) []string {
	options := []string{"--model", model}
	if thinking != "" {
		options = append(options, "--thinking", thinking)
	}
	return options
}
