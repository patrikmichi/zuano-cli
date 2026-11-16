package evidence

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/patrikmichi/zuano-cli/internal/signing"
)

const checkpointPurpose = "audit-checkpoint"

type auditLeaf struct {
	id   int64
	leaf [sha256.Size]byte
}

type checkpoint struct {
	OrgID     string
	HourStart string
	RowCount  int64
	FirstID   int64
	LastID    int64
	Root      string
	KID       string
	Signature string
}

// auditCheck recomputes the Merkle root of every audit hour from the exported
// rows and compares it with the signed checkpoint the bundle carries for it.
type auditCheck struct {
	res        *Result
	orgID      string
	from, to   time.Time
	userKeys   []signing.Key
	byHour    map[time.Time][]auditLeaf
	lastID    int64
	partsSeen []int
}

func newAuditCheck(res *Result, m *Manifest, userKeys []signing.Key) *auditCheck {
	from, _ := time.Parse(time.RFC3339Nano, m.AuditRange.From)
	to, _ := time.Parse(time.RFC3339Nano, m.AuditRange.To)
	return &auditCheck{res: res, orgID: m.Org.ID, from: from.UTC(), to: to.UTC(), userKeys: userKeys, byHour: map[time.Time][]auditLeaf{}}
}

// readPart takes the rows of one audit/events.part-NNN.jsonl.
func (a *auditCheck) readPart(name string, r io.Reader) error {
	if g := auditPart.FindStringSubmatch(name); g != nil {
		n, _ := strconv.Atoi(g[1])
		a.partsSeen = append(a.partsSeen, n)
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		if msg := a.addRow(sc.Bytes()); msg != "" {
			a.res.fail(name, "line %d: %s", line, msg)
		}
	}
	return sc.Err()
}

// addRow records one row's leaf and returns what is wrong with it, if anything.
func (a *auditCheck) addRow(line []byte) string {
	v, err := parseJSON(line)
	row, ok := v.(map[string]any)
	if err != nil || !ok {
		return "is not a JSON object"
	}
	num, _ := row["id"].(json.Number)
	id, err := strconv.ParseInt(num.String(), 10, 64)
	if err != nil || id < 1 {
		return "has no valid id"
	}
	ts, _ := row["ts"].(string)
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return "has no valid timestamp"
	}
	at = at.UTC()
	if org, present := row["orgId"]; present && org != a.orgID {
		return "belongs to another organization"
	}
	if at.Before(a.from) || !at.Before(a.to) {
		return "is outside the audit range the manifest states"
	}
	if id <= a.lastID {
		return "is not in ascending id order"
	}
	a.lastID = id
	fields := make(map[string]any, len(row))
	for k, val := range row {
		if val != nil {
			fields[k] = val
		}
	}
	canon, err := signing.CanonicalJSON(fields)
	if err != nil {
		return "cannot be put in canonical form"
	}
	hour := at.Truncate(time.Hour)
	a.byHour[hour] = append(a.byHour[hour], auditLeaf{id: id, leaf: sha256.Sum256(append([]byte{0}, canon...))})
	a.res.Events++
	return ""
}

