package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// sessionFileDoc mirrors a file of a session in the gateway's file store.
type sessionFileDoc struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	State       string `json:"state"`
	CreatedAt   string `json:"createdAt"`
	ExpiresAt   string `json:"expiresAt"`
}

func printJSONDoc(out io.Writer, doc any) error { return json.NewEncoder(out).Encode(doc) }

func sessionFilePath(id string, rest ...string) string {
	path := sessionPath(id, "/files")
	for _, r := range rest {
		path += "/" + url.PathEscape(r)
	}
	return path
}

func bsFilesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "files",
		Short: "List, upload, download and delete the files of a session",
		Long: `A session has a folder in the file store that code run in the session reads and
writes. These commands manage it with a project server key (ZUANO_API_KEY) or a
session token; the files go with the session. A file is scanned after its upload
and can be downloaded once it is clean.`,
	}
	cmd.AddCommand(bsFilesListCmd(), bsFilesUploadCmd(), bsFilesDownloadCmd(), bsFilesRmCmd())
	return cmd
}

func bsFilesListCmd() *cobra.Command {
	var f approvalFlags
	var limit int
	var cursor string
	cmd := &cobra.Command{
		Use:   "list <ses_id>",
		Short: "List a session's files, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := builderID(args[0], "ses_", "a session id")
			if err != nil {
				return err
			}
			if limit < 0 || limit > 100 {
				return errors.New("--limit is between 1 and 100")
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			params := map[string]string{"cursor": cursor}
			if limit > 0 {
				params["limit"] = strconv.Itoa(limit)
			}
			var page struct {
				Items      []sessionFileDoc `json:"items"`
				NextCursor string           `json:"nextCursor"`
			}
			raw, err := builderRequest(cmd, c, f.jsonOut, http.MethodGet, withQuery(sessionFilePath(id), params), nil, &page)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if f.jsonOut {
				printRaw(out, raw)
				return nil
			}
			rows := make([][]string, 0, len(page.Items))
			for _, file := range page.Items {
				rows = append(rows, []string{file.ID, file.Name, file.ContentType, strconv.FormatInt(file.SizeBytes, 10), file.State, file.ExpiresAt})
			}
			printRows(out, []string{"ID", "NAME", "TYPE", "BYTES", "STATE", "EXPIRES"}, rows)
			if page.NextCursor != "" {
				fmt.Fprintf(out, "More: --cursor %s\n", textsafe.Clean(page.NextCursor))
			}
			return nil
		},
	}
	f.bind(cmd, "Print the list as JSON")
	cmd.Flags().IntVar(&limit, "limit", 0, "At most this many files (1 to 100)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue after this cursor")
	return cmd
}

func bsFilesUploadCmd() *cobra.Command {
	var f approvalFlags
	var name string
	cmd := &cobra.Command{
		Use:   "upload <ses_id> <path>...",
		Short: "Upload files into a session's folder",
		Long: `Uploads each file straight to the object store with a signed address from the
gateway, then tells the gateway it is done. The gateway scans the file before it
can be downloaded or used by the session's code; the state printed is where the
scan stands.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := builderID(args[0], "ses_", "a session id")
			if err != nil {
				return err
			}
			if name != "" && len(args) > 2 {
				return errors.New("--name names one file; upload several without it")
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var docs []sessionFileDoc
			var firstErr error
			for _, path := range args[1:] {
				doc, err := uploadSessionFile(cmd, c, id, path, name, f.jsonOut)
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", textsafe.Clean(path), err)
					continue
				}
				docs = append(docs, doc)
				if !f.jsonOut {
					fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", doc.ID, orDash(textsafe.Clean(doc.State)), textsafe.Clean(path))
				}
			}
			if f.jsonOut {
				if err := printJSONDoc(cmd.OutOrStdout(), map[string]any{"files": docs}); err != nil {
					return err
				}
			}
			if firstErr != nil {
				return &ExitError{Code: ExitCode(firstErr)}
			}
			return nil
		},
	}
	f.bind(cmd, "Print the uploaded files as one JSON document")
	cmd.Flags().StringVar(&name, "name", "", "Name to store the file under (default: its file name)")
	return cmd
}

func uploadSessionFile(cmd *cobra.Command, c *client.Client, sessionID, path, name string, quiet bool) (sessionFileDoc, error) {
	ctx := cmd.Context()
	fh, err := os.Open(path)
	if err != nil {
		return sessionFileDoc{}, err
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		return sessionFileDoc{}, err
	}
	if !info.Mode().IsRegular() {
		return sessionFileDoc{}, errors.New("not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return sessionFileDoc{}, err
	}
	name = firstNonEmptyString(name, filepath.Base(path))
	contentType := detectContentType(fh, path)

	var created struct {
		FileID string `json:"fileId"`
		Upload struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"upload"`
	}
	body := map[string]any{"name": name, "contentType": contentType, "size": info.Size(), "sha256": hex.EncodeToString(h.Sum(nil))}
	if _, err := builderRequest(cmd, c, quiet, http.MethodPost, sessionFilePath(sessionID), body, &created); err != nil {
		return sessionFileDoc{}, err
	}
	if created.FileID == "" {
		return sessionFileDoc{}, errors.New("the gateway did not return a file id for the upload")
	}
	target, err := presignedURL(created.Upload.URL)
	if err != nil {
		return sessionFileDoc{}, err
	}
	var progress io.Writer
	if !quiet {
		progress = progressWriter(cmd)
	}
	for attempt := 0; ; attempt++ {
		if _, err := fh.Seek(0, io.SeekStart); err != nil {
			return sessionFileDoc{}, err
		}
		err = putObject(ctx, target, uploadTarget{URL: created.Upload.URL, Method: created.Upload.Method, Headers: created.Upload.Headers}, fh, info.Size(), contentType, progress, name)
		var netErr *networkError
		if err == nil || attempt > 0 || !errors.As(err, &netErr) {
			break
		}
	}
	if err != nil {
		return sessionFileDoc{}, err
	}
	var done struct {
		File sessionFileDoc `json:"file"`
	}
	if _, err := builderRequest(cmd, c, quiet, http.MethodPost, sessionFilePath(sessionID, created.FileID, "complete"), map[string]any{}, &done); err != nil {
		return sessionFileDoc{}, err
	}
	if done.File.ID == "" {
		done.File.ID = created.FileID
	}
	return done.File, nil
}

