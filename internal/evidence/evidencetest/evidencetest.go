// Package evidencetest builds compliance export bundles the way the gateway
// writes them (format 1: volumes, a nested signed manifest, hourly audit
// checkpoints), with test keys, for tests of the code that verifies them.
package evidencetest

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/signing/signingtest"
)

// Options shape a built bundle.
type Options struct {
	// RowsPerPart splits the audit rows into parts; zero keeps one part.
	RowsPerPart int
	// VolumeBytes closes a volume once the files in it pass this many bytes; zero keeps one volume.
	VolumeBytes int
	// SkipCheckpoint leaves out the checkpoint of these hours (counted from the first hour).
	SkipCheckpoint map[int]bool
}

// Bundle is a bundle in memory: its files, where each one is, and the keys
// that signed it. Change Files, Rows or Manifest and call Seal again to sign
// the new state, or change Files after Seal to break what was signed.
type Bundle struct {
	t        testing.TB
	opts     Options
	Evidence *signingtest.Signer
	Audit    *signingtest.Signer
	OrgID    string
	Rows     []map[string]any
	// Files holds every file except manifest.json and manifest.sig.
	Files   map[string][]byte
	Volume  map[string]int
	Volumes int
	// Manifest is the document Seal signs; it is rebuilt from Files by Seal.
	Manifest map[string]any
	// ManifestSigner and ManifestPurpose decide who signs the manifest and for what.
	ManifestSigner  *signingtest.Signer
	ManifestPurpose string
	Checkpoints     []map[string]any
}

// Hour returns the start of the nth hour of the bundle's period.
func Hour(n int) time.Time { return time.Date(2026, 10, 1, n, 0, 0, 0, time.UTC) }

const stamp = "2006-01-02T15:04:05.000Z"

// New builds a signed bundle of six audit rows over three hours, with a
// checkpoint for each hour.
func New(t testing.TB, o Options) *Bundle {
	t.Helper()
	b := &Bundle{t: t, opts: o, Evidence: signingtest.New("evidence-1"), Audit: signingtest.New("audit-1"), OrgID: "org_acme", Files: map[string][]byte{}, Volume: map[string]int{}, ManifestPurpose: "compliance-manifest"}
	b.ManifestSigner = b.Evidence
	for i, h := range []int{0, 0, 1, 2, 2, 2} {
		b.Rows = append(b.Rows, map[string]any{
			"id": json.Number(fmt.Sprint(i + 1)), "orgId": b.OrgID, "ts": Hour(h).Add(time.Minute).Format(stamp), "event": "tool_call",
			"tool": "tool-" + string(rune('a'+i)), "outcome": "ok", "service": nil, "dlp": map[string]any{"classes": []any{"email"}, "note": nil},
		})
	}
	b.RebuildAudit()
	b.Seal()
	return b
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func (b *Bundle) canonical(v any) []byte { return must(signing.CanonicalJSON(v)) }

func leaf(canon []byte) [32]byte { return sha256.Sum256(append([]byte{0}, canon...)) }

func parent(l, r [32]byte) [32]byte {
	return sha256.Sum256(append(append([]byte{1}, l[:]...), r[:]...))
}

// root is the Merkle root the gateway computes: pairs upward, an odd node carried up.
func root(leaves [][32]byte) [32]byte {
	for len(leaves) > 1 {
		var next [][32]byte
		for i := 0; i < len(leaves); i += 2 {
			if i+1 < len(leaves) {
				next = append(next, parent(leaves[i], leaves[i+1]))
			} else {
				next = append(next, leaves[i])
			}
		}
		leaves = next
	}
	return leaves[0]
}

func rowWithoutNulls(row map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range row {
		if v != nil {
			out[k] = v
		}
	}
	return out
}

func rowTime(row map[string]any) time.Time {
	return must(time.Parse(time.RFC3339Nano, row["ts"].(string))).UTC()
}

func rowID(row map[string]any) int64 {
	num, _ := row["id"].(json.Number)
	n, _ := num.Int64()
	return n
}

// RebuildAudit rewrites the audit parts, the checkpoints, their verification
// and the key file from Rows.
func (b *Bundle) RebuildAudit() {
	for name := range b.Files {
		if strings.HasPrefix(name, "audit/") {
			delete(b.Files, name)
		}
	}
	per := b.opts.RowsPerPart
	if per <= 0 {
		per = len(b.Rows) + 1
	}
	for part := 1; part == 1 || (part-1)*per < len(b.Rows); part++ {
		var sb strings.Builder
		for i := (part - 1) * per; i < len(b.Rows) && i < part*per; i++ {
			sb.Write(b.canonical(b.Rows[i]))
			sb.WriteByte('\n')
		}
		b.Files[fmt.Sprintf("audit/events.part-%03d.jsonl", part)] = []byte(sb.String())
	}

	byHour := map[time.Time][]map[string]any{}
	var hours []time.Time
	for _, row := range b.Rows {
		h := rowTime(row).Truncate(time.Hour)
		if _, ok := byHour[h]; !ok {
			hours = append(hours, h)
		}
		byHour[h] = append(byHour[h], row)
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Before(hours[j]) })
	var cpLines strings.Builder
	b.Checkpoints = nil
	verified, unsigned := 0, []any{}
	for _, h := range hours {
		if b.opts.SkipCheckpoint[int(h.Sub(Hour(0))/time.Hour)] {
			unsigned = append(unsigned, h.Format(stamp))
			continue
		}
		rows := byHour[h]
		var leaves [][32]byte
		for _, r := range rows {
			leaves = append(leaves, leaf(b.canonical(rowWithoutNulls(r))))
		}
		rt := root(leaves)
		cp := map[string]any{"orgId": b.OrgID, "hourStart": h.Format(stamp), "rowCount": json.Number(fmt.Sprint(len(rows))), "firstId": json.Number(fmt.Sprint(rowID(rows[0]))), "lastId": json.Number(fmt.Sprint(rowID(rows[len(rows)-1]))), "root": hex.EncodeToString(rt[:])}
		signed := map[string]any{"purpose": "audit-checkpoint"}
		for k, v := range cp {
			signed[k] = v
		}
		cp["kid"] = b.Audit.Key.KID
		cp["signature"] = base64.StdEncoding.EncodeToString(b.Audit.SignMessage(b.canonical(signed)))
		b.Checkpoints = append(b.Checkpoints, cp)
		cpLines.Write(b.canonical(cp))
		cpLines.WriteByte('\n')
		verified++
	}
	b.Files["audit/checkpoints.jsonl"] = []byte(cpLines.String())
	b.Files["audit/verification.json"] = b.indent(map[string]any{"hours": len(hours), "verified": verified, "unsigned": unsigned, "failures": []any{}, "ok": true})
	b.Files["keys/public-keys.json"] = b.indent(map[string]any{"audit": []signing.Key{b.Audit.Key}, "evidence": []signing.Key{b.Evidence.Key}})
	b.Files["controls.csv"] = []byte("control_id,title,status\r\n")
	b.Files["evidence/index.json"] = []byte("[]\n")
	b.Files["README.txt"] = []byte("how to verify\n")
}

