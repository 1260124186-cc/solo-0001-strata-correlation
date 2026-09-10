package persistence

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

// Backup files are self-describing envelopes written only inside the
// configured backup directory. Requests reference backups by the generated
// identifier, never by a server path.
const backupFormat = "strata-backup-v1"

type BackupSummary struct {
	Profiles    int    `json:"profiles"`
	Revisions   int    `json:"revisions"`
	Comparisons int    `json:"comparisons"`
	SHA256      string `json:"sha256"`
}

type BackupMeta struct {
	ID        string        `json:"id"`
	CreatedAt time.Time     `json:"created_at"`
	Note      string        `json:"note,omitempty"`
	SizeBytes int64         `json:"size_bytes"`
	Summary   BackupSummary `json:"summary"`
}

type BackupListItem struct {
	ID        string        `json:"id"`
	CreatedAt time.Time     `json:"created_at"`
	Note      string        `json:"note,omitempty"`
	SizeBytes int64         `json:"size_bytes"`
	Valid     bool          `json:"valid"`
	Summary   BackupSummary `json:"summary"`
}

type BackupChecks struct {
	DigestOK  bool `json:"digest_ok"`
	StateOK   bool `json:"state_ok"`
	SummaryOK bool `json:"summary_ok"`
}

type BackupDetail struct {
	ID        string        `json:"id"`
	CreatedAt time.Time     `json:"created_at"`
	Note      string        `json:"note,omitempty"`
	SizeBytes int64         `json:"size_bytes"`
	Format    string        `json:"format,omitempty"`
	Summary   BackupSummary `json:"summary"`
	Checks    BackupChecks  `json:"checks"`
	Valid     bool          `json:"valid"`
	Detail    string        `json:"detail,omitempty"`
}