func bsFilesDownloadCmd() *cobra.Command {
	var f approvalFlags
	var output string
	var force bool
	cmd := &cobra.Command{
		Use:   "download <ses_id> <file_id>",
		Short: "Download a clean file of a session",
		Long: `Saves the file under -o, or under its own name in the current directory, and
refuses to replace one that exists unless --force. A file still being scanned,
or one the scan rejected, cannot be downloaded (the gateway answers 409).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := builderID(args[0], "ses_", "a session id")
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var doc struct {
				File     sessionFileDoc `json:"file"`
				Download struct {
					URL string `json:"url"`
				} `json:"download"`
			}
			if _, err := builderRequest(cmd, c, f.jsonOut, http.MethodGet, sessionFilePath(id, args[1]), nil, &doc); err != nil {
				return err
			}
			doc.File.ID = firstNonEmptyString(doc.File.ID, args[1])
			target, err := downloadTarget(output, fileDoc{ID: doc.File.ID, Name: doc.File.Name})
			if err != nil {
				return err
			}
			saved, err := saveSessionFile(cmd.Context(), doc.Download.URL, target, doc.File, force)
			if err != nil {
				return err
			}
			if f.jsonOut {
				return printJSONDoc(cmd.OutOrStdout(), map[string]any{"file": doc.File, "saved": saved})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved %s (%d bytes)\n", textsafe.Clean(saved), doc.File.SizeBytes)
			return nil
		},
	}
	f.bind(cmd, "Print the file's record and where it was saved as JSON")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Where to save it: a file, or a directory (default: the file's own name)")
	cmd.Flags().BoolVar(&force, "force", false, "Replace the file if it exists")
	return cmd
}

// saveSessionFile fetches the signed address and writes the bytes beside
// target before moving them into place, so a failed download leaves nothing.
func saveSessionFile(ctx context.Context, address, target string, meta sessionFileDoc, force bool) (string, error) {
	u, err := presignedURL(address)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(target); err == nil && !force {
		return "", fmt.Errorf("%s already exists; use --force to replace it", textsafe.Clean(target))
	}
	reqCtx, cancel, err := requestContext(ctx, transferRequest)
	if err != nil {
		return "", err
	}
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := storageClient.Do(req)
	if err != nil {
		return "", timeoutCause(reqCtx, fmt.Errorf("download %s: %w", textsafe.Clean(meta.ID), err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the object store refused the download of %s (HTTP %d)", textsafe.Clean(meta.ID), resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".zuano-download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", timeoutCause(reqCtx, fmt.Errorf("download %s: %w", textsafe.Clean(meta.ID), err))
	}
	if meta.SizeBytes > 0 && n != meta.SizeBytes {
		return "", fmt.Errorf("downloaded %d bytes but the gateway recorded %d for %s", n, meta.SizeBytes, textsafe.Clean(meta.ID))
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

func bsFilesRmCmd() *cobra.Command {
	var f approvalFlags
	cmd := &cobra.Command{
		Use:   "rm <ses_id> <file_id>...",
		Short: "Delete files of a session",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := builderID(args[0], "ses_", "a session id")
			if err != nil {
				return err
			}
			c, err := approvalsClient(f.gatewayURL)
			if err != nil {
				return err
			}
			var firstErr error
			for _, fileID := range args[1:] {
				if _, err := builderRequest(cmd, c, f.jsonOut, http.MethodDelete, sessionFilePath(id, fileID), nil, nil); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", textsafe.Clean(fileID), err)
					continue
				}
				if !f.jsonOut {
					fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s\n", textsafe.Clean(fileID))
				}
			}
			if firstErr != nil {
				return &ExitError{Code: ExitCode(firstErr)}
			}
			return nil
		},
	}
	f.bind(cmd, "Print nothing but errors")
	return cmd
}
