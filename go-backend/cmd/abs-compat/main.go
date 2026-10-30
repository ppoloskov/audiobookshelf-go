// abs-compat replays an explicit list of read-only API cases against two servers.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"audiobookshelf-compat/internal/compat"
)

type input struct {
	Cases []compat.Case `json:"cases"`
}

func main() { os.Exit(run()) }

func run() int {
	leftText := flag.String("left", "", "reference server base URL")
	rightText := flag.String("right", "", "rewrite server base URL")
	caseFile := flag.String("cases", "", "JSON file containing an explicit cases array")
	leftEnv := flag.String("left-token-env", "ABS_COMPAT_LEFT_TOKEN", "environment variable name holding the reference bearer token")
	rightEnv := flag.String("right-token-env", "ABS_COMPAT_RIGHT_TOKEN", "environment variable name holding the rewrite bearer token")
	timeout := flag.Duration("timeout", 10*time.Second, "per-request timeout")
	maxBody := flag.Int64("max-body", compat.DefaultBodyLimit, "maximum response body bytes per server")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: abs-compat -left URL -right URL -cases FILE [options]\n\n")
		fmt.Fprintln(flag.CommandLine.Output(), "Replays only explicit GET/HEAD cases. Tokens are read from the named environment variables.")
		flag.PrintDefaults()
		fmt.Fprintln(flag.CommandLine.Output(), `Case file format: {"cases":[{"name":"health","method":"GET","path":"/healthcheck","compare_headers":["content-type"],"ignore_json_pointers":["/generatedAt"]}]}`)
	}
	flag.Parse()
	if *leftText == "" || *rightText == "" || *caseFile == "" {
		flag.Usage()
		return 2
	}
	left, err := parseBase(*leftText)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid -left:", err)
		return 2
	}
	right, err := parseBase(*rightText)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid -right:", err)
		return 2
	}
	data, err := os.ReadFile(*caseFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read case file:", err)
		return 2
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var in input
	if err = dec.Decode(&in); err != nil {
		fmt.Fprintln(os.Stderr, "invalid case file:", err)
		return 2
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		fmt.Fprintln(os.Stderr, "invalid case file: trailing data")
		return 2
	}
	if len(in.Cases) == 0 {
		fmt.Fprintln(os.Stderr, "invalid case file: cases must contain at least one case")
		return 2
	}
	lt, rt := os.Getenv(*leftEnv), os.Getenv(*rightEnv)
	if *leftEnv == *rightEnv {
		fmt.Fprintln(os.Stderr, "left and right token environment variables must be different")
		return 2
	}
	if *maxBody <= 0 || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "-max-body must be positive and -timeout must be positive")
		return 2
	}
	opts := compat.Options{LeftURL: left, RightURL: right, LeftToken: lt, RightToken: rt, Timeout: *timeout, BodyLimit: *maxBody}
	failed := 0
	for i, c := range in.Cases {
		if c.Name == "" {
			c.Name = fmt.Sprintf("case-%d", i+1)
		}
		res, err := compat.Run(context.Background(), c, opts)
		if err != nil {
			fmt.Printf("FAIL %s: %s\n", c.Name, err)
			failed++
			continue
		}
		if res.Equal {
			fmt.Printf("PASS %s\n", res.Name)
		} else {
			fmt.Printf("DIFF %s: %s\n", res.Name, res.Detail)
			failed++
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "%d of %d cases failed or differed\n", failed, len(in.Cases))
		return 1
	}
	fmt.Printf("All %d cases matched\n", len(in.Cases))
	return 0
}

func parseBase(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("expected an http(s) origin URL without userinfo, path, query, or fragment")
	}
	return u, nil
}
