package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
	ldap "gopkg.in/ldap.v2"
)

const (
	exitCodeContext     = 2 + iota
	exitCodeLDAPBind    // 3
	exitCodePrintConfig // 4
	exitCodeGitDir      // 5
	exitCodeAffiliates  // 6
	exitCodeWriteReport // 7
)

type options struct {
	config config
	github *github.Client
	ldap   ldap.Client
	devs   devAffiliates

	// chanAPI controls the number of concurrent API calls
	chanAPI chan struct{}

	// chanGit controls the number of concurrent git commands
	chanGit chan struct{}
}

type config struct {
	Debug        bool         `json:"debug"`
	Args         []string     `json:"args"`
	OutputDir    string       `json:"output-dir"`
	MemberOrg    string       `json:"member-org"`
	TargetOrg    string       `json:"target-org"`
	TargetRepo   string       `json:"target-repo"`
	Resume       bool         `json:"resume"`
	NoAffiliates bool         `json:"no-fetch-affiliates"`
	UTC          bool         `json:"utc"`
	Offline      bool         `json:"offline"`
	Git          gitConfig    `json:"git"`
	GitHub       gitHubConfig `json:"gitHub"`
	LDAP         ldapConfig   `json:"ldap"`
}

type gitHubConfig struct {
	API            githubAPIConfig `json:"api"`
	NoUsers        bool            `json:"no-fetch-users"`
	NoIssues       bool            `json:"no-fetch-issues"`
	NoPullRequests bool            `json:"no-fetch-pull-requests"`
}

type githubAPIConfig struct {
	Max           int           `json:"api-max"`
	Retries       int           `json:"api-retries"`
	Wait          time.Duration `json:"api-wait"`
	RetryWait     time.Duration `json:"api-retry-wait"`
	ShowRateLimit bool          `json:"show-rate-limit"`
}

type gitConfig struct {
	Max       int    `json:"git-max"`
	Disabled  bool   `json:"no-git"`
	TargetDir string `json:"target-git-dir"`
}

type ldapConfig struct {
	Disabled bool          `json:"no-ldap"`
	Host     string        `json:"ldap-host"`
	TLS      ldapTLSConfig `json:"tls"`
}

type ldapTLSConfig struct {
	Insecure bool `json:"ldap-tls-insecure"`
}

