package evidence_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/patrikmichi/zuano-cli/internal/evidence"
	"github.com/patrikmichi/zuano-cli/internal/evidence/evidencetest"
	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/signing/signingtest"
)

func verify(t *testing.T, b *evidencetest.Bundle, opts evidence.Options) *evidence.Result {
	t.Helper()
	return verifyPaths(t, b.Write(), []signing.Key{b.Evidence.Key}, opts)
}

func verifyPaths(t *testing.T, paths []string, trusted []signing.Key, opts evidence.Options) *evidence.Result {
	t.Helper()
	res, err := evidence.Verify(paths, evidence.Keys{Evidence: trusted}, opts)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	return res
}

func failures(res *evidence.Result) string {
	var parts []string
	for _, f := range res.Failures {
		parts = append(parts, f.String())
	}
	return strings.Join(parts, "\n")
}

func wantFailure(t *testing.T, res *evidence.Result, wants ...string) {
	t.Helper()
	if res.OK() {
		t.Fatal("the bundle verified")
	}
	got := failures(res)
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("failures lack %q:\n%s", want, got)
		}
	}
}

func TestVerify_AcceptsABundleInTheGatewaysFormat(t *testing.T) {
	for name, o := range map[string]evidencetest.Options{
		"one volume":         {},
		"audit parts":        {RowsPerPart: 2},
		"several volumes":    {VolumeBytes: 1},
		"parts and volumes":  {RowsPerPart: 1, VolumeBytes: 100},
		"an unsigned hour":   {SkipCheckpoint: map[int]bool{1: true}},
		"every hour is open": {SkipCheckpoint: map[int]bool{0: true, 1: true, 2: true}},
	} {
		b := evidencetest.New(t, o)
		res := verify(t, b, evidence.Options{})
		if !res.OK() {
			t.Errorf("%s: %s", name, failures(res))
			continue
		}
		if res.Events != 6 || res.Hours != 3 || res.Volumes != b.Volumes || res.Files != len(b.Manifest["files"].([]any)) {
			t.Errorf("%s: events %d, hours %d, volumes %d, files %d", name, res.Events, res.Hours, res.Volumes, res.Files)
		}
		if want := 3 - len(o.SkipCheckpoint); res.VerifiedHours != want || len(res.UnsignedHours) != len(o.SkipCheckpoint) {
			t.Errorf("%s: verified %d, unsigned %v", name, res.VerifiedHours, res.UnsignedHours)
		}
	}
}

func TestVerify_TakesVolumesInAnyOrder(t *testing.T) {
	b := evidencetest.New(t, evidencetest.Options{VolumeBytes: 1})
	paths := b.Write()
	if len(paths) < 3 {
		t.Fatalf("only %d volumes", len(paths))
	}
	slices.Reverse(paths)
	if res := verifyPaths(t, paths, []signing.Key{b.Evidence.Key}, evidence.Options{}); !res.OK() {
		t.Error(failures(res))
	}
}

