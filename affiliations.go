package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	affiliationsFileName = "developers_affiliations.txt"
	affiliationsURL      = "https://raw.githubusercontent.com/cncf/" +
		"gitdm/master/" + affiliationsFileName
)

type devAffiliation struct {
	Occurrences int                 `json:"occurrences"`
	Name        string              `json:"name"`
	Emails      []string            `json:"emails,omitempty"`
	Companies   []affiliatedCompany `json:"companies,omitempty"`
}
type affiliatedCompany struct {
	Name  string     `json:"name"`
	Until *time.Time `json:"until,omitempty"`
}

func (a affiliatedCompany) active() bool {
	return a.Until != nil && a.Until.Before(time.Now())
}

func getDevAffiliations(
	ctx context.Context) (int, map[string]*devAffiliation, error) {

	var r io.Reader

	// Unless disabled, download the latest copy of the developer
	// affiliations file.
	if !config.noFetchAffiliations {
		rep, err := http.Get(affiliationsURL)
		if err != nil {
			return 0, nil, nil
		}
		if rep.StatusCode > 299 {
			return 0, nil, errors.New(rep.Status)
		}
		defer rep.Body.Close()
		r = rep.Body
	}

	fileName := fmt.Sprintf(".%s", affiliationsFileName)
	filePath := path.Join(config.outputDir, fileName)

	// If -no-fetch-affiliations is present then load the affiliations
	// from the local file. Otherwise setup a tee process to both
	// update the local file and scan the downloaded content at the
	// same time.
	if config.noFetchAffiliations {
		f, err := os.Open(filePath)
		if err != nil {
			return 0, nil, err
		}
		defer f.Close()
		r = f
	} else {
		//
		f, err := os.Create(filePath)
		if err != nil {
			return 0, nil, err
		}
		defer f.Close()
		r = io.TeeReader(r, f)
	}

	var (
		n    int
		scan = bufio.NewScanner(r)
		data = map[string]*devAffiliation{}

		devRX = regexp.MustCompile(`^([^:]+):\s*(.+)$`)
		coRX  = regexp.MustCompile(`^\t(.+?)(?:\suntil\s(\d{4}-\d{2}-\d{2}))?$`)
	)

	// Get rid of the leading comment
	if !scan.Scan() {
		return 0, nil, io.EOF
	}
	// Place cursor on first dev affiliate line
	if !scan.Scan() {
		return 0, nil, io.EOF
	}

	// A cancelled context will cause the loop to return early, but not
	// until the current developer affiliate has been processed.
	for ctx.Err() == nil {
		devLine := scan.Text()
		devMatch := devRX.FindStringSubmatch(devLine)
		if len(devMatch) == 0 {
			return 0, nil, fmt.Errorf(
				"error matching affiliate+dev: %s", devLine)
		}

		var dev *devAffiliation
		if d, ok := data[devMatch[1]]; ok {
			dev = d
			dev.Occurrences++
		} else {
			n++
			dev = &devAffiliation{Occurrences: 1, Name: devMatch[1]}
			data[dev.Name] = dev
		}

		if len(devMatch) > 2 {
			emails := strings.Split(devMatch[2], ",")
			for _, v := range emails {
				dev.Emails = append(dev.Emails, strings.TrimSpace(
					strings.Replace(v, "!", "@", -1)))
			}
		}

		// Add the dev to the data map all of their available e-mail addresses.
		for i := range dev.Emails {
			data[dev.Emails[i]] = dev
		}

		for {
			// Place the cursor on the first company line for the developer.
			// If there is no more data then return the data map.
			if !scan.Scan() {
				return n, data, nil
			}
			coLine := scan.Text()
			coMatch := coRX.FindStringSubmatch(coLine)

			// When the line no longer matches the company regex then
			// that means a new developer has been encountered. Break out
			// of this loop so that the outer loop may continue.
			if len(coMatch) == 0 {
				break
			}

			// Create a new affiliated company for the dev.
			co := affiliatedCompany{Name: coMatch[1]}

			// Check to see if the company has an expiry date.
			if len(coMatch) > 2 && coMatch[2] != "" {
				t, err := time.Parse("2006-01-02", coMatch[2])
				if err != nil {
					return 0, nil, err
				}
				if !t.IsZero() {
					co.Until = &t
				}
			}

			// Append the affiliated company to the developer's
			// Companies slice.
			dev.Companies = append(dev.Companies, co)
		}
	}

	return n, data, nil
}

func supplementWithAffiliates(
	user *userWrapper,
	knownEmails map[string]struct{},
	affiliates map[string]*devAffiliation) {

	login := user.GetLogin()

	// Add all of the e-mail addresses from the affiliate looked up
	// by the user's name as long as the name is sufficiently unique.
	if name := user.GetName(); len(name) >= 8 || strings.Contains(name, " ") {
		if a, ok := affiliates[name]; ok && a.Occurrences == 1 {
			for _, v := range a.Emails {
				if _, ok := knownEmails[v]; !ok {
					knownEmails[v] = struct{}{}
					user.Emails = append(user.Emails, v)
					if debug {
						log.Printf(
							"affiliate: login=%s, name=%s mail=%s",
							login, name, v)
					}
				}
			}
		}
	}

	// Add all of the e-mail addresses from the affiliate looked up
	// by the user's primary e-mail address.
	if primryEmail := user.GetEmail(); primryEmail != "" {
		if a, ok := affiliates[primryEmail]; ok && a.Occurrences == 1 {
			for _, v := range a.Emails {
				if _, ok := knownEmails[v]; !ok {
					knownEmails[v] = struct{}{}
					user.Emails = append(user.Emails, v)
					if debug {
						log.Printf(
							"affiliate: login=%s, primryEmail=%s mail=%s",
							login, primryEmail, v)
					}
				}
			}
		}
	}
}
