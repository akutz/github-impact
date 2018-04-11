package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"strings"

	"gopkg.in/ldap.v2"
)

func ldapBind(ctx context.Context, user, pass string) (ldap.Client, error) {
	client, err := ldap.DialTLS("tcp", config.ldapHost, &tls.Config{
		ServerName:         strings.Split(config.ldapHost, ":")[0],
		InsecureSkipVerify: config.ldapInsecureTLS,
	})
	if err != nil {
		return nil, err
	}
	if err := client.Bind(user, pass); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func supplementWithLDAP(
	ctx context.Context,
	client ldap.Client,
	user *userWrapper,
	knownEmails map[string]struct{}) error {

	if !config.ldap {
		return nil
	}

	login := user.GetLogin()
	name := user.GetName()

	req := &ldap.SearchRequest{
		BaseDN:     "DC=vmware,DC=com",
		Attributes: []string{"mail"},
		Scope:      ldap.ScopeWholeSubtree,
	}

	// Search by login ID
	req.Filter = fmt.Sprintf(
		`(&(objectClass=person)(samAccountName=%s))`, login)
	rep, err := client.Search(req)
	if err != nil {
		return err
	}
	if len(rep.Entries) == 1 {
		if v := rep.Entries[0].GetAttributeValue("mail"); v != "" {
			if _, ok := knownEmails[v]; !ok {
				knownEmails[v] = struct{}{}
				user.Emails = append(user.Emails, v)
				if debug {
					log.Printf(
						"ldap: login=%[1]s, sAMAccountName=%[1]s mail=%[2]s",
						login, v)
				}
			}
			return nil
		}
	}

	if name == "" {
		return nil
	}

	// Search by display name
	req.Filter = fmt.Sprintf(
		`(&(objectClass=person)(displayName=%s))`, name)
	rep, err = client.Search(req)
	if err != nil {
		return err
	}
	if len(rep.Entries) == 1 {
		if v := rep.Entries[0].GetAttributeValue("mail"); v != "" {
			if _, ok := knownEmails[v]; !ok {
				knownEmails[v] = struct{}{}
				user.Emails = append(user.Emails, v)
				if debug {
					log.Printf(
						"ldap: login=%s, displayName=%s mail=%s",
						login, name, v)
				}
			}
			return nil
		}
	}

	return nil
}
