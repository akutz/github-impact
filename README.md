# GitHub-Impact
Discovers the impact of one or more users on a GitHub project.

## Install
```shell
go get -u github.com/akutz/github-impact
```

## Requirements
In order to provide information about a user's commit impact, the
`git` command must be installed. If the `git` command is not detected
in the path then the `-target-git-dir` flag is not available.

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
