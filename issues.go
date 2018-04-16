// +build none

package main

import (
	"context"
	"sync"

	"github.com/google/go-github/github"
)

type issue struct {
	URL           string `json:"url"`
	Created       bool   `json:"created"`
	Commented     bool   `json:"commented"`
	IsPullRequest bool   `json:"isPullRequest"`
}

func (m *member) getIssues(ctx context.Context, opts options) error {

	var (
		wg         sync.WaitGroup
		chanIssues = make(chan *issueWrapper)
		chanErrs   = make(chan error, 1)
	)

	fetchIssuesWith := func(listOpts github.IssueListByRepoOptions) {
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

func fetchIssues(
	ctx context.Context,
	listOpts github.IssueListByRepoOptions,
	opts options) (chan *issueWrapper, chan error) {

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
		opts.State = "all"

		retries := 0

		for ctx.Err() == nil && opts.Page > 0 {
			waitForAPI()
			issues, rep, err := client.Issues.ListByRepo(
				ctx,
				config.targetOrg,
				config.targetRepo,
				&opts)
			doneWithAPI()
			printRateLimit(rep)
			if err != nil {
				if retryAfter(rep, &retries) {
					continue
				}
				chanErrs <- err
				return
			}

			for i := 0; i < len(issues) && ctx.Err() == nil; i++ {
				wg.Add(1)
				go func(i int) {
					issue := &issueWrapper{Issue: *issues[i]}
					if !config.noFetchPullRequests && issue.IsPullRequest() {
						retries := 0
						for {
							waitForAPI()
							pr, rep, err := client.PullRequests.Get(
								ctx,
								config.targetOrg,
								config.targetRepo,
								issue.GetNumber())
							doneWithAPI()
							if err != nil {
								if retryAfter(rep, &retries) {
									continue
								}
								chanErrs <- err
								return
							}
							issue.MergedAt = pr.MergedAt
							break
						}
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
