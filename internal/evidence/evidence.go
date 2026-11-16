// Package evidence checks a compliance export bundle on this machine, with no
// session. The gateway splits a bundle into volumes and signs a manifest that
// lists every file with its hash, size and volume; the audit log in it carries
// hourly Merkle checkpoints. The manifest signature is checked against evidence
// keys the caller trusts; the audit checkpoint keys come from the bundle's key
// file, which the signed manifest covers by hash.
package evidence

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/patrikmichi/zuano-cli/internal/signing"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

// Names and wire constants of the gateway's bundle format.
const (
	ManifestFile      = "manifest.json"
	SignatureFile     = "manifest.sig"
	ManifestPurpose   = "compliance-manifest"
	BundleFormat      = 1
	checkpointsFile   = "audit/checkpoints.jsonl"
	verificationFile  = "audit/verification.json"
	keysFile          = "keys/public-keys.json"
	firstAuditPart    = "audit/events.part-001.jsonl"
	maxEntries        = 200000
	maxVolumes        = 1000
	maxSmallFile      = 64 << 20
	maxManifest       = 32 << 20
	maxSignatureFile  = 64 << 10
	maxLine           = 16 << 20
	maxFailures       = 50
	maxShownRunes     = 100
	DefaultMaxBytes   = int64(16) << 30
	hourTimestampForm = "2006-01-02T15:04:05.000Z"
)

var (
	auditPart = regexp.MustCompile(`^audit/events\.part-(\d{3,9})\.jsonl$`)
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// ErrNoEvidenceKeys means the caller supplied no key to check the manifest with.
	ErrNoEvidenceKeys = errors.New("no trusted evidence key was given")
)

// FileEntry is a file the manifest lists.
type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// Volume is the volume that holds the file, counting from 1.
	Volume int `json:"volume"`
}

// Manifest is the signed description of a bundle.
type Manifest struct {
	Format   int    `json:"format"`
	ExportID string `json:"exportId"`
	Pack     struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"pack"`
	Org struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"org"`
	Range struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"range"`
	// AuditRange is the range widened to whole hours; it is what audit/ covers.
	AuditRange struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"auditRange"`
	GeneratedAt   string      `json:"generatedAt"`
	RequestedBy   string      `json:"requestedBy"`
	Volumes       int         `json:"volumes"`
	Files         []FileEntry `json:"files"`
	EvidenceKeyID string      `json:"evidenceKeyId"`
}

// Failure is one thing that does not check out, naming a file or an audit
// hour. It never carries content from the bundle beyond a file name.
type Failure struct {
	File string
	Msg  string
}

func (f Failure) String() string {
	if f.File != "" {
		return f.File + ": " + f.Msg
	}
	return f.Msg
}

// Result is what verification found.
type Result struct {
	Manifest      Manifest
	Volumes       int
	Files         int
	Events        int64
	Hours         int
	VerifiedHours int
	// UnsignedHours are hours with audit rows and no checkpoint; they are not verified.
	UnsignedHours []string
	Failures      []Failure
	// Dropped counts failures past the ones kept.
	Dropped int
}

// OK reports a bundle with no failures.
func (r *Result) OK() bool { return len(r.Failures) == 0 && r.Dropped == 0 }

func (r *Result) fail(file, format string, args ...any) {
	if len(r.Failures) >= maxFailures {
		r.Dropped++
		return
	}
	r.Failures = append(r.Failures, Failure{File: shown(file), Msg: fmt.Sprintf(format, args...)})
}

// shown makes text from the bundle fit to print.
func shown(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	if r := []rune(s); len(r) > maxShownRunes {
		s = string(r[:maxShownRunes]) + "..."
	}
	return textsafe.Clean(s)
}

