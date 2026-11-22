package main

import (
	"fmt"
	"io"

	"github.com/wamra/specs-index/internal/api"
)

func cmdConfig(args []string, out, errw io.Writer) int {
	var c common
	fs := newFlags("config", errw, &c)
	pos, code := parseInterspersed(fs, args)
	if code >= 0 {
		return code
	}
	switch {
	case len(pos) == 1 && pos[0] == "init":
		r, err := api.ConfigInit()
		if err != nil {
			return fail(errw, fmt.Errorf("config init: %w", err))
		}
		if c.json {
			return printJSON(out, r)
		}
		fmt.Fprintf(out, "wrote %s\n", r.File)
		return 0
	case len(pos) != 0:
		return usageErr(errw, "config: usage: config [init]")
	}
	cfg, err := loadConfig()
	if err != nil {
		return fail(errw, err)
	}
	r, err := api.Config(cfg)
	if err != nil {
		return fail(errw, err)
	}
	if c.json {
		return printJSON(out, r)
	}
	exists := "not found, using defaults"
	if r.FileExists {
		exists = "found"
	}
	fmt.Fprintf(out, "config file: %s (%s)\n", r.File, exists)
	fmt.Fprintf(out, "provider:    %s\n", r.Provider)
	fmt.Fprintf(out, "model:       %s\n", r.Model)
	fmt.Fprintf(out, "base url:    %s\n", r.BaseURL)
	fmt.Fprintf(out, "allow cloud: %t\n", r.AllowCloud)
	if r.Dims != 0 {
		fmt.Fprintf(out, "dims:        %d\n", r.Dims)
	}
	switch {
	case r.APIKeyEnv == "":
		fmt.Fprintln(out, "api key:     not configured")
	case r.APIKeySet:
		fmt.Fprintf(out, "api key:     set (from the variable named %s)\n", r.APIKeyEnv)
	default:
		fmt.Fprintf(out, "api key:     not set (the variable named %s is empty)\n", r.APIKeyEnv)
	}
	if r.Region != "" {
		fmt.Fprintf(out, "region:      %s\n", r.Region)
	}
	if r.Profile != "" {
		fmt.Fprintf(out, "profile:     %s\n", r.Profile)
	}
	fmt.Fprintln(out, "environment variables:")
	for _, v := range r.Environment {
		cur := "not set"
		if v.Set {
			cur = "set to " + v.Value
		}
		fmt.Fprintf(out, "  %s (%s)  %s\n", v.Name, cur, v.Description)
	}
	return 0
}
