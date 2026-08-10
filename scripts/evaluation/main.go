// Command evaluation runs one deliberately narrow, opt-in product-path
// evaluation. The subject is always an external built YHC executable;
// this command does not import or construct the production engine.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const runnerVersion = "p43.0-v1"

type harnessError struct {
	code  string
	cause error
}

func (err *harnessError) Error() string {
	if err.cause == nil {
		return err.code
	}
	return err.code + ": " + err.cause.Error()
}

func (err *harnessError) Unwrap() error { return err.cause }

func fail(code string, cause error) error {
	return &harnessError{code: code, cause: cause}
}

func errorCode(err error) string {
	var harnessErr *harnessError
	if errors.As(err, &harnessErr) {
		return harnessErr.code
	}
	return "harness_internal"
}

func main() {
	var binary, scenario, reportPath string
	flag.StringVar(&binary, "binary", "", "absolute path to a built yhc executable")
	flag.StringVar(&scenario, "scenario", supportedScenario, "versioned evaluation scenario")
	flag.StringVar(&reportPath, "report", "", "new report path under an existing private directory")
	flag.Parse()
	if binary == "" || reportPath == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "evaluation_failed code=usage_invalid")
		os.Exit(2)
	}
	if err := evaluate(context.Background(), binary, scenario, reportPath, defaultDependencies()); err != nil {
		fmt.Fprintf(os.Stderr, "evaluation_failed code=%s\n", errorCode(err))
		os.Exit(1)
	}
}

func commandRoot() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fail("scenario_source_unavailable", nil)
	}
	root := filepath.Dir(filename)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fail("scenario_source_unavailable", err)
	}
	return root, nil
}
