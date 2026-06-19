package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// record is one line of qa.jsonl or judged.jsonl.
type record struct {
	ID         string  `json:"id"`
	Scope      string  `json:"scope"`
	Group      string  `json:"group"`
	Question   string  `json:"question"`
	Gold       string  `json:"gold"`
	Response   string  `json:"response"`
	Input      int     `json:"input_tokens"`
	Output     int     `json:"output_tokens"`
	CacheRead  int     `json:"cache_read_tokens"`
	ToolCalls  int     `json:"tool_calls"`
	RolioCalls int     `json:"rolio_calls"`
	Seconds    float64 `json:"seconds"`
	Error      string  `json:"error,omitempty"`
	Correct    *bool   `json:"correct,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

// readRecords returns the last record of each ID in file order.
func readRecords(path string) ([]record, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	index := map[string]int{}
	var records []record
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r record
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if i, ok := index[r.ID]; ok {
			records[i] = r
		} else {
			index[r.ID] = len(records)
			records = append(records, r)
		}
	}
	return records, nil
}

// each runs work for each item with a limit on parallel runs and appends each
// returned record to path.
func each[T any](path string, items []T, parallel int, work func(T) record) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, max(parallel, 1))
	done := 0
	for _, item := range items {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			r := work(item)
			line, _ := json.Marshal(r)
			mu.Lock()
			defer mu.Unlock()
			file.Write(append(line, '\n'))
			done++
			status := "ok"
			if r.Error != "" {
				status = "error: " + r.Error
			} else if r.Correct != nil {
				status = map[bool]string{true: "correct", false: "wrong"}[*r.Correct]
			}
			fmt.Printf("[%d/%d] %s %s\n", done, len(items), r.ID, status)
		}()
	}
	wg.Wait()
	return nil
}