func main() {
	flag.Usage = usage

	// Set up an options object to send into the functions.
	var opts options

	opts.config.Debug, _ = strconv.ParseBool(os.Getenv("DEBUG"))

	// Set up the flags
	flag.StringVar(
		&opts.config.OutputDir, "output", "data",
		"The output directory")
	flag.StringVar(
		&opts.config.MemberOrg, "member-org", "VMware",
		"The source GitHub org")
	flag.StringVar(
		&opts.config.TargetOrg, "target-org", "kubernetes",
		"The targeted GitHub org")
	flag.StringVar(
		&opts.config.TargetRepo, "target-repo", "kubernetes",
		"The targeted GitHub repo")
	flag.BoolVar(
		&opts.config.Resume, "resume", false,
		"Resume at the specified member name. An errors occurs if "+
			"more than one username is specified.")
	flag.BoolVar(
		&opts.config.UTC, "utc", false,
		"Print timestamps using UTC")
	flag.BoolVar(
		&opts.config.Offline, "offline", false,
		"Offline mode sets all -no-fetch flags to true")
	flag.BoolVar(
		&opts.config.Offline, "no-fetch", false,
		"Synonym for -offline")
	flag.BoolVar(
		&opts.config.Offline, "report-only", false,
		"Synonym for -offline")
	flag.BoolVar(
		&opts.config.NoAffiliates, "no-fetch-affiliates", false,
		"Do not update the local developer affiliations file "+
			"(https://goo.gl/ux4PVs)")

	flag.StringVar(
		&opts.config.LDAP.Host, "ldap-host", "SCROOTDC01.vmware.com:3269",
		"The LDAP host used to supplement e-mail addresses")
	flag.BoolVar(
		&opts.config.LDAP.Disabled, "no-ldap", false,
		"Disable LDAP lookups")
	flag.BoolVar(
		&opts.config.LDAP.TLS.Insecure, "ldap-tls-insecure", false,
		"Enable LDAP TLS insecure mode")

	flag.BoolVar(
		&opts.config.GitHub.NoUsers, "no-fetch-users", false,
		"Do not update local user cache")
	flag.BoolVar(
		&opts.config.GitHub.NoIssues, "no-fetch-issues", false,
		"Do not update local issue cache")
	flag.BoolVar(
		&opts.config.GitHub.NoPullRequests, "no-fetch-pull-requests", false,
		"Do not update local pull request cache")
	flag.IntVar(
		&opts.config.GitHub.API.Max, "api-max", 2,
		"Number of max concurrent API calls")
	flag.IntVar(
		&opts.config.GitHub.API.Retries, "api-retries", 5,
		"Number of retries for a failed API call")
	var apiWait string
	flag.StringVar(
		&apiWait, "api-wait", "1s",
		"Duration of time to wait between API calls")
	var apiRetryWait string
	flag.StringVar(
		&apiRetryWait, "api-retry-wait", "10s",
		"Duration of time to wait between failed API calls when the "+
			"response header \"Retry-After\" is missing")
	flag.BoolVar(
		&opts.config.GitHub.API.ShowRateLimit, "show-rate-limit",
		opts.config.Debug,
		"Shows the rate limit info after all API calls")

	// Check to see if the git command is in the path.
	if exec.Command("git", "version").Run() == nil {
		var defaultTargetGitDir string
		if goPath := getGoPath(); goPath != "" {
			gitDir := path.Join(
				goPath,
				"src",
				"github.com",
				opts.config.TargetOrg,
				opts.config.TargetRepo,
				".git")
			if ok, err := fileExists(gitDir); !ok {
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(exitCodeGitDir)
				}
			} else {
				defaultTargetGitDir = gitDir
			}
		}
		flag.StringVar(
			&opts.config.Git.TargetDir, "target-git-dir", defaultTargetGitDir,
			"The path to the git directory to search for commit activity")
		flag.BoolVar(
			&opts.config.Git.Disabled, "no-git", false,
			"Do not write git commit activity")
		flag.IntVar(
			&opts.config.Git.Max, "git-max", 10,
			"Number of max concurrent git commands")
	} else {
		opts.config.Git.Disabled = true
	}

	// Parse the flags
	flag.Parse()

	// Create the program's context
	ctx := context.Background()

	if opts.config.Offline {
		opts.config.LDAP.Disabled = true
		opts.config.GitHub.NoUsers = true
		opts.config.GitHub.NoIssues = true
		opts.config.GitHub.NoPullRequests = true
	}

	// Parse the GitHub API retry config if the GitHub API is used.
	if !opts.config.GitHub.NoUsers &&
		!opts.config.GitHub.NoIssues &&
		!opts.config.GitHub.NoPullRequests {

		// Parse the amount of time to wait between API calls.
		if d, err := time.ParseDuration(apiWait); err != nil {
			opts.config.GitHub.API.Wait = time.Duration(1) * time.Second
		} else {
			opts.config.GitHub.API.Wait = d
		}

		// Parse the amount of time to wait between failed API calls.
		if d, err := time.ParseDuration(apiRetryWait); err != nil {
			opts.config.GitHub.API.RetryWait = time.Duration(10) * time.Second
		} else {
			opts.config.GitHub.API.RetryWait = d
		}
	}

	opts.config.Args = flag.Args()
	if !opts.config.Resume {
		// If resume is disabled then remove duplicate args
		opts.config.Args = unique(flag.Args())
	} else if flag.NArg() != 1 {
		// If resume is enabled and there is not exactly one argument
		// then print an error
		fmt.Fprintln(
			os.Stderr,
			"The flag -resume must be used with a single username")
		flag.Usage()
		os.Exit(1)
	}

	if opts.config.Debug {
		enc := json.NewEncoder(os.Stderr)
		enc.SetIndent("", "  ")
		if err := enc.Encode(opts.config); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitCodePrintConfig)
		}
	}

	if !opts.config.Git.Disabled {
		// chanGit controls the number of concurrent git commands
		opts.chanGit = make(chan struct{}, opts.config.Git.Max)
	}

	// Create the github API client if any of the features
	// that use it are enabled.
	if !opts.config.GitHub.NoUsers &&
		!opts.config.GitHub.NoIssues &&
		!opts.config.GitHub.NoPullRequests {

		// Parse the GitHub API key.
		apiKey := os.Getenv("GITHUB_API_KEY")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "GITHUB_API_KEY required")
			os.Exit(1)
		}

		// Create the GitHub client.
		opts.github = newGitHubAPIClient(ctx, apiKey)

		// chanAPI controls the number of concurrent API calls
		opts.chanAPI = make(chan struct{}, opts.config.GitHub.API.Max)
	}

	// Create the ldap client.
	if !opts.config.LDAP.Disabled {
		ldapUser := os.Getenv("LDAP_USER")
		ldapPass := os.Getenv("LDAP_PASS")
		if ldapUser == "" || ldapPass == "" {
			fmt.Fprintln(os.Stderr, "LDAP_USER & LDAP_PASS required")
			os.Exit(1)
		}
		client, err := ldapBind(ctx, ldapUser, ldapPass, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitCodeLDAPBind)
		}
		defer client.Close()
		opts.ldap = client
	}

	// Ensure the outut directory exists
	os.MkdirAll(opts.config.OutputDir, 0755)

	// Parse the developer affiliates file.
	if !opts.config.NoAffiliates {
		_, devs, err := getDevAffiliates(ctx, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitCodeAffiliates)
		}
		opts.devs = devs
	}

	// Get all of the members of the GitHub org.
	chanMembers, chanErrs := getMembers(ctx, opts)

	go func() {
		for {
			select {
			case <-ctx.Done():
				fmt.Fprintln(os.Stderr, ctx.Err())
				os.Exit(exitCodeContext)
			case err, ok := <-chanErrs:
				if ok {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
				return
			}
		}
	}()

	if err := writeReport(ctx, chanMembers, opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeWriteReport)
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
