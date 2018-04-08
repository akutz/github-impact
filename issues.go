package main

import (
	"context"
	"sync"

	"github.com/google/go-github/github"
)

func getIssues(
	ctx context.Context,
	client *github.Client,
	login string) (chan *github.Issue, chan error) {

	if config.noFetchIssues {
		return loadIssuesFromDisk(login)
	}

	//return fetchIssues(ctx, client, login)
	return nil, nil
}

func loadIssuesFromDisk(login string) (chan *github.Issue, chan error) {
	var (
		chanIssues = make(chan *github.Issue)
		chanErrs   = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanIssues)
			close(chanErrs)
		}()
	}()

	return chanIssues, chanErrs
}

func fetchIssues(
	ctx context.Context,
	client *github.Client,
	opts github.IssueListByRepoOptions) (chan *github.Issue, chan error) {

	var (
		chanIssues = make(chan *github.Issue)
		chanErrs   = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanIssues)
			close(chanErrs)
		}()

		opts.Direction = "asc"
		opts.Sort = "created"
		opts.Page = 1

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
					chanIssues <- issues[i]
					wg.Done()
				}(i)
			}

			opts.Page = rep.NextPage
		}
	}()

	return chanIssues, chanErrs
}
