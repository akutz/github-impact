package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type member struct {
	Login     string               `json:"login"`
	Name      string               `json:"name,omitempty"`
	LDAPLogin string               `json:"ldapLogin,omitempty"`
	Emails    uniqueStringSlice    `json:"emails,omitempty"`
	Employed  uniqueDateRangeSlice `json:"employed,omitempty"`
	Commits   []changeset          `json:"commits,omitempty"`
	//Issues    []issue              `json:"commits,omitempty"`
}

type uniqueStringSlice []string

func (u *uniqueStringSlice) append(s string) {
	if s == "" {
		return
	}
	for _, e := range *u {
		if e == s {
			return
		}
	}
	*u = append(*u, s)
}

type dateRange struct {
	From  *time.Time `json:"from,omitempty"`
	Until *time.Time `json:"until,omitempty"`
}

type uniqueDateRangeSlice []dateRange

func (u *uniqueDateRangeSlice) append(d dateRange) {
	for i := 0; i < len(*u); i++ {
		e := (*u)[i]
		if e.From != nil && d.From != nil &&
			e.From.Equal(*d.From) {
			if d.Until != nil {
				(*u)[i].Until = d.Until
			}
			return
		}
		if e.Until != nil && d.Until != nil &&
			e.Until.Equal(*d.Until) {
			if d.From != nil {
				(*u)[i].From = d.From
			}
			return
		}
	}
	*u = append(*u, d)
}

func (m member) filePath(opts options) string {
	return path.Join(opts.config.OutputDir, fmt.Sprintf("%s.json", m.Login))
}

func (m member) encode(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

func (m member) writeToDisk(opts options) error {
	f, err := os.Create(m.filePath(opts))
	if err != nil {
		return err
	}
	defer f.Close()
	return m.encode(f)
}

func (m *member) decode(r io.Reader) error {
	dec := json.NewDecoder(r)
	return dec.Decode(m)
}

func (m *member) loadFromDisk(opts options) error {
	f, err := os.Open(m.filePath(opts))
	if err != nil {
		return err
	}
	defer f.Close()
	return m.decode(f)
}

func (m *member) load(ctx context.Context, opts options) error {

	// Load the user from the local disk cache.
	if ok, err := fileExists(m.filePath(opts)); err != nil {
		return err
	} else if ok {
		if err := m.loadFromDisk(opts); err != nil {
			return err
		}
	}

	// Load the user from GitHub if allowed.
	if !opts.config.GitHub.NoUsers {
		if err := m.loadFromGitHub(ctx, opts); err != nil {
			return err
		}
	}

	// Load the user from LDAP if allowed.
	if !opts.config.LDAP.Disabled {
		if err := m.loadFromLDAP(ctx, opts); err != nil {
			return err
		}
	}

	// Load from the affiliates file.
	return m.loadFromAffiliates(ctx, opts)
}

func getMembers(ctx context.Context, opts options) (chan member, chan error) {

	var (
		chanMembersOut = make(chan member)
		chanErrsOut    = make(chan error, 1)
	)

	go func() {
		defer func() {
			close(chanMembersOut)
			close(chanErrsOut)
		}()

		var (
			chanMembersIn chan member
			chanErrsIn    chan error
		)
		// If there are non-flag arguments and resume is disabled then
		// return the user details for the specified usernames only.
		// Otherwise return all users.
		if len(opts.config.Args) > 0 && !opts.config.Resume {
			chanMembersIn, chanErrsIn = getNamedMembers(ctx, opts)
		} else {
			chanMembersIn, chanErrsIn = getAllMembers(ctx, opts)
		}

		// Write the members to disk before sending them into
		// the out channel.
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-chanErrsIn:
				if ok {
					chanErrsOut <- err
				}
				return
			case m, ok := <-chanMembersIn:
				if !ok {
					return
				}
				if !opts.config.Git.Disabled {
					if err := m.gitLog(ctx, opts); err != nil {
						chanErrsOut <- err
						return
					}
				}
				if err := m.writeToDisk(opts); err != nil {
					chanErrsOut <- err
					return
				}
				chanMembersOut <- m
			}
		}
	}()

	return chanMembersOut, chanErrsOut

}

func getNamedMembers(
	ctx context.Context, opts options) (chan member, chan error) {

	var (
		chanMembers = make(chan member)
		chanErrs    = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanMembers)
			close(chanErrs)
		}()

		// All non-flag arguments are considered GitHub user names.
		for i := 0; i < len(opts.config.Args) && ctx.Err() == nil; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				m := member{Login: opts.config.Args[i]}
				if err := m.load(ctx, opts); err != nil {
					chanErrs <- err
					return
				}
				chanMembers <- m
			}(i)
		}
	}()

	return chanMembers, chanErrs
}

func getAllMembers(
	ctx context.Context, opts options) (chan member, chan error) {

	var (
		chanMembers = make(chan member)
		chanErrs    = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanMembers)
			close(chanErrs)
		}()

		var (
			chanLogins     chan string
			chanLoginsErrs chan error
		)

		if opts.config.GitHub.NoUsers {
			chanLogins, chanLoginsErrs = getCachedLogins(ctx, opts)
		} else {
			chanLogins, chanLoginsErrs = fetchMemberLogins(ctx, opts)
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
				if opts.config.Resume && login < flag.Arg(0) {
					continue
				}

				// Indicate that there is now a user on which to wait
				wg.Add(1)

				go func(login string) {
					defer wg.Done()
					m := member{Login: login}
					if err := m.load(ctx, opts); err != nil {
						chanErrs <- err
						return
					}
					chanMembers <- m
				}(login)
			}
		}
	}()

	return chanMembers, chanErrs
}

func getCachedLogins(
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

		searchPatt := path.Join(opts.config.OutputDir, "*.json")
		matches, err := filepath.Glob(searchPatt)
		if err != nil {
			chanErrs <- err
			return
		}

		for i := 0; i < len(matches) && ctx.Err() == nil; i++ {
			wg.Add(1)
			go func(i int) {
				fileName := path.Base(matches[i])
				fileExt := path.Ext(fileName)
				login := strings.TrimSuffix(fileName, fileExt)
				chanLogins <- login
				wg.Done()
			}(i)
		}
	}()

	return chanLogins, chanErrs
}