type backupEnvelope struct {
	Format    string          `json:"format"`
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"created_at"`
	Note      string          `json:"note,omitempty"`
	Digest    string          `json:"digest"`
	Summary   BackupSummary   `json:"summary"`
	Data      json.RawMessage `json:"data"`
}

type BackupStore struct{ dir string }

func OpenBackups(dir string) (*BackupStore, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(absolute, 0700); err != nil {
		return nil, err
	}
	return &BackupStore{dir: absolute}, nil
}

// ValidBackupID accepts only the generated identifier shape. The character
// set excludes path separators and dots so an identifier can never escape
// the configured backup directory.
func ValidBackupID(id string) bool {
	if !strings.HasPrefix(id, "bak_") || len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id[len("bak_"):] {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func (s *BackupStore) path(id string) (string, error) {
	if !ValidBackupID(id) {
		return "", geology.Invalid("id", "备份编号无效")
	}
	path := filepath.Join(s.dir, id+".json")
	if filepath.Dir(path) != s.dir {
		return "", geology.Invalid("id", "备份编号无效")
	}
	return path, nil
}

func newBackupID(now time.Time) (string, error) {
	bytes := make([]byte, 4)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "bak_" + now.Format("20060102T150405Z") + "_" + hex.EncodeToString(bytes), nil
}

func summarize(state State, digest string) BackupSummary {
	revisions := 0
	for _, history := range state.Histories {
		revisions += len(history)
	}
	return BackupSummary{Profiles: len(state.Histories), Revisions: revisions, Comparisons: len(state.Comparisons), SHA256: digest}
}

// Create writes a point-in-time backup of the given consistent state. The
// state is cloned under the repository read lock by the caller, so the
// backup always reflects a single moment.
func (s *BackupStore) Create(state State, note string) (BackupMeta, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return BackupMeta{}, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	now := time.Now().UTC()
	summary := summarize(state, digest)
	var id string
	for attempts := 0; attempts < 5; attempts++ {
		candidate, err := newBackupID(now)
		if err != nil {
			return BackupMeta{}, err
		}
		if _, err = os.Stat(filepath.Join(s.dir, candidate+".json")); os.IsNotExist(err) {
			id = candidate
			break
		} else if err != nil {
			return BackupMeta{}, err
		}
	}
	if id == "" {
		return BackupMeta{}, fmt.Errorf("无法生成唯一备份编号")
	}
	contents, err := json.Marshal(backupEnvelope{Format: backupFormat, ID: id, CreatedAt: now, Note: note, Digest: digest, Summary: summary, Data: raw})
	if err != nil {
		return BackupMeta{}, err
	}
	if len(contents) > maxSnapshot {
		return BackupMeta{}, fmt.Errorf("备份文件超过 64 MiB 上限")
	}
	committed, err := writeFileAtomic(s.dir, id+".json", contents)
	if err != nil {
		if committed {
			os.Remove(filepath.Join(s.dir, id+".json"))
		}
		return BackupMeta{}, fmt.Errorf("写入备份文件: %w", err)
	}
	return BackupMeta{ID: id, CreatedAt: now, Note: note, SizeBytes: int64(len(contents)), Summary: summary}, nil
}

type backupRead struct {
	meta    BackupMeta
	state   State
	format  string
	checks  BackupChecks
	failure string
}

// read parses a backup file and always verifies the checksum digest. With
// deep set it also validates the embedded state and the stored summary.
// Corruption is reported through failure, not returned as an error.
func (s *BackupStore) read(id string, deep bool) (backupRead, error) {
	var out backupRead
	path, err := s.path(id)
	if err != nil {
		return out, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return out, geology.Missing("备份不存在")
	}
	if err != nil {
		return out, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxSnapshot+1))
	if err != nil {
		return out, err
	}
	out.meta = BackupMeta{ID: id, CreatedAt: info.ModTime().UTC(), SizeBytes: info.Size()}
	if len(raw) > maxSnapshot {
		out.failure = "备份文件超过 64 MiB 上限"
		return out, nil
	}
	var env backupEnvelope
	if err = json.Unmarshal(raw, &env); err != nil {
		out.failure = "备份文件不是有效的 JSON"
		return out, nil
	}
	out.format = env.Format
	out.meta.CreatedAt = env.CreatedAt
	out.meta.Note = env.Note
	out.meta.Summary = env.Summary
	if env.Format != backupFormat {
		out.failure = "未知备份格式"
		return out, nil
	}
	if env.ID != id {
		out.failure = "备份编号与文件内容不一致"
		return out, nil
	}
	sum := sha256.Sum256(env.Data)
	out.checks.DigestOK = env.Digest == hex.EncodeToString(sum[:])
	if !out.checks.DigestOK {
		out.failure = "校验摘要不匹配"
		return out, nil
	}
	if !deep {
		return out, nil
	}
	var state State
	if err = json.Unmarshal(env.Data, &state); err != nil {
		out.failure = "备份内容无法解析"
		return out, nil
	}
	if err = validateBackupState(state); err != nil {
		out.failure = "备份内容无效: " + err.Error()
		return out, nil
	}
	out.checks.StateOK = true
	out.checks.SummaryOK = env.Summary == summarize(state, env.Digest)
	if !out.checks.SummaryOK {
		out.failure = "备份摘要与内容不一致"
		return out, nil
	}
	out.state = state
	return out, nil
}

func validateBackupState(state State) error {
	if len(state.Histories) > 2000 {
		return fmt.Errorf("剖面数量超过 2000 上限")
	}
	if len(state.Comparisons) > 10000 {
		return fmt.Errorf("对比结果数量超过 10000 上限")
	}
	for _, history := range state.Histories {
		if len(history) > 500 {
			return fmt.Errorf("单个剖面版本数超过 500 上限")
		}
	}
	return state.Validate()
}

// List returns every backup file in the configured directory, including
// damaged ones marked invalid. Listing verifies the checksum digest only;
// Inspect performs the full content validation.
func (s *BackupStore) List() ([]BackupListItem, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	items := []BackupListItem{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "bak_") || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !ValidBackupID(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		item := BackupListItem{ID: id, CreatedAt: info.ModTime().UTC(), SizeBytes: info.Size()}
		result, err := s.read(id, false)
		if err != nil {
			var problem *geology.Problem
			if errors.As(err, &problem) && problem.Code == "missing" {
				continue
			}
			return nil, err
		}
		if result.failure == "" {
			item.Valid = true
			item.CreatedAt = result.meta.CreatedAt
			item.Note = result.meta.Note
			item.Summary = result.meta.Summary
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

// Inspect fully verifies a backup and reports each check without failing on
// damaged files.
func (s *BackupStore) Inspect(id string) (BackupDetail, error) {
	result, err := s.read(id, true)
	if err != nil {
		return BackupDetail{}, err
	}
	detail := BackupDetail{
		ID:        result.meta.ID,
		CreatedAt: result.meta.CreatedAt,
		Note:      result.meta.Note,
		SizeBytes: result.meta.SizeBytes,
		Format:    result.format,
		Summary:   result.meta.Summary,
		Checks:    result.checks,
		Detail:    result.failure,
	}
	detail.Valid = result.failure == "" && result.checks.DigestOK && result.checks.StateOK && result.checks.SummaryOK
	return detail, nil
}

// Load returns a fully validated state ready for restore or preview. Damaged
// backups are rejected with a conflict so the current data stays untouched.
func (s *BackupStore) Load(id string) (State, BackupMeta, error) {
	result, err := s.read(id, true)
	if err != nil {
		return State{}, BackupMeta{}, err
	}
	if result.failure != "" {
		return State{}, BackupMeta{}, geology.Conflict("备份不可用: " + result.failure)
	}
	return result.state, result.meta, nil
}
