package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/go-github/github"
)

type issueWrapper struct {
	github.Issue
	MergedAt *time.Time `json:"mergedAt,omitempty"`
}

func (i issueWrapper) merged() bool {
	return i.IsPullRequest() && i.MergedAt != nil && !i.MergedAt.IsZero()
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

func writeIssueLog(
	ctx context.Context,
	client *github.Client,
	user *github.User) error {

	var (
		report         issueAndPullRequestReport
		login          = user.GetLogin()
		issueNumbers   = map[int]struct{}{}
		issueNumbersMu sync.Mutex
	)

	// Ensure the issue directory exists.
	dirPath := path.Join(config.outputDir, login, "issues")
	os.MkdirAll(dirPath, 0755)

	syncWriteIssue := func(issue *issueWrapper) error {
		issueNumbersMu.Lock()
		defer issueNumbersMu.Unlock()
		if _, ok := issueNumbers[issue.GetNumber()]; ok {
			return nil
		}
		issueNumbers[issue.GetNumber()] = struct{}{}
		return writeIssue(login, dirPath, issue)
	}

	updateIssueReport := func(i *issueWrapper, r *issueReport) {
		var createdOrAssigned bool
		if u := i.GetUser(); u != nil && u.GetLogin() == login {
			createdOrAssigned = true
			r.Created = r.Created + 1
			if i.merged() {
				r.Merged = r.Merged + 1
			}
		}
		if u := i.GetAssignee(); u != nil && u.GetLogin() == login {
			createdOrAssigned = true
			r.Assigned = r.Assigned + 1
		}
		if !createdOrAssigned {
			r.Mentioned = r.Mentioned + 1
		}
	}

	writeIssues := func() error {
		chanIssues, chanErrs := getIssues(ctx, client, login)
		for {
			select {
			case <-ctx.Done():
				return nil
			case err, ok := <-chanErrs:
				if ok {
					return err
				}
				return nil
			case issue, ok := <-chanIssues:
				if !ok {
					return nil
				}
				if issue.IsPullRequest() {
					updateIssueReport(issue, &report.PullRequests)
				} else {
					updateIssueReport(issue, &report.Issues)
				}
				if err := syncWriteIssue(issue); err != nil {
					return err
				}
			}
		}
	}

	if err := writeIssues(); err != nil {
		return err
	}

	if !report.hasIssues() {
		os.RemoveAll(dirPath)
		return nil
	}

	f, err := os.Create(path.Join(dirPath, "data.json"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func getIssues(
	ctx context.Context,
	client *github.Client,
	login string) (chan *issueWrapper, chan error) {

	if config.noFetchIssues {
		return loadIssuesFromDisk(ctx, login)
	}

	var (
		wg         sync.WaitGroup
		chanIssues = make(chan *issueWrapper)
		chanErrs   = make(chan error, 1)
	)

	fetchIssuesWith := func(opts github.IssueListByRepoOptions) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chanIssuesIn, chanErrsIn := fetchIssues(ctx, client, opts)
			for {
				select {
				case <-ctx.Done():
					return
				case err, ok := <-chanErrsIn:
					if ok {
						chanErrs <- err
					}
					return
				case issue, ok := <-chanIssuesIn:
					if !ok {
						return
					}
					chanIssues <- issue
				}
			}
		}()
	}

	fetchIssuesWith(github.IssueListByRepoOptions{Creator: login})
	fetchIssuesWith(github.IssueListByRepoOptions{Assignee: login})
	fetchIssuesWith(github.IssueListByRepoOptions{Mentioned: login})

	go func() {
		wg.Wait()
		close(chanIssues)
		close(chanErrs)
	}()

	return chanIssues, chanErrs
}

func loadIssuesFromDisk(
	ctx context.Context,
	login string) (chan *issueWrapper, chan error) {

	var (
		chanIssues = make(chan *issueWrapper)
		chanErrs   = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanIssues)
			close(chanErrs)
		}()

		files, err := filepath.Glob(
			path.Join(config.outputDir, login, "issues", "*.json"))
		if err != nil {
			chanErrs <- err
			return
		}

		for i := 0; i < len(files) && ctx.Err() == nil; i++ {
			if path.Base(files[i]) == "data.json" {
				continue
			}
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				f, err := os.Open(files[i])
				if err != nil {
					chanErrs <- err
					return
				}
				defer f.Close()
				dec := json.NewDecoder(f)
				var issue issueWrapper
				if err := dec.Decode(&issue); err != nil {
					chanErrs <- err
					return
				}
				chanIssues <- &issue
			}(i)
		}
	}()

	return chanIssues, chanErrs
}

func fetchIssues(
	ctx context.Context,
	client *github.Client,
	opts github.IssueListByRepoOptions) (chan *issueWrapper, chan error) {

	var (
		chanIssues = make(chan *issueWrapper)
		chanErrs   = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanIssues)
			close(chanErrs)
		}()

		opts.Page = 1
		opts.Sort = "created"
		opts.State = "all"
		opts.Direction = "asc"

		for ctx.Err() == nil && opts.Page > 0 {
			issues, rep, err := client.Issues.ListByRepo(
				ctx,
				config.targetOrg,
				config.targetRepo,
				&opts)
			if err != nil {
				chanErrs <- err
				return
			}

			printRateLimit(rep.Rate)

			for i := 0; i < len(issues) && ctx.Err() == nil; i++ {
				wg.Add(1)
				go func(i int) {
					issue := &issueWrapper{Issue: *issues[i]}
					if issue.IsPullRequest() {
						pr, _, err := client.PullRequests.Get(
							ctx,
							config.targetOrg,
							config.targetRepo,
							issue.GetNumber())
						if err != nil {
							chanErrs <- err
							return
						}
						issue.MergedAt = pr.MergedAt
					}
					chanIssues <- issue
					wg.Done()
				}(i)
			}

			opts.Page = rep.NextPage
		}
	}()

	return chanIssues, chanErrs
}

func writeIssue(login, dirPath string, issue *issueWrapper) error {
	fileName := fmt.Sprintf("%d.json", issue.GetNumber())
	f, err := os.Create(path.Join(dirPath, fileName))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(issue)
}
