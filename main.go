package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

var debug, _ = strconv.ParseBool(os.Getenv("DEBUG"))

var config struct {
	args          []string
	logLevel      string
	outputDir     string
	memberOrg     string
	targetOrg     string
	targetRepo    string
	resume        bool
	showRateLimit bool
	targetGitDir  string
	gitMax        int
	noGit         bool
	noFetchIssues bool
	noFetchUsers  bool
}

func main() {
	flag.Usage = usage

	// Parse the GitHub API key.
	githubAPIKey := os.Getenv("GITHUB_API_KEY")
	if githubAPIKey == "" {
		fmt.Fprintln(os.Stderr, "GITHUB_API_KEY required")
		os.Exit(1)
	}

	// Set up the flags
	flag.StringVar(
		&config.outputDir, "output", "data",
		"The output directory")
	flag.StringVar(
		&config.memberOrg, "member-org", "vmware",
		"The source GitHub org")
	flag.StringVar(
		&config.targetOrg, "target-org", "kubernetes",
		"The targeted GitHub org")
	flag.StringVar(
		&config.targetRepo, "target-repo", "kubernetes",
		"The targeted GitHub repo")
	flag.BoolVar(
		&config.resume, "resume", false,
		"Resume at the specified member name. An errors occurs if "+
			"more than one username is specified.")
	flag.BoolVar(
		&config.noFetchUsers, "no-fetch-users", false,
		"Do not update local user cache")
	flag.BoolVar(
		&config.showRateLimit, "show-rate-limit", debug,
		"Shows the rate limit info after all API calls")

	// Check to see if the git command is in the path.
	if exec.Command("git", "version").Run() == nil {
		var defaultTargetGitDir string
		if goPath := getGoPath(); goPath != "" {
			gitDir := path.Join(
				goPath,
				"src",
				"github.com",
				config.targetOrg,
				config.targetRepo,
				".git")
			if ok, err := fileExists(gitDir); !ok {
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			} else {
				defaultTargetGitDir = gitDir
			}
		}
		flag.StringVar(
			&config.targetGitDir, "target-git-dir", defaultTargetGitDir,
			"The path to the git directory to search for commit activity")
		flag.BoolVar(
			&config.noGit, "no-git", false,
			"Do not write git commit activity")
		flag.IntVar(
			&config.gitMax, "git-max", 10,
			"Number of max concurrent git commands")
	} else {
		config.noGit = true
	}

	// Parse the flags
	flag.Parse()

	chanGit = make(chan struct{}, config.gitMax)

	// Remove duplicate args
	if !config.resume {
		config.args = unique(flag.Args())
	} else if flag.NArg() != 1 {
		fmt.Fprintln(
			os.Stderr,
			"The flag -resume must be used with a single username")
		flag.Usage()
		os.Exit(1)
	}

	// Ensure the outut directory exists
	os.MkdirAll(config.outputDir, 0755)

	// Create a new token source.
	tokenSource := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: githubAPIKey},
	)

	// Create a new Oauth2 client
	ctx := context.Background()
	oauth2Client := oauth2.NewClient(ctx, tokenSource)

	// Create a new GitHub client.
	client := github.NewClient(oauth2Client)

	var (
		chanUsers chan *github.User
		chanErrs  chan error
	)

	// If there are non-flag arguments and resume is disabled then
	// return the user details for the specified usernames only.
	// Otherwise return all users.
	if len(config.args) > 0 && !config.resume {
		chanUsers, chanErrs = fetchNamedMembers(ctx, client)
	} else {
		chanUsers, chanErrs = fetchAllMembers(ctx, client)
	}

	// wg waits on all pending user operations to complete before
	// exiting the program
	var wg sync.WaitGroup

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, ctx.Err())
			wg.Wait()
			os.Exit(1)

		case err, ok := <-chanErrs:
			if ok {
				fmt.Fprintln(os.Stderr, err)
				wg.Wait()
				os.Exit(1)
			}

		case user, ok := <-chanUsers:
			if !ok {
				wg.Wait()
				os.Exit(0)
			}

			wg.Add(1)

			go func(user *github.User) {
				// wg2 waits on all operations for *this* user to
				// complete before signalling wg that the user has
				// been processed.
				var wg2 sync.WaitGroup
				defer func() {
					wg2.Wait()
					fmt.Println(user.GetLogin())
					wg.Done()
				}()

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
						err := writeGitLogForAuthor(ctx, user)
						if err != nil {
							chanErrs <- err
						}
					}()
				}

			}(user)
		}
	}
}

