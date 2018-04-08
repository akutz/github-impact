package main

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"sync"

	"github.com/google/go-github/github"
)

func updateLocalCache(
	ctx context.Context,
	client *github.Client) (chan reportEntry, chan error) {

	var (
		chanEntries = make(chan reportEntry)
		chanErrs    = make(chan error, 1)
	)

	go func() {
		defer close(chanEntries)
		defer close(chanErrs)

		// Get the members.
		chanUsers, chanUsersErrs := getMembers(ctx, client)

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

				go func(user *github.User) {
					defer wg.Done()

					// wg2 waits on all operations for *this* user to
					// complete before signalling wg that the user has
					// been processed.
					var wg2 sync.WaitGroup

					// Write the user to disk if the user was fetched live.
					if !config.noFetchUsers {
						wg2.Add(1)
						go func() {
							defer wg2.Done()
							if err := writeUser(user); err != nil {
								chanErrs <- err
							}
						}()
					}

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
						Login: user.GetLogin(),
						Name:  user.GetName(),
						Email: user.GetEmail(),
					}

					updateEntryWithChangesetTotals(&entry, chanErrs)

					chanEntries <- entry
				}(user)
			}
		}
	}()

	return chanEntries, chanErrs
}

func updateEntryWithChangesetTotals(
	entry *reportEntry, chanErrs chan error) {

	changesetTotalsPath := path.Join(
		config.outputDir, entry.Login,
		"commits", "data.json")
	if ok, err := fileExists(changesetTotalsPath); !ok {
		if err != nil {
			chanErrs <- err
		}
		return
	}

	// Open the changeset totals file.
	f, err := os.Open(changesetTotalsPath)
	if err != nil {
		chanErrs <- err
		return
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var cst changesetTotals
	if err := dec.Decode(&cst); err != nil {
		chanErrs <- err
		return
	}

	entry.Additions = cst.Additions
	entry.Deletions = cst.Deletions
	entry.LatestCommitSHA = cst.LatestCommitSHA
	if !cst.LatestCommitDate.IsZero() {
		entry.LatestCommitDate = &cst.LatestCommitDate
	}
}
