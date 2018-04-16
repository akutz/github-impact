package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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
	return a.Until == nil || a.Until.Before(time.Now())
}

type devAffiliates map[string]*devAffiliation

// decode decodes the provided IO stream into the devAffiliates object
// and returns the number of unique values.
func (da devAffiliates) decode(ctx context.Context, r io.Reader) (int, error) {
	var (
		n    int
		scan = bufio.NewScanner(r)

		devRX = regexp.MustCompile(`^([^:]+):\s*(.+)$`)
		coRX  = regexp.MustCompile(`^\t(.+?)(?:\suntil\s(\d{4}-\d{2}-\d{2}))?$`)
	)

	// Get rid of the leading comment
	if !scan.Scan() {
		return 0, io.EOF
	}
	// Place cursor on first dev affiliate line
	if !scan.Scan() {
		return 0, io.EOF
	}

	// A cancelled context will cause the loop to return early, but not
	// until the current developer affiliate has been processed.
	for ctx.Err() == nil {
		devLine := scan.Text()
		devMatch := devRX.FindStringSubmatch(devLine)
		if len(devMatch) == 0 {
			return 0, fmt.Errorf(
				"error matching affiliate+dev: %s", devLine)
		}

		var dev *devAffiliation
		if d, ok := da[devMatch[1]]; ok {
			dev = d
			dev.Occurrences++
		} else {
			n++
			dev = &devAffiliation{Occurrences: 1, Name: devMatch[1]}
			da[dev.Name] = dev
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
			da[dev.Emails[i]] = dev
		}

		for {
			// Place the cursor on the first company line for the developer.
			// If there is no more data then return the data map.
			if !scan.Scan() {
				return n, nil
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
					return 0, err
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

	return n, nil
}

func getDevAffiliates(
	ctx context.Context, opts options) (int, devAffiliates, error) {

	var r io.Reader

	// Unless disabled, download the latest copy of the developer
	// affiliations file.
	if !opts.config.NoAffiliates {
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
	filePath := path.Join(opts.config.OutputDir, fileName)

	// If -no-fetch-affiliations is present then load the affiliations
	// from the local file. Otherwise setup a tee process to both
	// update the local file and scan the downloaded content at the
	// same time.
	if opts.config.NoAffiliates {
		// It's okay if the affiliates file does not exist. In that
		// case just return an empty map.
		if ok, err := fileExists(filePath); !ok {
			if err != nil {
				return 0, nil, err
			}
			return 0, nil, nil
		}
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

	data := devAffiliates{}
	n, err := data.decode(ctx, r)
	if err != nil {
		return n, nil, err
	}
	return n, data, nil
}

func (m *member) loadFromAffiliates(
	ctx context.Context, opts options) error {

	for _, a := range opts.devs {
		if a.Name == m.Name && a.Occurrences == 1 {
			for _, email := range a.Emails {
				m.Emails.append(email)
			}
			return nil
		}
	}
	return nil
}