func TestVerify_RefusesAManifestNobodyTrustedSigned(t *testing.T) {
	b := evidencetest.New(t, evidencetest.Options{})
	forger := signingtest.New("evidence-1")
	forged := evidencetest.New(t, evidencetest.Options{})
	t.Run("a bundle carrying its own key", func(t *testing.T) {
		// A forged bundle lists its own key in keys/public-keys.json; only the trusted key counts.
		wantFailure(t, verifyPaths(t, forged.Write(), []signing.Key{b.Evidence.Key}, evidence.Options{}), "the signature does not verify against the trusted evidence key")
	})
	t.Run("a key id nobody trusts", func(t *testing.T) {
		other := evidencetest.New(t, evidencetest.Options{})
		other.ManifestSigner = signingtest.New("rogue")
		other.Seal()
		wantFailure(t, verifyPaths(t, other.Write(), []signing.Key{b.Evidence.Key}, evidence.Options{}), `"rogue"`, "not among the trusted evidence keys")
	})
	t.Run("a different key under the trusted id", func(t *testing.T) {
		wantFailure(t, verifyPaths(t, b.Write(), []signing.Key{forger.Key}, evidence.Options{}), "does not verify")
	})
	t.Run("an audit key signing the manifest", func(t *testing.T) {
		other := evidencetest.New(t, evidencetest.Options{})
		other.ManifestSigner = other.Audit
		other.Seal()
		wantFailure(t, verify(t, other, evidence.Options{}), "not among the trusted evidence keys")
	})
	t.Run("another purpose in the signed text", func(t *testing.T) {
		other := evidencetest.New(t, evidencetest.Options{})
		other.ManifestPurpose = "audit-checkpoint"
		other.Seal()
		wantFailure(t, verify(t, other, evidence.Options{}), "does not verify")
	})
	t.Run("another purpose named in manifest.sig", func(t *testing.T) {
		other := evidencetest.New(t, evidencetest.Options{})
		other.Files["manifest.sig"] = bytes.ReplaceAll(other.Files["manifest.sig"], []byte("compliance-manifest"), []byte("audit-checkpoint"))
		wantFailure(t, verify(t, other, evidence.Options{}), "was not made for a compliance manifest")
	})
	t.Run("a manifest edited after it was signed", func(t *testing.T) {
		other := evidencetest.New(t, evidencetest.Options{})
		other.Files["manifest.json"] = bytes.ReplaceAll(other.Files["manifest.json"], []byte("admin@acme.test"), []byte("mallory@acme.test"))
		res := verify(t, other, evidence.Options{})
		wantFailure(t, res, "does not verify")
		if res.Files != 0 {
			t.Error("files were read against a manifest that is not signed")
		}
	})
	t.Run("a repeated key in the manifest", func(t *testing.T) {
		other := evidencetest.New(t, evidencetest.Options{})
		text := bytes.Replace(other.Files["manifest.json"], []byte(`"format": 1,`), []byte(`"format": 1, "format": 2,`), 1)
		other.WriteManifest(text)
		wantFailure(t, verify(t, other, evidence.Options{}), "is not valid JSON")
	})
	t.Run("another format", func(t *testing.T) {
		other := evidencetest.New(t, evidencetest.Options{})
		other.WriteManifest(bytes.Replace(other.Files["manifest.json"], []byte(`"format": 1`), []byte(`"format": 2`), 1))
		wantFailure(t, verify(t, other, evidence.Options{}), "is format 2")
	})
	t.Run("no trusted key at all", func(t *testing.T) {
		if _, err := evidence.Verify(b.Write(), evidence.Keys{}, evidence.Options{}); err != evidence.ErrNoEvidenceKeys {
			t.Errorf("err = %v", err)
		}
	})
}

func TestVerify_ChecksEveryFileTheManifestListsAndNothingMore(t *testing.T) {
	t.Run("a file edited to the same length", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		b.Files["controls.csv"] = []byte("control_id,title,statuz\r\n")
		wantFailure(t, verify(t, b, evidence.Options{}), "controls.csv: does not match the hash in the manifest")
	})
	t.Run("a file replaced by a larger one", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		b.Files["controls.csv"] = bytes.Repeat([]byte("0"), 1<<20)
		wantFailure(t, verify(t, b, evidence.Options{}), "controls.csv: declares 1048576 bytes in its archive; the manifest says")
	})
	t.Run("a file missing", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		delete(b.Files, "controls.csv")
		delete(b.Volume, "controls.csv")
		wantFailure(t, verify(t, b, evidence.Options{}), "controls.csv: is listed in the manifest but missing")
	})
	t.Run("a file nobody listed", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		b.Files["extra.txt"] = []byte("x")
		b.Volume["extra.txt"] = 1
		wantFailure(t, verify(t, b, evidence.Options{}), "extra.txt: is in the archives but not listed in the signed manifest")
	})
	t.Run("a file in another volume than the manifest says", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{VolumeBytes: 1})
		b.Volume["controls.csv"] = b.Volumes
		wantFailure(t, verify(t, b, evidence.Options{}), "holds files of volume")
	})
	t.Run("the manifest outside the last volume", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{VolumeBytes: 1})
		b.Volume["manifest.sig"] = 1
		wantFailure(t, verify(t, b, evidence.Options{}), "manifest.sig: is not in the last volume")
	})
	t.Run("a manifest that leaves out the audit checkpoints", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		var kept []any
		for _, e := range b.Manifest["files"].([]any) {
			if e.(map[string]any)["path"] != "audit/checkpoints.jsonl" {
				kept = append(kept, e)
			}
		}
		b.Manifest["files"] = kept
		b.WriteManifest(mustIndent(t, b.Manifest))
		wantFailure(t, verify(t, b, evidence.Options{}), "does not list audit/checkpoints.jsonl")
	})
	t.Run("more bytes than the cap", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		res := verify(t, b, evidence.Options{MaxBytes: 10})
		wantFailure(t, res, "lists more than 10 bytes of files")
		if res.Files != 0 {
			t.Error("files were read past the cap")
		}
	})
}

