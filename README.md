# GitHub-Impact
Discovers the impact of one or more users on a GitHub project.

## Install
```shell
go get -u github.com/akutz/github-impact
```

## Requirements

### The `git` command
In order to provide information about a user's commit impact, the
`git` command must be installed. If the `git` command is not detected
in the path then the `-target-git-dir` flag is not available.

### A GitHub API Key
The `github-impact` command requires that the environment variable
`GITHUB_API_KEY` be set to a GitHub API key with the following
permissions:

* `public_repo`
* `read:discussion`
* `read:gpg_key`
* `read:org`
* `read:public_key`
* `read:repo_hook`
* `read:user`
* `repo:invite`
* `repo:status`
* `repo_deployment`
* `user:email`

### LDAP credentials
LDAP may be used to supplement e-mail addresses for GitHub members who
elect to not share their e-mail address. The flag `-ldap` must be used
to enable LDAP lookup (as opposed to `-no-fetch` for the other features
that require remote access). Additionally, credentials for LDAP are
specified with the environment variables `LDAP_USER` and `LDAP_PASS`.

Please note that when accessing VMware's Active Directory the username
is `YOUR_USER_NAME@vmware.com`. Additionally, access to VMware's
Active Directory requires the VPN.

## All Users
```shell
$ GITHUB_API_KEY=ABC123 github-impact
```

## Single User
```shell
$ GITHUB_API_KEY=ABC123 github-impact akutz
```

## Multiple Users
```shell
$ GITHUB_API_KEY=ABC123 github-impact akutz clintkitson
```

## Resume from User
Resuming from a single user causes the program to ignore all discovered
usernames that are less than (using default string comparison) the
username specified on the command line:

```shell
$ GITHUB_API_KEY=ABC123 github-impact -resume zjs
```