func (b *Bundle) indent(v any) []byte {
	return append(must(json.MarshalIndent(v, "", "  ")), '\n')
}

// Seal lists every file in the manifest, assigns volumes, and signs it.
func (b *Bundle) Seal() {
	delete(b.Files, "manifest.json")
	delete(b.Files, "manifest.sig")
	var names []string
	for name := range b.Files {
		names = append(names, name)
	}
	// The gateway writes the README after everything else, in the volume the manifest joins.
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == "README.txt") != (names[j] == "README.txt") {
			return names[j] == "README.txt"
		}
		return names[i] < names[j]
	})
	b.Volume = map[string]int{}
	vol, used := 1, 0
	var entries []any
	for _, name := range names {
		if name != "README.txt" && b.opts.VolumeBytes > 0 && used >= b.opts.VolumeBytes {
			vol++
			used = 0
		}
		b.Volume[name] = vol
		used += len(b.Files[name])
		sum := sha256.Sum256(b.Files[name])
		entries = append(entries, map[string]any{"path": name, "sha256": hex.EncodeToString(sum[:]), "size": len(b.Files[name]), "volume": vol})
	}
	b.Volumes = vol
	b.Manifest = map[string]any{
		"format": 1, "exportId": "cex_1", "pack": map[string]any{"id": "iso27001", "version": "1"}, "mapping": map[string]any{"version": "1", "reviewed": true},
		"org": map[string]any{"id": b.OrgID, "slug": "acme"}, "range": map[string]any{"from": "2026-10-01T00:30:00.000Z", "to": "2026-10-01T03:15:00.000Z"},
		"auditRange": map[string]any{"from": Hour(0).Format(stamp), "to": Hour(4).Format(stamp)}, "generatedAt": "2026-10-20T00:00:00.000Z", "generator": nil,
		"requestedBy": "admin@acme.test", "volumes": vol, "files": entries, "coverage": map[string]any{"covered": 1}, "disclaimer": "d", "evidenceKeyId": b.Evidence.Key.KID,
	}
	b.WriteManifest(b.indent(b.Manifest))
}

// WriteManifest stores manifest text as manifest.json, signed as the bundle's
// ManifestSigner and ManifestPurpose say, over the text's own parsed form.
func (b *Bundle) WriteManifest(text []byte) {
	var doc any
	dec := json.NewDecoder(strings.NewReader(string(text)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		b.t.Fatal(err)
	}
	sig := b.ManifestSigner.SignMessage(b.canonical(map[string]any{"purpose": b.ManifestPurpose, "document": doc}))
	b.Files["manifest.json"] = text
	b.Volume["manifest.json"] = b.Volumes
	b.Files["manifest.sig"] = b.indent(map[string]any{"purpose": "compliance-manifest", "kid": b.ManifestSigner.Key.KID, "alg": "ed25519", "signature": base64.StdEncoding.EncodeToString(sig)})
	b.Volume["manifest.sig"] = b.Volumes
}

// Write stores each volume as a zip archive and returns their paths in volume order.
func (b *Bundle) Write() []string {
	t := b.t
	dir := t.TempDir()
	var paths []string
	for v := 1; v <= b.Volumes; v++ {
		var names []string
		for name, vol := range b.Volume {
			if vol == v {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		paths = append(paths, WriteZip(t, filepath.Join(dir, fmt.Sprintf("bundle-%03d.zip", v)), names, func(name string) []byte { return b.Files[name] }))
	}
	return paths
}

// WriteZip writes a zip of the named entries, deflated.
func WriteZip(t testing.TB, path string, names []string, content func(string) []byte) string {
	t.Helper()
	f := must(os.Create(path))
	zw := zip.NewWriter(f)
	for _, name := range names {
		w := must(zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate}))
		must(w.Write(content(name)))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