func TestVerify_NeedsEveryVolume(t *testing.T) {
	b := evidencetest.New(t, evidencetest.Options{VolumeBytes: 1})
	paths := b.Write()
	trusted := []signing.Key{b.Evidence.Key}
	t.Run("the first volume left out", func(t *testing.T) {
		res := verifyPaths(t, paths[1:], trusted, evidence.Options{})
		wantFailure(t, res, "volume 1 of ", "archives were given")
	})
	t.Run("a middle volume left out", func(t *testing.T) {
		res := verifyPaths(t, slices.Concat(paths[:1], paths[2:]), trusted, evidence.Options{})
		wantFailure(t, res, "volume 2 of ")
	})
	t.Run("the last volume left out", func(t *testing.T) {
		if _, err := evidence.Verify(paths[:len(paths)-1], evidence.Keys{Evidence: trusted}, evidence.Options{}); err == nil || !strings.Contains(err.Error(), "manifest.json or manifest.sig is missing") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a volume given twice", func(t *testing.T) {
		wantFailure(t, verifyPaths(t, append(slices.Clone(paths), paths[0]), trusted, evidence.Options{}), "appears more than once")
	})
}

// sealWithOldCheckpoints replaces the audit rows and signs a manifest over them
// while the checkpoints and the stated verification stay as they were.
func sealWithOldCheckpoints(b *evidencetest.Bundle, change func()) {
	checkpoints, stated := b.Files["audit/checkpoints.jsonl"], b.Files["audit/verification.json"]
	change()
	b.RebuildAudit()
	b.Files["audit/checkpoints.jsonl"], b.Files["audit/verification.json"] = checkpoints, stated
	b.Seal()
}

func TestVerify_RecomputesTheAuditCheckpoints(t *testing.T) {
	hour := func(n int) string { return evidencetest.Hour(n).Format("2006-01-02T15:04:05.000Z") }
	t.Run("a row removed", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		sealWithOldCheckpoints(b, func() { b.Rows = b.Rows[:5] })
		wantFailure(t, verify(t, b, evidence.Options{}), "audit hour "+hour(2)+": rows are missing against the signed checkpoint")
	})
	t.Run("a row changed", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		sealWithOldCheckpoints(b, func() { b.Rows[0]["tool"] = "other" })
		wantFailure(t, verify(t, b, evidence.Options{}), "audit hour "+hour(0)+": the rows differ from the signed checkpoint")
	})
	t.Run("a row added", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		sealWithOldCheckpoints(b, func() {
			extra := map[string]any{}
			for k, v := range b.Rows[2] {
				extra[k] = v
			}
			extra["id"], extra["tool"] = json.Number("7"), "added"
			b.Rows = append(b.Rows, extra)
		})
		wantFailure(t, verify(t, b, evidence.Options{}), "audit hour "+hour(1)+": the rows differ")
	})
	t.Run("an hour whose rows are all gone", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		sealWithOldCheckpoints(b, func() { b.Rows = append(slices.Clone(b.Rows[:2]), b.Rows[3:]...) })
		wantFailure(t, verify(t, b, evidence.Options{}), "audit hour "+hour(1)+": there is a checkpoint and no rows")
	})
	t.Run("a checkpoint root changed", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		root := b.Checkpoints[0]["root"].(string)
		b.Files["audit/checkpoints.jsonl"] = bytes.Replace(b.Files["audit/checkpoints.jsonl"], []byte(root), []byte(strings.Repeat("0", 64)), 1)
		b.Seal()
		wantFailure(t, verify(t, b, evidence.Options{}), "audit hour "+hour(0)+": the rows differ from the signed checkpoint")
	})
	t.Run("a checkpoint signed by another key", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		b.Audit = signingtest.New("audit-1")
		b.RebuildAudit()
		stolen := b.Files["audit/checkpoints.jsonl"]
		b.Audit = signingtest.New("audit-1")
		b.RebuildAudit()
		b.Files["audit/checkpoints.jsonl"] = stolen
		b.Seal()
		wantFailure(t, verify(t, b, evidence.Options{}), "the checkpoint signature does not verify")
	})
	t.Run("a checkpoint signed by a key the bundle does not list", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		b.Files["audit/checkpoints.jsonl"] = bytes.ReplaceAll(b.Files["audit/checkpoints.jsonl"], []byte(`"kid":"audit-1"`), []byte(`"kid":"stranger"`))
		b.Seal()
		wantFailure(t, verify(t, b, evidence.Options{}), "signed with a key the bundle does not list")
	})
	t.Run("a stated verification that says more than the rows", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		b.Files["audit/verification.json"] = []byte(`{"hours":3,"verified":2,"unsigned":[],"failures":[],"ok":true}`)
		b.Seal()
		wantFailure(t, verify(t, b, evidence.Options{}), "audit/verification.json: differs from the verification recomputed")
	})
	t.Run("an hour without a checkpoint", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{SkipCheckpoint: map[int]bool{1: true}})
		res := verify(t, b, evidence.Options{RequireCheckpoints: true})
		wantFailure(t, res, "1 audit hours have rows and no signed checkpoint")
		if len(res.UnsignedHours) != 1 || res.UnsignedHours[0] != hour(1) || res.VerifiedHours != 2 {
			t.Errorf("unsigned %v verified %d", res.UnsignedHours, res.VerifiedHours)
		}
	})
	t.Run("audit keys the caller supplies replace the bundle's", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		paths := b.Write()
		res, err := evidence.Verify(paths, evidence.Keys{Evidence: []signing.Key{b.Evidence.Key}, Audit: []signing.Key{b.Audit.Key}}, evidence.Options{})
		if err != nil || !res.OK() {
			t.Fatalf("%v %v", err, res)
		}
		res, err = evidence.Verify(paths, evidence.Keys{Evidence: []signing.Key{b.Evidence.Key}, Audit: []signing.Key{signingtest.New("audit-1").Key}}, evidence.Options{})
		if err != nil {
			t.Fatal(err)
		}
		wantFailure(t, res, "the checkpoint signature does not verify")
	})
}