// finish compares the rows read with the checkpoints and the stated verification.
func (a *auditCheck) finish(bundledKeys, checkpointBytes, stated []byte, requireCheckpoints bool) {
	a.checkParts()
	keys, ok := a.auditKeys(bundledKeys)
	stored := a.readCheckpoints(checkpointBytes)
	if !ok {
		return
	}

	type hourFailure struct{ hour, reason string }
	var failures []hourFailure
	unsigned := []string{}
	verified := 0
	hours := make([]time.Time, 0, len(a.byHour))
	for h := range a.byHour {
		hours = append(hours, h)
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Before(hours[j]) })
	for _, hour := range hours {
		hs := hour.Format(hourTimestampForm)
		cp, has := stored[hour]
		if !has {
			unsigned = append(unsigned, hs)
			continue
		}
		list := a.byHour[hour]
		leaves := make([][sha256.Size]byte, len(list))
		for i, l := range list {
			leaves[i] = l.leaf
		}
		same := int64(len(list)) == cp.RowCount && list[0].id == cp.FirstID && list[len(list)-1].id == cp.LastID && hex.EncodeToString(merkleRoot(leaves)) == cp.Root
		if !same {
			reason := "rows_changed"
			if int64(len(list)) < cp.RowCount {
				reason = "rows_missing"
			}
			failures = append(failures, hourFailure{hs, reason})
			continue
		}
		if reason := checkpointSignature(cp, keys); reason != "" {
			failures = append(failures, hourFailure{hs, reason})
			continue
		}
		verified++
	}
	storedHours := make([]time.Time, 0, len(stored))
	for h := range stored {
		storedHours = append(storedHours, h)
	}
	sort.Slice(storedHours, func(i, j int) bool { return storedHours[i].Before(storedHours[j]) })
	for _, hour := range storedHours {
		if _, has := a.byHour[hour]; !has {
			failures = append(failures, hourFailure{hour.Format(hourTimestampForm), "checkpoint_without_rows"})
		}
	}

	allHours := map[time.Time]bool{}
	for h := range a.byHour {
		allHours[h] = true
	}
	for h := range stored {
		allHours[h] = true
	}
	a.res.Hours, a.res.VerifiedHours, a.res.UnsignedHours = len(allHours), verified, unsigned
	for _, f := range failures {
		a.res.fail(checkpointsFile, "audit hour %s: %s", f.hour, describeHourFailure(f.reason))
	}
	if len(unsigned) > 0 && requireCheckpoints {
		a.res.fail(checkpointsFile, "%d audit hours have rows and no signed checkpoint, so they are not verified", len(unsigned))
	}
	a.checkStated(stated, len(allHours), verified, unsigned, func() []any {
		out := make([]any, len(failures))
		for i, f := range failures {
			out[i] = map[string]any{"hourStart": f.hour, "reason": f.reason}
		}
		return out
	}())
}

func describeHourFailure(reason string) string {
	switch reason {
	case "rows_changed":
		return "the rows differ from the signed checkpoint"
	case "rows_missing":
		return "rows are missing against the signed checkpoint"
	case "unknown_key":
		return "the checkpoint is signed with a key the bundle does not list"
	case "signature":
		return "the checkpoint signature does not verify"
	}
	return "there is a checkpoint and no rows"
}

// checkParts requires the audit parts to be numbered 1 to N without a gap.
func (a *auditCheck) checkParts() {
	slices.Sort(a.partsSeen)
	for i, n := range a.partsSeen {
		if n != i+1 {
			a.res.fail("audit/", "the audit parts are not numbered from 1 without a gap")
			return
		}
	}
}

// auditKeys returns the keys that check checkpoint signatures: the caller's,
// or the ones in the bundle's key file.
func (a *auditCheck) auditKeys(bundled []byte) ([]signing.Key, bool) {
	if len(a.userKeys) > 0 {
		return a.userKeys, true
	}
	v, err := parseJSON(bundled)
	if _, isObj := v.(map[string]any); err != nil || !isObj {
		a.res.fail(keysFile, "is not a key document")
		return nil, false
	}
	var doc struct {
		Audit []signing.Key `json:"audit"`
	}
	if err := json.Unmarshal(bundled, &doc); err != nil || len(doc.Audit) == 0 {
		a.res.fail(keysFile, "lists no audit keys")
		return nil, false
	}
	seen := map[string]bool{}
	for _, k := range doc.Audit {
		if k.KID == "" || seen[k.KID] || k.Alg != "ed25519" {
			a.res.fail(keysFile, "lists an audit key with no id, a repeated id or an algorithm other than ed25519")
			return nil, false
		}
		seen[k.KID] = true
	}
	return doc.Audit, true
}

// readCheckpoints parses audit/checkpoints.jsonl into checkpoints by hour.
func (a *auditCheck) readCheckpoints(raw []byte) map[time.Time]checkpoint {
	out := map[time.Time]checkpoint{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		cp, hour, msg := a.parseCheckpoint(sc.Bytes())
		if msg == "" {
			if _, dup := out[hour]; dup {
				msg = "repeats an hour"
			}
		}
		if msg != "" {
			a.res.fail(checkpointsFile, "line %d: %s", line, msg)
			continue
		}
		out[hour] = cp
	}
	if sc.Err() != nil {
		a.res.fail(checkpointsFile, "cannot be read line by line")
	}
	return out
}