// Keys are the public keys a bundle is checked against.
type Keys struct {
	// Evidence keys check the manifest signature. They must come from a source
	// the caller trusts, never from the bundle.
	Evidence []signing.Key
	// Audit keys check the checkpoint signatures. Left empty, the audit keys in
	// the bundle's key file are used; the signed manifest lists that file's hash,
	// so they are as trustworthy as the evidence key that signed it.
	Audit []signing.Key
}

// Options tune a verification.
type Options struct {
	// MaxBytes caps the uncompressed size of all files together; zero means DefaultMaxBytes.
	MaxBytes int64
	// RequireCheckpoints fails a bundle with audit hours that have no signed checkpoint.
	RequireCheckpoints bool
}

type volume struct {
	label string
	file  *os.File
	files map[string]*zip.File
}

// Verify checks a bundle given as its volume archives, in any order. It returns
// an error only when the input cannot be read as a bundle at all; everything
// wrong inside a readable bundle is a Failure in the result. Nothing is
// extracted: each file is hashed as it streams out of its archive.
func Verify(paths []string, keys Keys, opts Options) (*Result, error) {
	if len(paths) == 0 || len(paths) > maxVolumes {
		return nil, fmt.Errorf("give between 1 and %d volume archives", maxVolumes)
	}
	if len(keys.Evidence) == 0 {
		return nil, ErrNoEvidenceKeys
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	res := &Result{}
	vols, owner, err := openVolumes(paths, res)
	defer func() {
		for _, v := range vols {
			_ = v.file.Close()
		}
	}()
	if err != nil {
		return nil, err
	}
	mIdx, hasManifest := owner[ManifestFile]
	sIdx, hasSig := owner[SignatureFile]
	if !hasManifest || !hasSig {
		return nil, errors.New("the archives are not a compliance bundle: manifest.json or manifest.sig is missing (the manifest is in the last volume)")
	}
	manifestBytes, err := readBounded(vols[mIdx].files[ManifestFile], maxManifest)
	if err != nil {
		return nil, fmt.Errorf("manifest.json cannot be read: %w", err)
	}
	sigBytes, err := readBounded(vols[sIdx].files[SignatureFile], maxSignatureFile)
	if err != nil {
		return nil, fmt.Errorf("manifest.sig cannot be read: %w", err)
	}
	sig, ok := parseSignature(res, sigBytes)
	if !ok {
		return res, nil
	}
	tree, err := parseJSON(manifestBytes)
	if err != nil {
		res.fail(ManifestFile, "is not valid JSON")
		return res, nil
	}
	if !checkManifestSignature(res, keys.Evidence, tree, sig) {
		return res, nil
	}
	if err := json.Unmarshal(manifestBytes, &res.Manifest); err != nil {
		res.fail(ManifestFile, "does not have the shape of a manifest")
		return res, nil
	}
	m := &res.Manifest
	if !checkManifest(res, m, sig.kid, opts.MaxBytes) {
		return res, nil
	}
	res.Volumes = m.Volumes
	assign := assignVolumes(res, m, vols, owner, mIdx, sIdx, len(paths))
	checkListedFiles(res, m, vols, owner, assign, keys, opts)
	listed := map[string]bool{ManifestFile: true, SignatureFile: true}
	for _, e := range m.Files {
		listed[e.Path] = true
	}
	var unlisted []string
	for name := range owner {
		if !listed[name] {
			unlisted = append(unlisted, name)
		}
	}
	sort.Strings(unlisted)
	for _, name := range unlisted {
		res.fail(name, "is in the archives but not listed in the signed manifest")
	}
	return res, nil
}

// openVolumes opens every archive and indexes its entries. An entry that could
// harm a reader that extracts it, or that hides another, is a failure and is
// left out of the index.
func openVolumes(paths []string, res *Result) ([]*volume, map[string]int, error) {
	var vols []*volume
	owner := map[string]int{}
	total := 0
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return vols, nil, fmt.Errorf("open %s: %w", shown(filepath.Base(p)), err)
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			return vols, nil, fmt.Errorf("%s is not a regular file", shown(filepath.Base(p)))
		}
		zr, err := zip.NewReader(f, info.Size())
		if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
			_ = f.Close()
			return vols, nil, fmt.Errorf("%s is not a readable zip archive", shown(filepath.Base(p)))
		}
		v := &volume{label: shown(filepath.Base(p)), file: f, files: map[string]*zip.File{}}
		vols = append(vols, v)
		idx := len(vols) - 1
		total += len(zr.File)
		if total > maxEntries {
			return vols, nil, fmt.Errorf("the archives have more than %d entries", maxEntries)
		}
		for _, zf := range zr.File {
			if problem := entryProblem(zf); problem != "" {
				res.fail(zf.Name, "%s", problem)
				continue
			}
			if first, dup := owner[zf.Name]; dup {
				res.fail(zf.Name, "appears more than once (in %s and %s)", vols[first].label, v.label)
				continue
			}
			owner[zf.Name] = idx
			v.files[zf.Name] = zf
		}
	}
	return vols, owner, nil
}

