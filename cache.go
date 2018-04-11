package main

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"sync"

	"github.com/google/go-github/github"
	ldap "gopkg.in/ldap.v2"
)

func updateLocalCache(
	ctx context.Context,
	githubClient *github.Client,
	ldapClient ldap.Client) (chan reportEntry, chan error) {

	var (
		chanEntries = make(chan reportEntry)
		chanErrs    = make(chan error, 1)
	)

	go func() {
		defer close(chanEntries)
		defer close(chanErrs)

		// Get the dev affiliates.
		_, devAffiliates, err := getDevAffiliations(ctx)
		if err != nil {
			chanErrs <- err
			return
		}

		// Get the members.
		chanUsers, chanUsersErrs := getMembers(
			ctx, githubClient, ldapClient, devAffiliates)

		// wg waits on all pending user operations to complete before
		// exiting the program
		var wg sync.WaitGroup
		defer wg.Wait()

		for {
			select {
			case <-ctx.Done():
				return

			case err, ok := <-chanUsersErrs:
				if ok {
					chanErrs <- err
					return
				}

			case user, ok := <-chanUsers:
				if !ok {
					return
				}

				wg.Add(1)

				go func(user *userWrapper) {
					defer wg.Done()

					// wg2 waits on all operations for *this* user to
					// complete before signalling wg that the user has
					// been processed.
					var wg2 sync.WaitGroup

					// Write the user to disk if the user was fetched live
					// or if it might have new e-mail information.
					if config.ldap ||
						!config.noFetchUsers ||
						!config.noFetchAffiliations {

						wg2.Add(1)
						go func() {
							defer wg2.Done()
							if err := writeUser(user); err != nil {
								chanErrs <- err
							}
						}()
					}

					// Write the issue details for the user
					wg2.Add(1)
					go func() {
						defer wg2.Done()
						err := writeIssueLog(ctx, githubClient, user)
						if err != nil {
							chanErrs <- err
						}
					}()

					// Write the git log details for the user
					if !config.noGit {
						wg2.Add(1)
						go func() {
							defer wg2.Done()
							err := writeGitLog(ctx, user)
							if err != nil {
								chanErrs <- err
							}
						}()
					}

					wg2.Wait()

					// Transform the user into a report entry.
					entry := reportEntry{
						Login:  user.GetLogin(),
						Name:   user.GetName(),
						Emails: user.Emails,
					}

					wg2.Add(2)
					go func() {
						updateEntryWithIssueReport(&entry, chanErrs)
						wg2.Done()
					}()
					go func() {
						updateEntryWithChangesetReport(&entry, chanErrs)
						wg2.Done()
					}()

					wg2.Wait()
					chanEntries <- entry
				}(user)
			}
		}
	}()

	return chanEntries, chanErrs
}

func updateEntryWithChangesetReport(
	entry *reportEntry, chanErrs chan error) {

	filePath := path.Join(config.outputDir, entry.Login, "commits", "data.json")
	if ok, err := fileExists(filePath); !ok {
		if err != nil {
			chanErrs <- err
			return
		}
		return
	}

	f, err := os.Open(filePath)
	if err != nil {
		chanErrs <- err
		return
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var report changesetReport
	if err := dec.Decode(&report); err != nil {
		chanErrs <- err
		return
	}

	entry.Commits = report.Commits
	entry.Additions = report.Additions
	entry.Deletions = report.Deletions
	entry.LatestCommitSHA = report.LatestCommitSHA
	if !report.LatestCommitDate.IsZero() {
		entry.LatestCommitDate = &report.LatestCommitDate
	}
}

func updateEntryWithIssueReport(
	entry *reportEntry, chanErrs chan error) {

	filePath := path.Join(config.outputDir, entry.Login, "issues", "data.json")
	if ok, err := fileExists(filePath); !ok {
		if err != nil {
			chanErrs <- err
			return
		}
		return
	}

	f, err := os.Open(filePath)
	if err != nil {
		chanErrs <- err
		return
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var report issueAndPullRequestReport
	if err := dec.Decode(&report); err != nil {
		chanErrs <- err
		return
	}

	entry.Issues = report.Issues
	entry.PullRequests = report.PullRequests
}
