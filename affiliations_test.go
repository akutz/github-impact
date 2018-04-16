package main

import (
	"context"
	"testing"
)

func TestGetDevelopersAffiliations(t *testing.T) {

	opts := options{}

	n, data, err := getDevAffiliates(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("data is empty")
	}
	t.Logf("affiliate count=%d", n)
	t.Logf("len(data)=%d", len(data))

	t.Logf("by zuul:\n%+v", data["zuul"])

	if v, ok := data["Vladimir Vivien"]; !ok {
		t.Fatal("data[name] is empty")
	} else {
		t.Logf("by name:\n%+v", v)
	}

	if v, ok := data["vladimir.vivien@gmail.com"]; !ok {
		t.Fatal("data[email1] is empty")
	} else {
		t.Logf("by email1:\n%+v", v)
	}

	if v, ok := data["vladimirvivien@users.noreply.github.com"]; !ok {
		t.Fatal("data[email2] is empty")
	} else {
		t.Logf("by email2:\n%+v", v)
	}

	if v, ok := data["Øyvind Ingebrigtsen Øvergaard"]; !ok {
		t.Fatal("data[until] is empty")
	} else if u := v.Companies[1].Until; u.IsZero() {
		t.Fatal("data[until].Companies[1].Until.IsZero")
	} else {
		t.Logf("until:\n%+v", v)
	}

	t.Log("get affiliates again - no fetch")

	opts.config.NoAffiliates = true
	n, data, err = getDevAffiliates(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("data is empty")
	}
	t.Logf("affiliate count=%d", n)
	t.Logf("len(data)=%d", len(data))

	if v, ok := data["Vladimir Vivien"]; !ok {
		t.Fatal("data[name] is empty")
	} else {
		t.Logf("by name:\n%+v", v)
	}
}
