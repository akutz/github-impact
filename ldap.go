package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"gopkg.in/ldap.v2"
)

func ldapBind(
	ctx context.Context,
	user, pass string,
	opts options) (ldap.Client, error) {

	client, err := ldap.DialTLS(
		"tcp",
		opts.config.LDAP.Host,
		&tls.Config{
			ServerName:         strings.Split(opts.config.LDAP.Host, ":")[0],
			InsecureSkipVerify: opts.config.LDAP.TLS.Insecure,
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

func (m *member) loadFromLDAP(ctx context.Context, opts options) error {
	var filter string
	if m.LDAPLogin == "" {
		filter = fmt.Sprintf(`(&(objectClass=person)(displayName=%s))`, m.Name)
	} else {
		filter = fmt.Sprintf(`(sAMAccountName=%s)`, m.LDAPLogin)
	}

	req := &ldap.SearchRequest{
		BaseDN: "DC=vmware,DC=com",
		Attributes: []string{
			"mail",
			"sAMAccountName",
			"distinguishedName",
			"whenCreated",
			"whenChanged",
		},
		Scope:  ldap.ScopeWholeSubtree,
		Filter: filter,
	}
	if opts.config.Debug {
		log.Printf("%+v", req)
	}

	rep, err := opts.ldap.Search(req)
	if err != nil {
		return err
	}
	if len(rep.Entries) != 1 {
		patt := fmt.Sprintf(`(?i)^.+@.*%s.*\..+$`, opts.config.MemberOrg)
		for _, email := range m.Emails {
			if ok, err := regexp.MatchString(patt, email); !ok {
				if err != nil {
					return err
				}
				continue
			}
			req.Filter = fmt.Sprintf(`(mail=%s)`, email)
			if rep, err = opts.ldap.Search(req); err != nil {
				return err
			}
			break
		}
	}

	if len(rep.Entries) != 1 {
		return nil
	}

	entry := rep.Entries[0]
	if opts.config.Debug {
		log.Printf("%+v", entry)
	}

	m.LDAPLogin = entry.GetAttributeValue("sAMAccountName")
	m.Emails.append(entry.GetAttributeValue("mail"))

	var employed dateRange
	if v := entry.GetAttributeValue("whenCreated"); v != "" {
		t, err := time.Parse("20060102150405.0Z", v)
		if err != nil {
			return err
		}
		employed.From = &t
	}
	dn := entry.GetAttributeValue("distinguishedName")
	if strings.Contains(dn, "OU=Closed_Hold") {
		if v := entry.GetAttributeValue("whenChanged"); v != "" {
			t, err := time.Parse("20060102150405.0Z", v)
			if err != nil {
				return err
			}
			employed.Until = &t
		}
	}
	m.Employed.append(employed)

	return nil
}
