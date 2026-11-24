package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
	"strings"
)

// nextStep is the step of type kind that runs string with args, for why.
func nextStep(kind string, args []string, why string) nextCommand {
	return nextCommand{Type: kind, Command: commandLine(args), Argv: append([]string{"string"}, args...), Why: why}
}

// without is line without the option named, in any of its spellings, and
// its value.
func without(line []string, option string) []string {
	var kept []string
	for i := 0; i < len(line); i++ {
		name, _, inline := strings.Cut(line[i], "=")
		if _, ok := flagNamed(name, []string{option}); ok {
			if !inline {
				i++
			}
			continue
		}
		kept = append(kept, line[i])
	}
	return kept
}

// sitemapNext are the steps that carry a crawl on from data, the answer to
// op: while there may be more results or jobs, the next page, or for a page
// saved with -o FILE, all of them from that page on into FILE, which keeps
// them out of stdout as the page was; and for a job, by its status, the
// approve that starts it, the wait that follows it, or the results it found.
// Each repeats switches, the global switches given. The results are other
// JSON than a job, so they leave out --fields: see carried.
func sitemapNext(op *operation, data []byte, switches []string) []nextCommand {
	var answer struct {
		JobID  string            `json:"jobId"`
		Status string            `json:"status"`
		Cost   json.RawMessage   `json:"estimatedCostUsd"`
		Total  *int              `json:"total"`
		URLs   []json.RawMessage `json:"urls"`
		Jobs   []json.RawMessage `json:"jobs"`
	}
	if json.Unmarshal(data, &answer) != nil {
		return nil
	}
	if op.records != "" {
		read := len(answer.URLs) + len(answer.Jobs)
		more := read >= op.pageSize
		if answer.Total != nil {
			more = op.start+read < *answer.Total
		}
		if read == 0 || !more {
			return nil
		}
		if op.output != "" && op.output != "-" {
			why := "writes " + op.output + " again with every record from this page on, so it then holds NDJSON: one JSON object a line"
			return []nextCommand{nextStep(stepRead, append(without(op.line, "--limit"), "--all"), why)}
		}
		return []nextCommand{nextStep(stepRead, withOffset(op.line, op.start+read), "prints the next page")}
	}
	job := cmp.Or(answer.JobID, op.job)
	if job == "" {
		return nil
	}
	switch answer.Status {
	case "awaiting_approval":
		return []nextCommand{approveStep(job, string(answer.Cost), switches)}
	case "running":
		return []nextCommand{nextStep(stepWait, append([]string{"sitemap", "status", job, "--wait"}, switches...), "follows the crawl until it ends")}
	case "completed", "token_cap_exceeded", "canceled":
		return []nextCommand{nextStep(stepRead, append([]string{"sitemap", "results", job, "--limit", "100"}, carried(switches)...), "lists the first 100 pages it crawled")}
	}
	return nil
}

// approveStep is the step that starts the quoted crawl job, which costs up
// to cost, as the API sends it, so it needs the user's go.
func approveStep(job, cost string, switches []string) nextCommand {
	why := "starts the crawl at its quoted cost, which needs the user's go"
	if cost = strings.Trim(cost, `"`); cost != "" && cost != "null" {
		why = "starts the crawl at its quoted cost, up to $" + cost + ", which needs the user's go"
	}
	return nextStep(stepSpend, append([]string{"sitemap", "approve", job, "--wait"}, switches...), why)
}

// nextLine says on stderr the first of steps, and for one that spends, that
// it needs the user's go.
func nextLine(steps []nextCommand) string {
	if steps[0].Type == stepSpend {
		return "next, with the user's go: " + steps[0].Command + "\n"
	}
	return "next: " + steps[0].Command + "\n"
}

// withNext is data, a JSON object, with steps at its end as next_commands,
// or data as it came when it is no object, has its own next_commands, or
// steps is empty.
func withNext(data []byte, steps []nextCommand) []byte {
	object := bytes.TrimRight(data, " \t\r\n")
	fields, ok := members(object)
	if len(steps) == 0 || !ok || !json.Valid(object) || slices.ContainsFunc(fields, func(m member) bool { return m.key == "next_commands" }) {
		return data
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(steps) != nil {
		return data
	}
	start := []byte(`,"next_commands":`)
	if len(fields) == 0 {
		start = start[1:]
	}
	out := append(slices.Clone(bytes.TrimRight(object[:len(object)-1], " \t\r\n")), start...)
	out = append(out, bytes.TrimSuffix(encoded.Bytes(), []byte("\n"))...)
	return append(append(out, '}'), data[len(object):]...)
}