func mustIndent(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func TestVerify_CatchesRowsThatDoNotBelongInTheRange(t *testing.T) {
	cases := map[string]struct {
		change func(*evidencetest.Bundle)
		want   string
	}{
		"a row after the audit range": {func(b *evidencetest.Bundle) { b.Rows[5]["ts"] = "2026-10-05T00:00:00.000Z" }, "is outside the audit range"},
		"a row of another organization": {func(b *evidencetest.Bundle) { b.Rows[0]["orgId"] = "org_other" }, "belongs to another organization"},
		"ids out of order":                {func(b *evidencetest.Bundle) { b.Rows[0], b.Rows[1] = b.Rows[1], b.Rows[0] }, "is not in ascending id order"},
		"a row without an id":             {func(b *evidencetest.Bundle) { delete(b.Rows[0], "id") }, "has no valid id"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b := evidencetest.New(t, evidencetest.Options{})
			c.change(b)
			b.RebuildAudit()
			b.Seal()
			wantFailure(t, verify(t, b, evidence.Options{}), c.want)
		})
	}
	t.Run("audit parts with a gap", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{RowsPerPart: 3})
		b.Files["audit/events.part-003.jsonl"] = b.Files["audit/events.part-002.jsonl"]
		delete(b.Files, "audit/events.part-002.jsonl")
		b.Seal()
		wantFailure(t, verify(t, b, evidence.Options{}), "not numbered from 1 without a gap")
	})
	t.Run("a repeated key in a row", func(t *testing.T) {
		b := evidencetest.New(t, evidencetest.Options{})
		b.Files["audit/events.part-001.jsonl"] = bytes.Replace(b.Files["audit/events.part-001.jsonl"], []byte(`"event":"tool_call"`), []byte(`"event":"a","event":"b"`), 1)
		b.Seal()
		wantFailure(t, verify(t, b, evidence.Options{}), "line 1: is not a JSON object")
	})
}

// zipWith writes the bundle's single volume with extra entries.
func zipWith(t *testing.T, b *evidencetest.Bundle, extra func(*zip.Writer)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bundle.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range b.Files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(content)
	}
	extra(zw)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return path
}

