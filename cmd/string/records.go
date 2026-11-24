package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

// pageSizes are the most records the API sends in a page of each list: 5,000
// of a crawl's urls, and 100 of the jobs.
var pageSizes = map[string]int{"urls": 5000, "jobs": 100}

// everyPage prints every record of op's list, one JSON object a line, page
// by page from op.start: a crawl's urls until its total, and the jobs until
// a page holds fewer than a page can. --fields chooses from each record. A
// failure after the first page names the --offset that reads on, since the
// records before it are printed. With -o FILE, the file is written once
// every page is read.
func (c apiClient) everyPage(op *operation) error {
	f := c.rt.fields
	var out bytes.Buffer
	stdout := c.rt.stdout
	if f != nil {
		f.listed, stdout = true, f.out
	}
	toFile := op.output != "" && op.output != "-"
	size, offset := pageSizes[op.records], op.start
	for first := true; ; first = false {
		data, err := c.call(&operation{method: "GET", path: fmt.Sprintf("%s?limit=%d&offset=%d", op.path, size, offset)})
		if err != nil {
			if !first && !toFile {
				return goOnFrom(err, op.line, offset)
			}
			return err
		}
		var page struct {
			Total *int `json:"total"`
		}
		_ = json.Unmarshal(data, &page)
		var items []json.RawMessage
		if fields, ok := members(data); ok {
			if at := slices.IndexFunc(fields, func(m member) bool { return m.key == op.records }); at >= 0 {
				items, _ = elements(fields[at].value)
			}
		}
		var lines []byte
		for _, item := range items {
			line := compacted(item)
			if f != nil {
				line = f.chooseOne(item)
			}
			lines = append(append(lines, line...), '\n')
		}
		// Whether --fields names anything there is the first page's to say.
		if first && f != nil && f.missedAll() {
			return f.missed()
		}
		if toFile {
			out.Write(lines)
		} else if err := write(stdout, lines); err != nil {
			return err
		}
		offset += len(items)
		if len(items) == 0 || page.Total != nil && offset >= *page.Total || page.Total == nil && len(items) < size {
			break
		}
	}
	if !toFile {
		return nil
	}
	if err := os.WriteFile(op.output, out.Bytes(), 0o644); err != nil {
		return err
	}
	if !c.rt.json && !c.rt.quiet {
		fmt.Fprint(c.rt.stderr, receipt(op.output, out.Bytes(), "", "", false))
	}
	return nil
}

// goOnFrom is err for a page after the first, once the records before offset
// are printed: its next step reads on from offset, unless it has its own.
func goOnFrom(err error, line []string, offset int) error {
	f := asFailure(err)
	next := commandLine(withOffset(line, offset))
	f.notes = append(f.notes, fmt.Sprintf("The records before offset %d are printed, and `%s` prints the rest.", offset, next))
	if f.nextStep == "" {
		f.nextStep = "run `" + next + "`"
	}
	return f
}
