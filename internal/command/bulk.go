package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/infrabay/peopleforce-cli/internal/envelope"
	"github.com/infrabay/peopleforce-cli/internal/httpx"
)

// bulkRecord is one line of bulk-update input.
type bulkRecord struct {
	ID  json.Number    `json:"id"`
	Set map[string]any `json:"set"`
}

// newEmployeesBulkUpdateCommand batches many PUT /employees/{id} calls into
// one process with one keep-alive connection — 38 updates should not cost
// 38 process launches and 38 TCP handshakes.
func newEmployeesBulkUpdateCommand(app *App) *cobra.Command {
	var inputArg string
	cmd := &cobra.Command{
		Use:   "bulk-update",
		Short: "Update many employees in one run (NDJSON in, NDJSON report out)",
		Long: `Reads update records from --input and applies each as PUT /employees/{id},
reusing a single connection. Input is NDJSON — one JSON object per line:

  {"id": 8321, "set": {"github": "kam1kaze"}}

(a JSON array of the same objects is also accepted). The report on stdout is
NDJSON, one line per record, in input order:

  {"id": 8321, "ok": true, "status": 200, "data": {...updated employee...}}
  {"id": 9999, "ok": false, "status": 404, "error": {...}}

"data" carries the API's updated record when it returns one, so results can
be verified without follow-up GETs. --output/--jq/--fields do not apply to
this report. Exit code 0 when every record succeeded, 5 when any failed.

A network failure aborts the run: the report stops at the last record that
got a response and the exit code is 8, not 5. Records already on stdout were
applied; anything after the last reported line was not attempted, so re-run
with the remainder rather than the whole file.

--dry-run previews the whole batch without sending anything.`,
		Example: `  peopleforce employees bulk-update --input @updates.jsonl
  peopleforce employees bulk-update --input @updates.jsonl --dry-run
  printf '%s\n' '{"id":8321,"set":{"github":"kam1kaze"}}' | peopleforce employees bulk-update --input -`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBulkUpdate(app, inputArg)
		},
	}
	cmd.Flags().StringVar(&inputArg, "input", "", `NDJSON records from @file or - (stdin): {"id": ..., "set": {...}} per line (required)`)
	_ = cmd.MarkFlagRequired("input")
	return cmd
}

func runBulkUpdate(app *App, inputArg string) error {
	raw, err := readInput(app, inputArg)
	if err != nil {
		return err
	}
	records, err := parseBulkRecords(raw)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return usageErr("--input contains no records")
	}

	enc := json.NewEncoder(app.Stdout) // NDJSON: one compact line per record

	if app.dryRun {
		client := app.previewClient()
		for _, rec := range records {
			if err := enc.Encode(map[string]any{
				"id":      rec.ID,
				"dry_run": true,
				"method":  "PUT",
				"url":     client.URL(httpx.Request{Path: "/employees/" + url.PathEscape(rec.ID.String())}),
				"body":    rec.Set,
			}); err != nil {
				return err
			}
		}
		fmt.Fprintf(app.Stderr, "dry-run: %d record(s), nothing sent\n", len(records))
		return nil
	}

	client, err := app.client()
	if err != nil {
		return err
	}

	okCount, failCount := 0, 0
	for _, rec := range records {
		body, err := json.Marshal(rec.Set)
		if err != nil {
			return err
		}
		resp, err := client.Do(context.Background(), httpx.Request{
			Method:      "PUT",
			Path:        "/employees/" + url.PathEscape(rec.ID.String()),
			Body:        body,
			ContentType: "application/json",
		})
		if err != nil {
			// Network failure likely affects the whole batch; report this
			// record and abort rather than burn a timeout per record.
			failCount++
			line := map[string]any{"id": rec.ID, "ok": false,
				"error": map[string]any{"type": "network", "message": err.Error()}}
			if encErr := enc.Encode(line); encErr != nil {
				return encErr
			}
			fmt.Fprintf(app.Stderr, "%d ok, %d failed, aborted on network error\n", okCount, failCount)
			return wrapTransport(err)
		}

		if resp.Status >= 200 && resp.Status <= 299 {
			okCount++
			n := envelope.Normalize(resp.Body, resp.Status)
			if err := enc.Encode(map[string]any{
				"id": rec.ID, "ok": true, "status": resp.Status, "data": n.Data,
			}); err != nil {
				return err
			}
			continue
		}
		failCount++
		ee := classifyStatus(resp.Status, resp.Body)
		if err := enc.Encode(map[string]any{
			"id": rec.ID, "ok": false, "status": resp.Status, "error": ee,
		}); err != nil {
			return err
		}
	}

	fmt.Fprintf(app.Stderr, "%d ok, %d failed\n", okCount, failCount)
	if failCount > 0 {
		return &ExitError{Code: ExitValidation, Type: "validation",
			Message: fmt.Sprintf("%d of %d updates failed (per-record report on stdout)", failCount, len(records))}
	}
	return nil
}

// parseBulkRecords accepts NDJSON (or any concatenated JSON objects) as well
// as a single JSON array of records.
func parseBulkRecords(raw []byte) ([]bulkRecord, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, usageErr("--input is empty")
	}

	var records []bulkRecord
	if trimmed[0] == '[' {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if err := dec.Decode(&records); err != nil {
			return nil, usageErr("--input: invalid JSON array: %v", err)
		}
	} else {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		for i := 1; ; i++ {
			var rec bulkRecord
			if err := dec.Decode(&rec); err == io.EOF {
				break
			} else if err != nil {
				return nil, usageErr("--input: invalid JSON on record %d: %v", i, err)
			}
			records = append(records, rec)
		}
	}

	for i, rec := range records {
		if n, err := rec.ID.Int64(); err != nil || n <= 0 {
			return nil, usageErr(`--input record %d: "id" must be a positive integer, got %q`, i+1, rec.ID.String())
		}
		if len(rec.Set) == 0 {
			return nil, usageErr(`--input record %d (id %s): missing or empty "set"`, i+1, rec.ID)
		}
	}
	return records, nil
}