// entryProblem says why an archive entry is refused, or returns "".
func entryProblem(zf *zip.File) string {
	switch mode := zf.Mode(); {
	case !safeName(zf.Name):
		return "has a name that is empty, absolute, outside the bundle, or contains a backslash, colon or control character"
	case mode&os.ModeSymlink != 0:
		return "is a symbolic link"
	case mode.IsDir():
		return "is a directory entry"
	case !mode.IsRegular():
		return "is not a regular file"
	case zf.Flags&0x1 != 0:
		return "is encrypted"
	}
	return ""
}

func safeName(name string) bool {
	if name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '\\' || r == ':' {
			return false
		}
	}
	return !slices.Contains(strings.Split(name, "/"), "..") && !slices.Contains(strings.Split(name, "/"), ".")
}

// readBounded reads a small file whose declared size is within max; a file that
// turns out larger than it declared is refused.
func readBounded(f *zip.File, max int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(max) {
		return nil, fmt.Errorf("it is larger than %d bytes", max)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("it is larger than %d bytes", max)
	}
	return data, nil
}

type manifestSig struct {
	kid string
	raw []byte
}

func parseSignature(res *Result, raw []byte) (manifestSig, bool) {
	v, err := parseJSON(raw)
	obj, isObj := v.(map[string]any)
	if err != nil || !isObj || len(obj) != 4 {
		res.fail(SignatureFile, "is not {purpose, kid, alg, signature}")
		return manifestSig{}, false
	}
	str := func(k string) string { s, _ := obj[k].(string); return s }
	if str("purpose") != ManifestPurpose {
		res.fail(SignatureFile, "was not made for a compliance manifest")
		return manifestSig{}, false
	}
	if str("alg") != "ed25519" {
		res.fail(SignatureFile, "names an algorithm other than ed25519")
		return manifestSig{}, false
	}
	decoded, err := base64.StdEncoding.DecodeString(str("signature"))
	if err != nil || str("kid") == "" {
		res.fail(SignatureFile, "has no readable key id or signature")
		return manifestSig{}, false
	}
	return manifestSig{kid: str("kid"), raw: decoded}, true
}

// checkManifestSignature verifies Ed25519 over the canonical JSON of
// {"purpose":"compliance-manifest","document":<manifest>} with a trusted key.
func checkManifestSignature(res *Result, trusted []signing.Key, manifest any, sig manifestSig) bool {
	payload, err := signing.CanonicalJSON(map[string]any{"purpose": ManifestPurpose, "document": manifest})
	if err != nil {
		res.fail(ManifestFile, "cannot be put in canonical form")
		return false
	}
	if !slices.ContainsFunc(trusted, func(k signing.Key) bool { return k.KID == sig.kid }) {
		res.fail(ManifestFile, "is signed with key %q, which is not among the trusted evidence keys", shown(sig.kid))
		return false
	}
	if err := signing.VerifyMessage(trusted, sig.kid, payload, sig.raw); err != nil {
		res.fail(ManifestFile, "the signature does not verify against the trusted evidence key")
		return false
	}
	return true
}

