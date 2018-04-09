package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"path"
	"strconv"
	"sync"
	"time"
)

type reportEntry struct {
	Login            string      `json:"login,omitempty"`
	Name             string      `json:"name,omitempty"`
	Email            string      `json:"email,omitempty"`
	Additions        int         `json:"additions,omitempty"`
	Deletions        int         `json:"deletions,omitempty"`
	LatestCommitSHA  string      `json:"latestCommitSHA,omitempty"`
	LatestCommitDate *time.Time  `json:"latestCommitDate,omitempty"`
	Issues           issueReport `json:"issues,omitempty"`
	PullRequests     issueReport `json:"pullRequests,omitempty"`
}

func (r reportEntry) fields() []string {
	latestCommitDate := ""
	if v := r.LatestCommitDate; v != nil {
		latestCommitDate = v.String()
	}
	return []string{
		r.Login,
		r.Name,
		r.Email,
		strconv.Itoa(r.Additions),
		strconv.Itoa(r.Deletions),
		r.LatestCommitSHA,
		latestCommitDate,
		strconv.Itoa(r.Issues.Created),
		strconv.Itoa(r.Issues.Assigned),
		strconv.Itoa(r.Issues.Mentioned),
		strconv.Itoa(r.PullRequests.Created),
		strconv.Itoa(r.PullRequests.Assigned),
		strconv.Itoa(r.PullRequests.Mentioned),
		strconv.Itoa(r.PullRequests.Merged),
	}
}

var csvReportHeader = []string{
	"login",
	"name",
	"email",
	"additions",
	"deletions",
	"latestCommitSHA",
	"latestCommitDate",
	"issuesCreated",
	"issuesAssigned",
	"issuesMentioned",
	"pullRequestsCreated",
	"pullRequestsAssigned",
	"pullRequestsMentioned",
	"pullRequestsMerged",
}

func generateReport(
	ctx context.Context,
	chanEntries chan reportEntry,
	chanErrs chan error) error {

	// Create the report.csv file that will receive output in
	// addition to stdout.
	csvf, err := os.Create(path.Join(config.outputDir, "report.csv"))
	if err != nil {
		return err
	}
	defer csvf.Close()
	csvw := csv.NewWriter(io.MultiWriter(os.Stdout, csvf))
	defer csvw.Flush()
	csvw.Write(csvReportHeader)
	var csvwMu sync.Mutex
	writeCSV := func(entry reportEntry) error {
		csvwMu.Lock()
		defer csvwMu.Unlock()
		csvw.Write(entry.fields())
		csvw.Flush()
		return csvw.Error()
	}

	// Create the report.json file.
	jsonf, err := os.Create(path.Join(config.outputDir, "report.json"))
	if err != nil {
		return err
	}
	defer jsonf.Close()
	jsonEnc := json.NewEncoder(jsonf)
	jsonEnc.SetIndent("", "  ")
	var jsonMu sync.Mutex
	writeJSON := func(entry reportEntry) error {
		jsonMu.Lock()
		defer jsonMu.Unlock()
		return jsonEnc.Encode(entry)
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-chanErrs:
			return err
		case entry, ok := <-chanEntries:
			if !ok {
				return nil
			}
			if err := writeCSV(entry); err != nil {
				return err
			}
			if err := writeJSON(entry); err != nil {
				return err
			}
		}
	}
}
