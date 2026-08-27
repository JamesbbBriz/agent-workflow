package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
)

type response struct {
	OK          bool   `json:"ok"`
	WorkflowRef string `json:"workflow_ref,omitempty"`
	Hash        string `json:"hash,omitempty"`
	Error       string `json:"error,omitempty"`
	Code        string `json:"code,omitempty"`
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(stderr, "usage: agent-workflow validate --file <workflow.json> [--json]")
		return 2
	}
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("file", "", "workflow definition JSON")
	jsonOutput := flags.Bool("json", false, "write a JSON response")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if *file == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "validate requires exactly one --file")
		return 2
	}
	body, err := os.ReadFile(*file)
	if err != nil {
		return writeError(stdout, stderr, *jsonOutput, "input_unavailable", errors.New("workflow file is unavailable"))
	}
	identity, err := contract.ValidateWorkflow(body)
	if err != nil {
		return writeError(stdout, stderr, *jsonOutput, "invalid_workflow", err)
	}
	result := response{OK: true, WorkflowRef: identity.Ref, Hash: identity.Hash}
	if *jsonOutput {
		_ = json.NewEncoder(stdout).Encode(result)
	} else {
		fmt.Fprintf(stdout, "%s %s\n", identity.Ref, identity.Hash)
	}
	return 0
}

func writeError(stdout, stderr io.Writer, jsonOutput bool, code string, err error) int {
	if jsonOutput {
		_ = json.NewEncoder(stdout).Encode(response{OK: false, Error: err.Error(), Code: code})
	} else {
		fmt.Fprintln(stderr, err)
	}
	return 1
}
