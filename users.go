package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path"
	"sync"

	"github.com/google/go-github/github"
	ldap "gopkg.in/ldap.v2"
)

type userWrapper struct {
	github.User
	Emails []string `json:"emails,omitempty"`
}

func getMembers(
	ctx context.Context,
	githubClient *github.Client,
	ldapClient ldap.Client,
	affiliates map[string]*devAffiliation) (chan *userWrapper, chan error) {

	// If there are non-flag arguments and resume is disabled then
	// return the user details for the specified usernames only.
	// Otherwise return all users.
	if len(config.args) > 0 && !config.resume {
		return getNamedMembers(ctx, githubClient, ldapClient, affiliates)
	}

	return getAllMembers(ctx, githubClient, ldapClient, affiliates)
}

func getNamedMembers(
	ctx context.Context,
	githubClient *github.Client,
	ldapClient ldap.Client,
	affiliates map[string]*devAffiliation) (chan *userWrapper, chan error) {

	var (
		chanUsers = make(chan *userWrapper)
		chanErrs  = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanUsers)
			close(chanErrs)
		}()

		// All non-flag arguments are considered GitHub user names.
		for i := 0; i < len(config.args) && ctx.Err() == nil; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				login := config.args[i]
				user, err := loadOrGetUser(
					ctx, githubClient, ldapClient, affiliates, login)
				if err != nil {
					chanErrs <- err
					return
				}
				chanUsers <- user
			}(i)
		}
	}()

	return chanUsers, chanErrs
}

func getAllMembers(
	ctx context.Context,
	githubClient *github.Client,
	ldapClient ldap.Client,
	affiliates map[string]*devAffiliation) (chan *userWrapper, chan error) {

	var (
		chanUsers = make(chan *userWrapper)
		chanErrs  = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanUsers)
			close(chanErrs)
		}()

		var (
			chanLogins     chan string
			chanLoginsErrs chan error
		)

		if config.noFetchUsers {
			chanLogins, chanLoginsErrs = getCachedLogins(ctx)
		} else {
			chanLogins, chanLoginsErrs = fetchMemberLogins(ctx, githubClient)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-chanLoginsErrs:
				if ok {
					chanErrs <- err
				}
				return
			case login, ok := <-chanLogins:
				if !ok {
					return
				}

				// If resume mode is enabled then only process
				// the member if their login name is >= the
				// first command-line argument
				if config.resume && login < flag.Arg(0) {
					continue
				}

				// Indicate that there is now a user on which to wait
				wg.Add(1)

				go func(login string) {
					defer wg.Done()
					user, err := loadOrGetUser(
						ctx, githubClient, ldapClient, affiliates, login)
					if err != nil {
						chanErrs <- err
						return
					}
					chanUsers <- user
				}(login)
			}
		}
	}()

	return chanUsers, chanErrs
}

func loadOrGetUser(
	ctx context.Context,
	githubClient *github.Client,
	ldapClient ldap.Client,
	affiliates map[string]*devAffiliation,
	login string) (user *userWrapper, err error) {

	if config.noFetchUsers {
		// Load the user from the local disk cache.
		user, err = loadUserFromDisk(login)
	} else {
		// Get the user via the GitHub API
		user, err = getUser(ctx, githubClient, login)
	}

	if err != nil {
		return nil, err
	}

	user.Emails = []string{}
	knownEmails := map[string]struct{}{}

	supplementWithAffiliates(user, knownEmails, affiliates)

	if err := supplementWithLDAP(
		ctx, ldapClient, user, knownEmails); err != nil {
		return nil, err
	}

	return
}

func getCachedLogins(
	ctx context.Context) (chan string, chan error) {

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

		loginDirNames, err := getLoginDirNames()
		if err != nil {
			chanErrs <- err
		}

		for i := 0; i < len(loginDirNames) && ctx.Err() == nil; i++ {
			wg.Add(1)
			go func(i int) {
				chanLogins <- loginDirNames[i]
				wg.Done()
			}(i)
		}
	}()

	return chanLogins, chanErrs
}

func fetchMemberLogins(
	ctx context.Context,
	client *github.Client) (chan string, chan error) {

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
		opts := &github.ListMembersOptions{
			ListOptions: github.ListOptions{Page: 1},
		}

		retries := 0

		for ctx.Err() == nil && opts.Page > 0 {
			waitForAPI()
			members, rep, err := client.Organizations.ListMembers(
				ctx,
				config.memberOrg,
				opts)
			doneWithAPI()
			printRateLimit(rep)
			if err != nil {
				if retryAfter(rep, &retries) {
					continue
				}
				chanErrs <- err
				return
			}

			for i := 0; i < len(members) && ctx.Err() == nil; i++ {
				wg.Add(1)
				go func(i int) {
					chanLogins <- members[i].GetLogin()
					wg.Done()
				}(i)
			}

			opts.Page = rep.NextPage
		}
	}()

	return chanLogins, chanErrs
}

func loadUserFromDisk(login string) (*userWrapper, error) {
	filePath := path.Join(config.outputDir, login, "data.json")
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var user userWrapper
	if err := dec.Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

func getUser(
	ctx context.Context,
	client *github.Client,
	login string) (*userWrapper, error) {

	retries := 0
	for {
		waitForAPI()
		user, rep, err := client.Users.Get(ctx, login)
		doneWithAPI()
		printRateLimit(rep)
		if err != nil {
			if retryAfter(rep, &retries) {
				continue
			}
			return nil, err
		}
		return &userWrapper{User: *user}, nil
	}

}

func writeUser(user *userWrapper) error {
	// Ensure the user's directory exists.
	dirPath := path.Join(config.outputDir, user.GetLogin())
	os.MkdirAll(dirPath, 0755)

	filePath := path.Join(dirPath, "data.json")
	f, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(user); err != nil {
		return err
	}

	return nil
}