// checkManifest checks what the signed manifest says about itself and returns
// false when the rest of the bundle cannot be read against it.
func checkManifest(res *Result, m *Manifest, signedBy string, maxBytes int64) bool {
	ok := true
	bad := func(format string, args ...any) {
		res.fail(ManifestFile, format, args...)
		ok = false
	}
	if m.Format != BundleFormat {
		bad("is format %d; this version reads format %d", m.Format, BundleFormat)
		return false
	}
	if m.EvidenceKeyID != signedBy {
		bad("names an evidence key other than the one that signed it")
	}
	if m.Org.ID == "" {
		bad("names no organization")
	}
	if m.Volumes < 1 || m.Volumes > maxVolumes {
		bad("states %d volumes", m.Volumes)
	}
	from, errFrom := time.Parse(time.RFC3339Nano, m.AuditRange.From)
	to, errTo := time.Parse(time.RFC3339Nano, m.AuditRange.To)
	if errFrom != nil || errTo != nil || !from.Before(to) || !from.Equal(from.Truncate(time.Hour)) || !to.Equal(to.Truncate(time.Hour)) {
		bad("has an audit range that is not a span of whole hours")
	}
	if len(m.Files) > maxEntries {
		bad("lists more than %d files", maxEntries)
		return false
	}
	seen := map[string]bool{}
	var total int64
	for _, e := range m.Files {
		switch {
		case !safeName(e.Path) || e.Path == ManifestFile || e.Path == SignatureFile:
			bad("lists a file name that is not allowed")
		case seen[e.Path]:
			bad("lists %s twice", shown(e.Path))
		case !sha256Hex.MatchString(e.SHA256) || e.Size < 0 || e.Volume < 1 || e.Volume > m.Volumes:
			bad("lists %s with an invalid hash, size or volume", shown(e.Path))
		}
		seen[e.Path] = true
		if e.Size > maxBytes || total > maxBytes-e.Size {
			bad("lists more than %d bytes of files", maxBytes)
			return false
		}
		total += e.Size
	}
	for _, need := range []string{firstAuditPart, checkpointsFile, verificationFile, keysFile} {
		if !seen[need] {
			bad("does not list %s", need)
		}
	}
	return ok
}

// assignVolumes works out which archive is which volume from the files the
// manifest places in each, and reports a volume that is missing, doubled or
// mixed. It returns the archive index of every volume that was found.
func assignVolumes(res *Result, m *Manifest, vols []*volume, owner map[string]int, mIdx, sIdx, given int) map[int]int {
	if given != m.Volumes {
		res.fail("", "%d archives were given and the manifest says the bundle has %d volumes", given, m.Volumes)
	}
	archiveVolume := map[int]int{}
	mixed := map[int]bool{}
	for _, e := range m.Files {
		i, present := owner[e.Path]
		if !present {
			continue
		}
		if prev, seen := archiveVolume[i]; seen && prev != e.Volume && !mixed[i] {
			mixed[i] = true
			res.fail(vols[i].label, "holds files of volume %d and of volume %d", prev, e.Volume)
		} else if !seen {
			archiveVolume[i] = e.Volume
		}
	}
	assign := map[int]int{}
	for i := range vols {
		v, ok := archiveVolume[i]
		if !ok {
			continue
		}
		if other, dup := assign[v]; dup {
			res.fail("", "volume %d is in two archives (%s and %s)", v, vols[other].label, vols[i].label)
			continue
		}
		assign[v] = i
	}
	for v := 1; v <= m.Volumes; v++ {
		if _, ok := assign[v]; !ok {
			res.fail("", "volume %d of %d was not given", v, m.Volumes)
		}
	}
	for _, name := range []string{ManifestFile, SignatureFile} {
		idx := mIdx
		if name == SignatureFile {
			idx = sIdx
		}
		if want, ok := assign[m.Volumes]; ok && want != idx {
			res.fail(name, "is not in the last volume")
		}
	}
	return assign
}