func (a *auditCheck) parseCheckpoint(line []byte) (checkpoint, time.Time, string) {
	v, err := parseJSON(line)
	obj, ok := v.(map[string]any)
	if err != nil || !ok {
		return checkpoint{}, time.Time{}, "is not a JSON object"
	}
	str := func(k string) string { s, _ := obj[k].(string); return s }
	integer := func(k string) (int64, bool) {
		n, _ := obj[k].(json.Number)
		i, err := strconv.ParseInt(n.String(), 10, 64)
		return i, err == nil
	}
	cp := checkpoint{OrgID: str("orgId"), HourStart: str("hourStart"), Root: str("root"), KID: str("kid"), Signature: str("signature")}
	var okCount, okFirst, okLast bool
	cp.RowCount, okCount = integer("rowCount")
	cp.FirstID, okFirst = integer("firstId")
	cp.LastID, okLast = integer("lastId")
	if !okCount || !okFirst || !okLast || cp.RowCount < 1 || cp.FirstID < 1 || cp.LastID < cp.FirstID || cp.KID == "" || !sha256Hex.MatchString(cp.Root) {
		return cp, time.Time{}, "is not a checkpoint"
	}
	at, err := time.Parse(time.RFC3339Nano, cp.HourStart)
	if err != nil {
		return cp, time.Time{}, "has no valid hour"
	}
	hour := at.UTC()
	switch {
	case !hour.Equal(hour.Truncate(time.Hour)):
		return cp, time.Time{}, "is for a time that is not the start of an hour"
	case hour.Before(a.from) || !hour.Before(a.to):
		return cp, time.Time{}, "is for an hour outside the audit range the manifest states"
	case cp.OrgID != a.orgID:
		return cp, time.Time{}, "belongs to another organization"
	}
	return cp, hour, ""
}

// checkpointSignature returns "" when a listed key signed the checkpoint, and
// the gateway's reason otherwise.
func checkpointSignature(cp checkpoint, keys []signing.Key) string {
	if !slices.ContainsFunc(keys, func(k signing.Key) bool { return k.KID == cp.KID }) {
		return "unknown_key"
	}
	payload, err := signing.CanonicalJSON(map[string]any{
		"purpose": checkpointPurpose, "orgId": cp.OrgID, "hourStart": cp.HourStart,
		"rowCount": json.Number(strconv.FormatInt(cp.RowCount, 10)), "firstId": json.Number(strconv.FormatInt(cp.FirstID, 10)),
		"lastId": json.Number(strconv.FormatInt(cp.LastID, 10)), "root": cp.Root,
	})
	sig, decodeErr := base64.StdEncoding.DecodeString(cp.Signature)
	if err != nil || decodeErr != nil || signing.VerifyMessage(keys, cp.KID, payload, sig) != nil {
		return "signature"
	}
	return ""
}

// checkStated compares audit/verification.json with the verification just
// recomputed from the rows.
func (a *auditCheck) checkStated(stated []byte, hours, verified int, unsigned []string, failures []any) {
	listed := make([]any, len(unsigned))
	for i, u := range unsigned {
		listed[i] = u
	}
	mine, err := signing.CanonicalJSON(map[string]any{"hours": hours, "verified": verified, "unsigned": listed, "failures": failures, "ok": len(failures) == 0})
	theirs, perr := parseJSON(stated)
	var theirsCanon []byte
	if perr == nil {
		theirsCanon, perr = signing.CanonicalJSON(theirs)
	}
	if err != nil || perr != nil || !bytes.Equal(mine, theirsCanon) {
		a.res.fail(verificationFile, "differs from the verification recomputed from the audit rows")
	}
}

// merkleRoot is the gateway's rule: pairs are hashed upward as
// SHA-256(0x01 || left || right) and an odd node is carried up unchanged.
func merkleRoot(leaves [][sha256.Size]byte) []byte {
	if len(leaves) == 0 {
		sum := sha256.Sum256(nil)
		return sum[:]
	}
	level := slices.Clone(leaves)
	for len(level) > 1 {
		next := make([][sha256.Size]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				next = append(next, level[i])
				continue
			}
			h := sha256.New()
			h.Write([]byte{1})
			h.Write(level[i][:])
			h.Write(level[i+1][:])
			var node [sha256.Size]byte
			copy(node[:], h.Sum(nil))
			next = append(next, node)
		}
		level = next
	}
	return level[0][:]
}
