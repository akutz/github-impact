package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
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
	Additions   int              `json:"additions"`
	Deletions   int              `json:"deletions"`
	Changes     []changesetEntry `json:"changes"`
}

type changesetReport struct {
	AuthorName       string    `json:"authorName,omitempty"`
	AuthorEmail      string    `json:"authorEmail,omitempty"`
	LatestCommitSHA  string    `json:"latestCommitSHA"`
	LatestCommitDate time.Time `json:"latestCommitDate"`
	Additions        int       `json:"additions"`
	Deletions        int       `json:"deletions"`
}

// Only allow n git operations at a time or the system hangs
var chanGit chan struct{}

func git(args ...string) (io.Reader, func(), func() error, error) {
	chanGit <- struct{}{}

	args = append([]string{
		"--no-pager",
		"--git-dir",
		config.targetGitDir,
	}, args...)
	cmd := exec.Command("git", args...)

	if debug {
		log.Printf("%v\n", cmd.Args)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		<-chanGit
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		<-chanGit
		return nil, nil, nil, err
	}

	return stdout, func() { <-chanGit }, cmd.Wait, nil
}

func writeGitLog(
	ctx context.Context,
	user *github.User) error {

	var (
		login      = user.GetLogin()
		name       = user.GetName()
		email      = user.GetEmail()
		changesets = map[string]changeset{}
	)

	// Get the changesets for the user with information starting
	// with the most specific to the least specific.
	if email != "" {
		if err := gitChangesets(ctx, login, email, changesets); err != nil {
			return err
		}
	}
	//if err := gitChangesets(ctx, login, login, changesets); err != nil {
	//	return err
	//}

	// Because searching on a name is fuzzy at best, the name must be at
	// least eight characters long and include a space to indicate a first
	// and last name is present.
	if len(name) >= 8 && strings.Contains(name, " ") {
		if err := gitChangesets(ctx, login, name, changesets); err != nil {
			return err
		}
	}

	dirPath := path.Join(config.outputDir, login, "commits")

	// Remove stale data
	os.RemoveAll(dirPath)

	if len(changesets) == 0 {
		return nil
	}

	os.MkdirAll(dirPath, 0755)
	csr := changesetReport{
		AuthorName:  name,
		AuthorEmail: email,
	}
	for _, cur := range changesets {
		csr.Additions = csr.Additions + cur.Additions
		csr.Deletions = csr.Deletions + cur.Deletions
		if v := cur.AuthorDate; v.After(csr.LatestCommitDate) {
			csr.LatestCommitSHA = cur.Long
			csr.LatestCommitDate = v
		}

		fileName := fmt.Sprintf("%s.json", cur.Short)
		filePath := path.Join(dirPath, fileName)

		f, err := os.Create(filePath)
		if err != nil {
			return err
		}
		defer f.Close()

		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(cur); err != nil {
			return err
		}
	}

	f, err := os.Create(path.Join(dirPath, "data.json"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(csr); err != nil {
		return err
	}

	return nil
}

// gitChangesets returns the changesets for an author
func gitChangesets(
	ctx context.Context,
	login, author string,
	changesets map[string]changeset) error {

	r, done, wait, err := git(
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
		if config.utc {
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
			m := addDelRX.FindStringSubmatch(scan.Text())
			if len(m) != 4 {
				return fmt.Errorf(
					"error matching changeset add/del line: "+
						"login=%s, author=%s, line=%s, changeset=%+v",
					login, author, scan.Text(), cur)
			}
			entry.Add, _ = strconv.Atoi(m[1])
			entry.Del, _ = strconv.Atoi(m[2])
			entry.Path = m[3]

			cur.Additions = cur.Additions + entry.Add
			cur.Deletions = cur.Deletions + entry.Del
			cur.Changes = append(cur.Changes, entry)

			if !scan.Scan() {
				break
			}
		}

		changesets[cur.Long] = cur
	}

	return wait()
}
