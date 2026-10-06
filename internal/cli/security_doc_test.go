package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// SECURITY.md lists, for every promise, the function that enforces it and the
// test that proves it. This test keeps that file honest: renaming or deleting
// either one without updating the document fails the build.
func TestSecurityDocNamesExist(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "SECURITY.md"))
	if err != nil {
		t.Fatal(err)
	}

	testNames := map[string]bool{}
	var code strings.Builder
	err = filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.HasSuffix(path, "_test.go") {
			for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(string(b), -1) {
				testNames[m[1]] = true
			}
		} else {
			code.Write(b)
			code.WriteByte('\n')
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	source := code.String()

	rows, symbols, tests := 0, 0, 0
	for _, line := range strings.Split(string(doc), "\n") {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| ---") || strings.HasPrefix(line, "| Rule") {
			continue
		}
		rows++
		for _, m := range regexp.MustCompile("`(\\w+)\\(\\)`").FindAllStringSubmatch(line, -1) {
			symbols++
			if !regexp.MustCompile(`func (\([^)]*\) )?` + m[1] + `\(`).MatchString(source) {
				t.Errorf("SECURITY.md names the function %s() but it no longer exists: %s", m[1], strings.TrimSpace(line))
			}
		}
		for _, m := range regexp.MustCompile("`(Test\\w+)`").FindAllStringSubmatch(line, -1) {
			tests++
			if !testNames[m[1]] {
				t.Errorf("SECURITY.md names the test %s but it no longer exists: %s", m[1], strings.TrimSpace(line))
			}
		}
	}
	if rows < 20 || symbols < 20 || tests < 25 {
		t.Fatalf("the tables look empty (rows=%d functions=%d tests=%d): is the format still `name()` and `TestName`?", rows, symbols, tests)
	}
}
