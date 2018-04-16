package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

func newGitHubAPIClient(ctx context.Context, apiKey string) *github.Client {

	// Create a new token source.
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: apiKey})

	// Create a new Oauth2 client
	oauth2Client := oauth2.NewClient(ctx, tokenSource)

	// Create a new GitHub client.
	return github.NewClient(oauth2Client)
}

func (o options) waitForAPI() {
	o.chanAPI <- struct{}{}
}
func (o options) doneWithAPI() {
	go func() {
		time.Sleep(o.config.GitHub.API.Wait)
		<-o.chanAPI
	}()
}

func printRateLimit(rep *github.Response, opts options) {
	if rep != nil && opts.config.GitHub.API.ShowRateLimit {
		fmt.Fprintln(os.Stderr, formatRateReset(rep.Rate))
	}
}

// formatRateReset formats d to look like "[rate reset in 2s]" or
// "[rate reset in 87m02s]" for the positive durations. And like
// "[rate limit was reset 87m02s ago]" for the negative cases.
//
// copied from https://goo.gl/WyhwRV
func formatRateReset(r github.Rate) string {

	d := r.Reset.Time.Sub(time.Now())

	isNegative := d < 0
	if isNegative {
		d *= -1
	}
	secondsTotal := int(0.5 + d.Seconds())
	minutes := secondsTotal / 60
	seconds := secondsTotal - minutes*60

	var timeString string
	if minutes > 0 {
		timeString = fmt.Sprintf("%dm%02ds", minutes, seconds)
	} else {
		timeString = fmt.Sprintf("%ds", seconds)
	}

	if isNegative {
		return fmt.Sprintf(
			"[rate lim=%d, rem=%d, limit was reset %v ago]",
			r.Limit, r.Remaining, timeString)
	}
	return fmt.Sprintf(
		"[rate lim=%d, rem=%d, reset in %v]",
		r.Limit, r.Remaining, timeString)
}

func retryAfter(rep *github.Response, cur *int, opts options) bool {
	if *cur > opts.config.GitHub.API.Retries {
		return false
	}

	if rep == nil {
		return false
	}

	if v := rep.Header["Retry-After"]; len(v) > 0 {
		if secs, _ := strconv.Atoi(v[0]); secs > 0 {
			time.Sleep(time.Duration(secs) * time.Second)
		}
	} else if rep.StatusCode == 500 {
		time.Sleep(opts.config.GitHub.API.RetryWait)
	} else {
		return false
	}

	*cur++
	return true
}

func (m *member) loadFromGitHub(ctx context.Context, opts options) error {
	if opts.config.GitHub.NoUsers {
		return nil
	}
	retries := 0
	for {
		opts.waitForAPI()
		user, rep, err := opts.github.Users.Get(ctx, m.Login)
		opts.doneWithAPI()
		printRateLimit(rep, opts)
		if err != nil {
			if retryAfter(rep, &retries, opts) {
				continue
			}
			return err
		}
		if m.Name == "" {
			m.Name = user.GetName()
		}
		m.Emails.append(user.GetEmail())
		return nil
	}
}

func fetchMemberLogins(
	ctx context.Context, opts options) (chan string, chan error) {

	var (
		chanLogins = make(chan string)
		chanErrs   = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanLogins)
			close(chanErrs)
		}()

		// Get all available pages of data as long as the context
		// is not cancelled and there are additional pages to retrieve.
		listOpts := &github.ListMembersOptions{
			ListOptions: github.ListOptions{Page: 1},
		}

		retries := 0

		for ctx.Err() == nil && listOpts.Page > 0 {
			opts.waitForAPI()
			members, rep, err := opts.github.Organizations.ListMembers(
				ctx,
				opts.config.MemberOrg,
				listOpts)
			opts.doneWithAPI()
			printRateLimit(rep, opts)
			if err != nil {
				if retryAfter(rep, &retries, opts) {
					continue
				}
				chanErrs <- err
				return
			}

			for i := 0; i < len(members) && ctx.Err() == nil; i++ {
				if login := members[i].GetLogin(); login != "" {
					wg.Add(1)
					go func() {
						chanLogins <- login
						wg.Done()
					}()
				}
			}

			listOpts.Page = rep.NextPage
		}
	}()

	return chanLogins, chanErrs
}