func TestVerify_RefusesEntriesThatCouldEscapeOrHideOthers(t *testing.T) {
	file := func(name string) func(*zip.Writer) {
		return func(zw *zip.Writer) {
			w, _ := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
			_, _ = w.Write([]byte("x"))
		}
	}
	for name, tc := range map[string]struct {
		extra func(*zip.Writer)
		want  string
	}{
		"a parent directory":      {file("../outside.txt"), "has a name that is empty, absolute, outside the bundle"},
		"a nested parent":         {file("evidence/../../outside.txt"), "has a name that is empty, absolute, outside the bundle"},
		"an absolute path":        {file("/etc/cron.d/x"), "has a name that is empty, absolute, outside the bundle"},
		"a windows path":          {file(`evidence\..\x`), "has a name that is empty, absolute, outside the bundle"},
		"a drive letter":          {file("C:evil"), "has a name that is empty, absolute, outside the bundle"},
		"a dot segment":           {file("evidence/./x"), "has a name that is empty, absolute, outside the bundle"},
		"a doubled slash":         {file("evidence//x"), "has a name that is empty, absolute, outside the bundle"},
		"a control character":     {file("evidence/x\x1b[2J"), "has a name that is empty, absolute, outside the bundle"},
		"a directory":             {func(zw *zip.Writer) { _, _ = zw.Create("evidence/") }, "has a name that is empty, absolute, outside the bundle"},
		"a symbolic link": {func(zw *zip.Writer) {
			h := &zip.FileHeader{Name: "evidence/link", Method: zip.Store}
			h.SetMode(os.ModeSymlink | 0o777)
			w, _ := zw.CreateHeader(h)
			_, _ = w.Write([]byte("/etc/passwd"))
		}, "is a symbolic link"},
		"a device": {func(zw *zip.Writer) {
			h := &zip.FileHeader{Name: "evidence/dev", Method: zip.Store}
			h.SetMode(os.ModeDevice | 0o666)
			_, _ = zw.CreateHeader(h)
		}, "is not a regular file"},
		"a second controls.csv": {file("controls.csv"), "appears more than once"},
	} {
		t.Run(name, func(t *testing.T) {
			b := evidencetest.New(t, evidencetest.Options{})
			res := verifyPaths(t, []string{zipWith(t, b, tc.extra)}, []signing.Key{b.Evidence.Key}, evidence.Options{})
			wantFailure(t, res, tc.want)
		})
	}
}

func TestVerify_NeverPrintsBundleContent(t *testing.T) {
	const secret = "LEAK-THIS-ROW-VALUE"
	b := evidencetest.New(t, evidencetest.Options{})
	sealWithOldCheckpoints(b, func() { b.Rows[0]["tool"] = secret })
	b.Files["controls.csv"] = []byte("LEAK-THIS-FILE-VALUE" + strings.Repeat("0", len(b.Files["controls.csv"])-len("LEAK-THIS-FILE-VALUE")))
	res := verify(t, b, evidence.Options{})
	wantFailure(t, res, "does not match the hash")
	if got := fmt.Sprint(res.Failures); strings.Contains(got, "LEAK-THIS") {
		t.Errorf("failures print bundle content: %s", got)
	}
	long := strings.Repeat("a", 400)
	b2 := evidencetest.New(t, evidencetest.Options{})
	b2.Files[long+".txt"] = []byte("x")
	b2.Volume[long+".txt"] = 1
	res = verify(t, b2, evidence.Options{})
	if got := failures(res); strings.Contains(got, strings.Repeat("a", 150)) {
		t.Errorf("a long entry name was printed whole: %d bytes", len(got))
	}
}

func TestVerify_RefusesInputThatIsNotABundle(t *testing.T) {
	dir := t.TempDir()
	notZip := filepath.Join(dir, "x.zip")
	if err := os.WriteFile(notZip, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	trusted := evidence.Keys{Evidence: []signing.Key{signingtest.New("k").Key}}
	for name, paths := range map[string][]string{"not a zip": {notZip}, "a directory": {dir}, "missing": {filepath.Join(dir, "none.zip")}, "nothing": nil} {
		if _, err := evidence.Verify(paths, trusted, evidence.Options{}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	other := filepath.Join(dir, "plain.zip")
	evidencetest.WriteZip(t, other, []string{"readme.txt"}, func(string) []byte { return []byte("hi") })
	if _, err := evidence.Verify([]string{other}, trusted, evidence.Options{}); err == nil || !strings.Contains(err.Error(), "not a compliance bundle") {
		t.Errorf("a zip without a manifest: %v", err)
	}
}
