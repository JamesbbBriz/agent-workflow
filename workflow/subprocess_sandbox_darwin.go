//go:build darwin

package workflow

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func sandboxDriver() (contractsv1.ProviderIsolationEvidenceDriver, error) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return "", err
	}
	return contractsv1.ProviderIsolationEvidenceDriverSandboxExec, nil
}

func sandboxCommand(executable string, args []string, root string, maxOutputBytes int) (string, []string, error) {
	sandbox, err := exec.LookPath("sandbox-exec")
	if err != nil {
		return "", nil, err
	}
	rootPattern := regexp.QuoteMeta(strings.TrimPrefix(root, "/"))
	rootPatterns := rootPattern
	if strings.HasPrefix(root, "/private/var/") {
		rootPatterns += "|" + regexp.QuoteMeta(strings.TrimPrefix(root, "/private/"))
	} else if strings.HasPrefix(root, "/var/") {
		rootPatterns += "|" + regexp.QuoteMeta(strings.TrimPrefix("/private"+root, "/"))
	}
	executablePattern := regexp.QuoteMeta(strings.TrimPrefix(executable, "/"))
	readPattern := fmt.Sprintf(`^/(?!System(?:/|$)|usr(?:/|$)|bin(?:/|$)|sbin(?:/|$)|dev(?:/|$)|etc(?:/|$)|Library(?:/|$)|private/etc(?:/|$)|private/var/db(?:/|$)|(?:%s)(?:/|$)|%s$)`, rootPatterns, executablePattern)
	outputFiles := []string{root + "/output/result"}
	if strings.HasPrefix(root, "/private/var/") {
		outputFiles = append(outputFiles, "/"+strings.TrimPrefix(root+"/output/result", "/private/"))
	} else if strings.HasPrefix(root, "/var/") {
		outputFiles = append(outputFiles, "/private"+root+"/output/result")
	}
	writeFilters := `(require-not (literal "/dev/null"))`
	for _, outputFile := range outputFiles {
		writeFilters += fmt.Sprintf(`(require-not (literal "%s"))`, strings.ReplaceAll(outputFile, `"`, `\"`))
	}
	profile := fmt.Sprintf(`(version 1)(allow default)(deny network*)(deny file-read-data (regex #"%s"))(deny file-write* (require-all %s))`, readPattern, writeFilters)
	commandArgs := []string{"-p", profile, "/bin/sh", "-c", `ulimit -f "$1"; shift; exec "$@"`, "agent-workflow-provider", strconv.Itoa(maxOutputBytes / 512), executable}
	return sandbox, append(commandArgs, args...), nil
}
