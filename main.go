package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
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
	utc           bool
	offline       bool
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
		&config.noFetchIssues, "no-fetch-issues", false,
		"Do not update local issue cache")
	flag.BoolVar(
		&config.showRateLimit, "show-rate-limit", debug,
		"Shows the rate limit info after all API calls")
	flag.BoolVar(
		&config.utc, "utc", false,
		"Print timestamps using UTC")
	flag.BoolVar(
		&config.offline, "offline", false,
		"Offline mode sets all -no-fetch flags to true")

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

	config.args = flag.Args()
	if !config.resume {
		// If resume is disabled then remove duplicate args
		config.args = unique(flag.Args())
	} else if flag.NArg() != 1 {
		// If resume is enabled and there is not exactly one argument
		// then print an error
		fmt.Fprintln(
			os.Stderr,
			"The flag -resume must be used with a single username")
		flag.Usage()
		os.Exit(1)
	}

	if config.offline {
		config.noFetchUsers = true
		config.noFetchIssues = true
	}

	if debug {
		log.Printf("%+v", config)
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

	// Start updating the local cache.
	chanEntries, chanErrs := updateLocalCache(ctx, client)

	// Begin generating the reports with the local cache channels.
	if err := generateReport(ctx, chanEntries, chanErrs); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printRateLimit(rate github.Rate) {
	if config.showRateLimit {
		fmt.Fprintln(os.Stderr, formatRateReset(rate))
	}
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

func getLoginDirNames() ([]string, error) {
	matches, err := filepath.Glob(path.Join(config.outputDir, "*", "data.json"))
	if err != nil {
		return nil, err
	}
	names := make([]string, len(matches))
	for i := 0; i < len(matches); i++ {
		names[i] = path.Base(path.Dir(matches[i]))
	}
	return names, nil
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
