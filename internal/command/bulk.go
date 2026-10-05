package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/infrabay/peopleforce-cli/internal/envelope"
	"github.com/infrabay/peopleforce-cli/internal/httpx"
	"github.com/infrabay/peopleforce-cli/internal/output"
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

  {"id": 101, "set": {"github": "octocat"}}

(a JSON array of the same objects is also accepted). The report on stdout is
NDJSON, one line per record, in input order:

  {"id": 101, "ok": true, "status": 200, "data": {...updated employee...}}
  {"id": 102, "ok": false, "status": 404, "error": {...}}

"data" carries the API's updated record when it returns one, so results can
be verified without follow-up GETs. --output/--jq/--fields do not apply to
this report. Exit code 0 when every record succeeded, 5 when any failed.

Two failures abort the run instead of being reported per record, because
nothing after them can be trusted to have been sent or understood:

  exit 8  network failure. The report stops at the record that failed; it
          was not answered, so it may or may not have been applied.
  exit 9  a 2xx answer that cannot be read: its body was cut off or is not
          JSON (a proxy or WAF page). The last report line has "ok": false
          and an error of type "output"; that update may well have been
          applied, so check it before re-running.

In both cases records reported "ok": true before the last line were applied
and anything after the last line was not attempted: re-run with the
remainder, not the whole file. Exit 5 means the run finished and at least
one record failed (see the per-record report).

--dry-run previews the whole batch without sending anything.`,
		Example: `  peopleforce employees bulk-update --input @updates.jsonl
  peopleforce employees bulk-update --input @updates.jsonl --dry-run
  printf '%s\n' '{"id":101,"set":{"github":"octocat"}}' | peopleforce employees bulk-update --input -`,
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

	enc := &reportEncoder{w: app.Stdout} // NDJSON: one compact line per record

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
			// Report exactly what the process will exit with: a 2xx whose body
			// was cut off is exit 9 / "output", not a network error.
			werr := wrapTransport(err)
			ee := &ExitError{Code: ExitNetwork, Type: "network", Message: err.Error()}
			errors.As(werr, &ee)
			line := map[string]any{"id": rec.ID, "ok": false, "error": map[string]any{"type": ee.Type, "message": ee.Message}}
			if ee.Status != 0 {
				line["status"] = ee.Status
			}
			if encErr := enc.Encode(line); encErr != nil {
				return reportWriteErr(rec.ID, encErr)
			}
			if ee.Code == ExitNetwork {
				fmt.Fprintf(app.Stderr, "%d ok, %d failed, aborted on network error\n", okCount, failCount)
			} else {
				fmt.Fprintf(app.Stderr, "%d ok, %d failed, aborted: the response to employee %s could not be read, the update may have been applied\n", okCount, failCount, rec.ID)
			}
			return werr
		}

		if resp.Status >= 200 && resp.Status <= 299 {
			n := envelope.Normalize(resp.Body, resp.Status)
			if n.NotJSON {
				// A proxy/WAF page answered, so the update may never have
				// reached PeopleForce; "applied" would be a guess. Same exit
				// as `employees update` for this response.
				failCount++
				ee := unusableBodyErr("PUT", resp)
				if encErr := enc.Encode(map[string]any{"id": rec.ID, "ok": false, "status": resp.Status,
					"error": map[string]any{"type": ee.Type, "message": ee.Message}}); encErr != nil {
					return reportWriteErr(rec.ID, encErr)
				}
				fmt.Fprintf(app.Stderr, "%d ok, %d failed, aborted: the answer for employee %s is not JSON, the update may or may not have been applied\n", okCount, failCount, rec.ID)
				return ee
			}
			okCount++
			if err := enc.Encode(map[string]any{
				"id": rec.ID, "ok": true, "status": resp.Status, "data": n.Data,
			}); err != nil {
				return reportWriteErr(rec.ID, err)
			}
			continue
		}
		failCount++
		ee := classifyResponse(resp)
		if err := enc.Encode(map[string]any{
			"id": rec.ID, "ok": false, "status": resp.Status, "error": ee,
		}); err != nil {
			return reportWriteErr(rec.ID, err)
		}
	}

	fmt.Fprintf(app.Stderr, "%d ok, %d failed\n", okCount, failCount)
	if failCount > 0 {
		return &ExitError{Code: ExitValidation, Type: "validation",
			Message: fmt.Sprintf("%d of %d updates failed (per-record report on stdout)", failCount, len(records))}
	}
	return nil
}

// reportEncoder writes NDJSON lines through output.EscapeC1, like every other
// JSON writer: API data in the report (an updated employee's name) could carry
// a C1 control such as U+009B, which encoding/json passes through verbatim and
// a terminal may act on.
type reportEncoder struct{ w io.Writer }

func (e *reportEncoder) Encode(v any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return err
	}
	_, err := e.w.Write(output.EscapeC1(buf.Bytes()))
	return err
}

// reportWriteErr classifies a failed write of the per-record report after the
// PUT already went out. A bare encoder error would be read as a usage error
// ("bad args, safe to re-run"); exit 9 says the mutation happened and only
// its report is missing, same as the curated `employees update`.
func reportWriteErr(id json.Number, err error) error {
	return &ExitError{Code: ExitOutput, Type: "output",
		Message: fmt.Sprintf("employee %s: the update was sent but its report line could not be written: %v", id, err)}
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
		if err := requireNoTrailingData(dec); err != nil {
			return nil, usageErr("--input: %v (a JSON array must be the whole input; use NDJSON for several records)", err)
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
