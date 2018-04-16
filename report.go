package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

var csvReportHeader = []string{
	"login",
	"name",
	"emails",
	"commits",
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

func (m member) csvFields(opts options) []string {
	var (
		additions              int
		deletions              int
		latestCommitSHA        string
		latestCommitDate       time.Time
		latestCommitDateString string
	)
	for _, c := range m.Commits {
		if c.AuthorDate.After(latestCommitDate) {
			latestCommitSHA = c.Short
			latestCommitDate = c.AuthorDate
			if opts.config.UTC {
				latestCommitDate = latestCommitDate.UTC()
			}
			//Mon Jan 2 15:04:05 -0700 MST 2006
			latestCommitDateString = latestCommitDate.Format(
				"2006-01-02:15:04:05-07")
		}
		for _, ce := range c.Changes {
			additions = additions + ce.Add
			deletions = deletions + ce.Del
		}
	}

	return []string{
		m.Login,
		m.Name,
		strings.Join(m.Emails, "|"),
		strconv.Itoa(len(m.Commits)),
		strconv.Itoa(additions),
		strconv.Itoa(deletions),
		latestCommitSHA,
		latestCommitDateString,
		"", //strconv.Itoa(r.Issues.Created),
		"", //strconv.Itoa(r.Issues.Assigned),
		"", //strconv.Itoa(r.Issues.Mentioned),
		"", //strconv.Itoa(r.PullRequests.Created),
		"", //strconv.Itoa(r.PullRequests.Assigned),
		"", //strconv.Itoa(r.PullRequests.Mentioned),
		"", //strconv.Itoa(r.PullRequests.Merged),
	}
}

type issueReport struct {
	Created   int `json:"created,omitempty"`
	Assigned  int `json:"assigned,omitempty"`
	Mentioned int `json:"mentioned,omitempty"`
	Merged    int `json:"merged,omitempty"`
}

type issueAndPullRequestReport struct {
	Login        string      `json:"login,omitempty"`
	Issues       issueReport `json:"issues,omitempty"`
	PullRequests issueReport `json:"pullRequests,omitempty"`
}

func (i issueReport) hasIssues() bool {
	return i.Created > 0 || i.Assigned > 0 || i.Mentioned > 0
}

func (i issueAndPullRequestReport) hasIssues() bool {
	return i.Issues.hasIssues() || i.PullRequests.hasIssues()
}

func writeReport(
	ctx context.Context, chanMembers chan member, opts options) error {

	var reportName string
	if args := opts.config.Args; len(args) > 0 {
		reportName = fmt.Sprintf("report-%s", strings.Join(args, "+"))
	} else {
		reportName = "report"
	}

	// Create the CSV report file and CSV writer for stdout.
	// An io.Multiwriter is not used because stdout receives all
	// members, but the report only receives members that have
	// activity.
	csvFileName := fmt.Sprintf("%s.csv", reportName)
	csvFilePath := path.Join(opts.config.OutputDir, csvFileName)
	csvf, err := os.Create(csvFilePath)
	if err != nil {
		return err
	}
	defer csvf.Close()

	csvw := csv.NewWriter(csvf)
	defer csvw.Flush()
	csvw.Write(csvReportHeader)
	csvw.Flush()
	if err := csvw.Error(); err != nil {
		return err
	}

	csvo := csv.NewWriter(os.Stdout)
	defer csvo.Flush()
	csvo.Write(csvReportHeader)
	csvo.Flush()
	if err := csvo.Error(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-chanMembers:
			if !ok {
				return nil
			}

			fields := m.csvFields(opts)
			csvo.Write(fields)
			csvo.Flush()
			if err := csvo.Error(); err != nil {
				return err
			}

			// Do not report entries with no commits.
			if len(m.Commits) == 0 {
				continue
			}

			// Encode to CSV
			csvw.Write(fields)
			csvw.Flush()
			if err := csvw.Error(); err != nil {
				return err
			}
		}
	}
}