func fetchNamedMembers(
	ctx context.Context,
	client *github.Client) (chan *github.User, chan error) {

	var (
		chanUsers = make(chan *github.User)
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
		wg.Add(len(config.args))
		for _, login := range config.args {
			go func(login string) {
				defer wg.Done()

				// If the context has been cancelled then do
				// not process this user. This is handled here
				// so that the above wg.Done() call gets
				// invoked and the program doesn't deadlock.
				if ctx.Err() != nil {
					return
				}

				// Get the user details.
				if config.noFetchUsers {
					user, err := loadUserFromDisk(login)
					if err != nil {
						chanErrs <- err
						return
					}
					chanUsers <- user
				} else {
					user, err := getUser(ctx, client, login)
					if err != nil {
						chanErrs <- err
						return
					}
					chanUsers <- user
				}
			}(login)
		}
	}()

	return chanUsers, chanErrs
}

func fetchAllMembers(
	ctx context.Context,
	client *github.Client) (chan *github.User, chan error) {

	var (
		chanUsers = make(chan *github.User)
		chanErrs  = make(chan error, 1)
	)

	go func() {
		var wg sync.WaitGroup
		defer func() {
			wg.Wait()
			close(chanUsers)
			close(chanErrs)
		}()

		if config.noFetchUsers {
			matches, err := filepath.Glob(
				path.Join(config.outputDir, "*"))
			if err != nil {
				chanErrs <- err
				return
			}
			wg.Add(len(matches))
			for _, m := range matches {
				go func(m string) {
					defer wg.Done()

					login := path.Base(m)

					// If resume mode is enabled then only process
					// the member if their login name is >= the
					// first command-line argument
					if config.resume && login < flag.Arg(0) {
						return
					}

					user, err := loadUserFromDisk(login)
					if err != nil {
						chanErrs <- err
						return
					}
					chanUsers <- user
				}(m)
			}
			return
		}

		// Get all available pages of member data as long as the context
		// is not cancelled and there are additional pages to retrieve.
		opts := &github.ListMembersOptions{
			ListOptions: github.ListOptions{Page: 1},
		}
		for ctx.Err() == nil && opts.Page > 0 {
			members, rep, err := client.Organizations.ListMembers(
				ctx,
				config.memberOrg,
				opts)
			if err != nil {
				chanErrs <- err
				return
			}

			printRateLimit(rep.Rate)

			// Add the users to the wait group.
			wg.Add(len(members))

			// Start a goroutine to process the members in this page.
			go func(members []*github.User) {
				for _, m := range members {
					// Start a goroutine to process this member.
					go func(login string) {
						defer wg.Done()

						// If the context has been cancelled then do
						// not process this user. This is handled here
						// so that the above wg.Done() call gets
						// invoked and the program doesn't deadlock.
						if ctx.Err() != nil {
							return
						}

						// If resume mode is enabled then only process
						// the member if their login name is >= the
						// first command-line argument
						if config.resume && login < flag.Arg(0) {
							return
						}

						// Get the user details.
						user, err := getUser(ctx, client, login)
						if err != nil {
							chanErrs <- err
							return
						}

						chanUsers <- user
					}(m.GetLogin())
				}
			}(members)

			opts.Page = rep.NextPage
		}
	}()

	return chanUsers, chanErrs
}

