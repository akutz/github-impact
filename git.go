package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

type changesetEntry struct {
	Add  int    `json:"add"`
	Del  int    `json:"del"`
	Path string `json:"path"`
}

type changeset struct {
	Short       string           `json:"shaShort"`
	Long        string           `json:"shaLong"`
	Subject     string           `json:"subject,omitempty"`
	AuthorName  string           `json:"authorName,omitempty"`
	AuthorEmail string           `json:"authorEmail,omitempty"`
	AuthorDate  time.Time        `json:"authorDate"`
	Changes     []changesetEntry `json:"changes"`
}

func (o options) waitForGit() {
	o.chanGit <- struct{}{}
}
func (o options) doneWithGit() {
	<-o.chanGit
}

func git(
	opts options,
	args ...string) (io.Reader, func(), func() error, error) {

	opts.waitForGit()

	args = append([]string{
		"--no-pager",
		"--git-dir",
		opts.config.Git.TargetDir,
	}, args...)
	cmd := exec.Command("git", args...)

	if opts.config.Debug {
		log.Printf("%v\n", cmd.Args)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		opts.doneWithGit()
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		opts.doneWithGit()
		return nil, nil, nil, err
	}

	return stdout, opts.doneWithGit, cmd.Wait, nil
}

// gitLog gets the changesets for the user's available e-mail addresses.
func (m *member) gitLog(ctx context.Context, opts options) error {
	changesets := map[string]changeset{}
	knownChangesets := map[string]struct{}{}

	// Add the existing changesets to the list so dupes don't get added.
	for _, cs := range m.Commits {
		knownChangesets[cs.Long] = struct{}{}
	}

	for _, email := range m.Emails {
		if err := m.findChangesets(
			ctx, email, knownChangesets, changesets, opts); err != nil {
			return err
		}
	}
	for _, commit := range changesets {
		m.Commits = append(m.Commits, commit)
	}
	return nil
}

// findChangesets finds the changesets for the provided author.
func (m member) findChangesets(
	ctx context.Context,
	author string,
	knownChangesets map[string]struct{},
	changesets map[string]changeset,
	opts options) error {

	r, done, wait, err := git(
		opts,
		"log",
		"--author",
		author,
		`--format=format:%h%n%H%n%s%n%an%n%ae%n%at`,
		"--numstat")
	if err != nil {
		return err
	}
	defer done()

	var (
		scan     = bufio.NewScanner(r)
		addDelRX = regexp.MustCompile(`^(\-|\d+)\s+(\-|\d+)\s*([^\s].*)$`)
	)

	// COMMIT_ID_SHORT
	// COMMIT_ID_LONG
	// SUBJECT
	// AUTHOR_NAME
	// AUTHOR_EMAIL
	// AUTHOR_DATE (UNIX epoch)
	// ADD_N     DEL_N     FILE_NAME
	// ADD_N     DEL_N     FILE_NAME
	// ...
	// <BLANK LINE>
	var doNotScan bool
	for ctx.Err() == nil {
		if !doNotScan {
			doNotScan = false
			if !scan.Scan() {
				break
			}
		}
		var cur changeset
		cur.Short = scan.Text()
		scan.Scan()
		cur.Long = scan.Text()
		scan.Scan()
		cur.Subject = scan.Text()
		scan.Scan()
		cur.AuthorName = scan.Text()
		scan.Scan()
		cur.AuthorEmail = scan.Text()
		scan.Scan()
		epoch, _ := strconv.ParseInt(scan.Text(), 10, 64)
		cur.AuthorDate = time.Unix(epoch, 0)
		if opts.config.UTC {
			cur.AuthorDate = cur.AuthorDate.UTC()
		}

		// Advance to the next line. This may or may not be the
		// next commit. The issue is when additions/deletions are
		// absent. When this is the case there is no break between
		// one commit to the next. Injecting a new line into the
		// format doesn't help because it would still be necessary to
		// test to see if additions/deletions are encountered.
		if !scan.Scan() {
			break
		}

		// Check to see if the current line is an additions/deletion line
		if !addDelRX.MatchString(scan.Text()) {
			// No additions/deletion found. Jump to the next iteration
			// of this for loop, but indicate that the next Scan() should
			// be disabled since we're already sitting at the top of a
			// new commit entry.
			doNotScan = true
			continue
		}

		// At this point the line is an additions/deletions line
		for scan.Text() != "" {
			var entry changesetEntry
			match := addDelRX.FindStringSubmatch(scan.Text())
			if len(match) != 4 {
				return fmt.Errorf(
					"error matching changeset add/del line: "+
						"login=%s, author=%s, line=%s, changeset=%+v",
					m.Login, author, scan.Text(), cur)
			}
			entry.Add, _ = strconv.Atoi(match[1])
			entry.Del, _ = strconv.Atoi(match[2])
			entry.Path = match[3]

			cur.Changes = append(cur.Changes, entry)

			if !scan.Scan() {
				break
			}
		}

		if _, ok := knownChangesets[cur.Long]; ok {
			// Ignore existing commit
			continue
		}

		// Only count the commit if it occurred during the time which
		// the member was employed with the source organization.
		var validCommit bool
		for _, e := range m.Employed {
			if e.From != nil && cur.AuthorDate.After(*e.From) {
				if e.Until == nil || cur.AuthorDate.Before(*e.Until) {
					validCommit = true
					break
				}
			}
		}

		if validCommit {
			changesets[cur.Long] = cur
		} else if opts.config.Debug {
			log.Printf("ignoring commit: sha=%s, date=%s, author=%s <%s>",
				cur.Short, cur.AuthorDate, cur.AuthorName, cur.AuthorEmail)
		}
	}

	return wait()
}