// checkListedFiles hashes every file the manifest lists, in volume order, and
// feeds the audit files to the audit check.
func checkListedFiles(res *Result, m *Manifest, vols []*volume, owner map[string]int, assign map[int]int, keys Keys, opts Options) {
	byPath := map[string]FileEntry{}
	for _, e := range m.Files {
		byPath[e.Path] = e
	}
	audit := newAuditCheck(res, m, keys.Audit)
	var bundledKeys, checkpoints, stated []byte
	small := map[string]*[]byte{keysFile: &bundledKeys, checkpointsFile: &checkpoints, verificationFile: &stated}

	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return partOrder(paths[i]) < partOrder(paths[j]) })

	for _, p := range paths {
		e := byPath[p]
		i, present := owner[p]
		if !present {
			if _, volumeGiven := assign[e.Volume]; volumeGiven {
				res.fail(p, "is listed in the manifest but missing from the archives")
			}
			continue
		}
		if want, ok := assign[e.Volume]; ok && want != i {
			res.fail(p, "is in %s, not in volume %d", vols[i].label, e.Volume)
			continue
		}
		res.Files++
		var sink func(io.Reader) error
		switch dst, isSmall := small[p]; {
		case isSmall:
			if e.Size > maxSmallFile {
				res.fail(p, "is too large to check")
				continue
			}
			sink = func(r io.Reader) error {
				data, err := io.ReadAll(r)
				*dst = data
				return err
			}
		case auditPart.MatchString(p):
			name := p
			sink = func(r io.Reader) error { return audit.readPart(name, r) }
		}
		checkEntry(res, vols[i].files[p], e, sink)
	}
	audit.finish(bundledKeys, checkpoints, stated, opts.RequireCheckpoints)
}

// partOrder sorts audit parts by number and everything else by name.
func partOrder(p string) string {
	if g := auditPart.FindStringSubmatch(p); g != nil {
		return "audit/events.part-" + strings.Repeat("0", 9-len(g[1])) + g[1]
	}
	return p
}

// checkEntry streams one file through a hash, refusing one that is larger than
// the manifest says, and gives the same bytes to sink when there is one.
func checkEntry(res *Result, f *zip.File, e FileEntry, sink func(io.Reader) error) {
	if f.UncompressedSize64 != uint64(e.Size) {
		res.fail(e.Path, "declares %d bytes in its archive; the manifest says %d", f.UncompressedSize64, e.Size)
		return
	}
	rc, err := f.Open()
	if err != nil {
		res.fail(e.Path, "cannot be read from its archive")
		return
	}
	defer rc.Close()
	sum := &countingHash{Hash: sha256.New()}
	tee := io.TeeReader(io.LimitReader(rc, e.Size+1), sum)
	if sink != nil {
		if err := sink(tee); err != nil {
			res.fail(e.Path, "cannot be read: %v", err)
		}
	}
	// Whatever the sink left unread still goes through the hash.
	switch _, err = io.Copy(io.Discard, tee); {
	case errors.Is(err, zip.ErrChecksum):
		res.fail(e.Path, "fails its archive's own checksum")
	case err != nil:
		res.fail(e.Path, "cannot be read from its archive")
	case sum.n != e.Size:
		res.fail(e.Path, "has more or fewer bytes than the %d the manifest lists", e.Size)
	case hex.EncodeToString(sum.Sum(nil)) != e.SHA256:
		res.fail(e.Path, "does not match the hash in the manifest")
	}
}

// countingHash counts the bytes it hashes.
type countingHash struct {
	hash.Hash
	n int64
}

func (c *countingHash) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.Hash.Write(p)
}