func loadUserFromDisk(login string) (*github.User, error) {
	filePath := path.Join(config.outputDir, login, "data.json")
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var user github.User
	if err := dec.Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

func printRateLimit(rate github.Rate) {
	if config.showRateLimit {
		fmt.Fprintln(os.Stderr, formatRateReset(rate))
	}
}

func getUser(
	ctx context.Context,
	client *github.Client,
	login string) (*github.User, error) {

	user, rep, err := client.Users.Get(ctx, login)
	if err != nil {
		return nil, err
	}
	printRateLimit(rep.Rate)
	return user, nil
}

func writeUser(user *github.User) error {
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

func unique(src []string) []string {
	if len(src) == 0 {
		return nil
	}
	uniq := map[string]struct{}{}
	for _, s := range src {
		if _, ok := uniq[s]; !ok {
			uniq[s] = struct{}{}
		}
	}
	dst := make([]string, len(uniq))
	i := 0
	for s := range uniq {
		dst[i] = s
		i++
	}
	return dst
}

func fileExists(filePath string) (bool, error) {
	_, err := os.Stat(filePath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
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

func getGoPath() string {
	if goPath := os.Getenv("GOPATH"); goPath != "" {
		return strings.Split(goPath, ":")[0]
	}
	if goPath := path.Join(os.Getenv("HOME"), "go"); goPath != "" {
		return goPath
	}
	if goPath := path.Join(os.Getenv("USERPROFILE"), "go"); goPath != "" {
		return goPath
	}
	return ""
}

func writeGitLogForAuthor(
	ctx context.Context,
	user *github.User) error {

	login := user.GetLogin()

	// Ensure the directory for the commits exists.
	dirPath := path.Join(config.outputDir, login, "commits")
	os.MkdirAll(dirPath, 0755)

	// Remove the commits directory for the user if the user has
	// no commits
	defer func() {
		matches, _ := filepath.Glob(path.Join(dirPath, "*"))
		if len(matches) == 0 {
			os.RemoveAll(dirPath)
		}
	}()

	commitIDsWritten := map[string]struct{}{}

	processCommitIDs := func(commitByType string, commitIDs []string) error {

		// Ensure commits are written to different sub-directories
		// that reflect the type of information used to query the commit,
		// ex: login, email, name
		commitByTypeDirPath := path.Join(dirPath, commitByType)
		os.MkdirAll(commitByTypeDirPath, 0755)

		// Remove the commits directory for the user if the user has
		// no commits
		defer func() {
			matches, _ := filepath.Glob(path.Join(commitByTypeDirPath, "*"))
			if len(matches) == 0 {
				os.RemoveAll(commitByTypeDirPath)
			}
		}()

		for _, commitID := range commitIDs {

			// Do not write the commit to disk twice.
			if _, ok := commitIDsWritten[commitID]; ok {
				continue
			}

			filePath := path.Join(commitByTypeDirPath, commitID)

			if err := func() error {
				// Get all of the commit information.
				r, done, wait, err := git("show", commitID)
				if err != nil {
					return err
				}
				defer done()

				f, err := os.Create(filePath)
				if err != nil {
					return err
				}
				defer f.Close()

				// Copy the commit info into the commit file.
				if _, err := io.Copy(f, r); err != nil {
					return err
				}

				return wait()
			}(); err != nil {
				os.RemoveAll(filePath)
				return err
			}

			commitIDsWritten[commitID] = struct{}{}
		}

		return nil
	}

	// Get the commits for the user's login ID.
	{
		commitIDs, err := gitLogForAuthor(ctx, login)
		if err != nil {
			return err
		}
		if err := processCommitIDs("login", commitIDs); err != nil {
			return err
		}
	}

	// If the user has a valid e-mail address then get commits for
	// that too.
	if email := user.GetEmail(); email != "" {
		commitIDs, err := gitLogForAuthor(ctx, email)
		if err != nil {
			return err
		}
		if err := processCommitIDs("email", commitIDs); err != nil {
			return err
		}
	}

	// If the user has a valid display name then get commits for
	// that too.
	if name := user.GetName(); name != "" {
		commitIDs, err := gitLogForAuthor(ctx, name)
		if err != nil {
			return err
		}
		if err := processCommitIDs("name", commitIDs); err != nil {
			return err
		}
	}

	return nil
}

func gitLogForAuthor(
	ctx context.Context,
	author string) ([]string, error) {

	r, done, wait, err := git("log", "--oneline", "--author", author)
	if err != nil {
		return nil, err
	}
	defer done()

	commitIDs := []string{}
	scan := bufio.NewScanner(r)
	for ctx.Err() == nil && scan.Scan() {
		commitIDs = append(commitIDs, strings.Split(scan.Text(), " ")[0])
	}

	return commitIDs, wait()
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

func usage() {
	fmt.Fprintf(
		flag.CommandLine.Output(),
		"usage: %s [FLAGS] [USER...]\n\n",
		os.Args[0])
	fmt.Fprintf(
		flag.CommandLine.Output(),
		"FLAGS\n")
	flag.PrintDefaults()
	fmt.Fprintln(
		flag.CommandLine.Output(), `
ENVIRONMENT VARIABLES
  DEBUG
    Set to a truthy value to enable verbose output

  GITHUB_API_KEY
    A GitHub API key with the following permissions:

      * public_repo
      * read:discussion
      * read:gpg_key
      * read:org
      * read:public_key
      * read:repo_hook
      * read:user
      * repo:invite
      * repo:status
      * repo_deployment
      * user:email

    This environment variable is REQUIRED.`)
}
