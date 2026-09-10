package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/correlation"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/geology"
)

func validState() State {
	now := time.Date(2026, 9, 10, 11, 0, 0, 0, time.UTC)
	id := "prf_00000000000000000000000000000001"
	p := geology.Profile{
		ID:        id,
		Metadata:  geology.Metadata{Name: "测试剖面", Site: "测试地点", DepthMM: 10000},
		Layers:    []geology.Layer{{TopMM: 0, BottomMM: 4000, Rock: geology.Sandstone}},
		State:     geology.Draft,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s := emptyState()
	s.Histories[id] = []geology.Revision{{
		Profile: p,
		Event:   geology.Event{Action: "create", Reason: "新建剖面", Version: 1, At: now},
	}}
	return s
}

func seedSnapshot(t *testing.T, dir string, state State) {
	t.Helper()
	if err := writeSnapshotFile(filepath.Join(dir, "strata.json"), state); err != nil {
		t.Fatalf("写入种子快照失败: %v", err)
	}
}

func writeSnapshotFile(path string, state State) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	contents, err := json.Marshal(envelope{Digest: hex.EncodeToString(sum[:]), Data: raw})
	if err != nil {
		return err
	}
	return os.WriteFile(path, contents, 0600)
}

// 关闭后重开：快照内容完整恢复。
func TestOpenRestoresSnapshot(t *testing.T) {
	dir := t.TempDir()
	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	ctx := context.Background()
	state := validState()
	if err = repo.Update(ctx, func(s *State) (bool, error) {
		*s = state.Clone()
		return true, nil
	}); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
	if err = repo.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("从快照恢复失败: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.state.Latest("prf_00000000000000000000000000000001")
	if err != nil {
		t.Fatalf("恢复后读取剖面失败: %v", err)
	}
	if got.Version != 1 || got.Name != "测试剖面" || len(got.Layers) != 1 {
		t.Fatalf("恢复内容与写入不一致: %+v", got)
	}
}

// 损坏的字节（校验和不再匹配）必须拒绝启动。
func TestOpenRejectsCorruptedSnapshot(t *testing.T) {
	dir := t.TempDir()
	seedSnapshot(t, dir, validState())
	path := filepath.Join(dir, "strata.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	idx := strings.Index(string(raw), "sandstone")
	if idx < 0 {
		t.Fatalf("种子数据中未找到可篡改的岩性字段")
	}
	raw[idx] = 'x'
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir); err == nil {
		t.Fatalf("字节被篡改且校验和失配的快照必须拒绝启动")
	} else if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("应报告校验和错误，实际: %v", err)
	}
}

// 校验和正确但语义非法（锁定版本存在缺口）仍必须拒绝启动。
func TestOpenRejectsSemanticallyInvalidSnapshot(t *testing.T) {
	dir := t.TempDir()
	seedSnapshot(t, dir, validState())
	path := filepath.Join(dir, "strata.json")

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err = json.Unmarshal(contents, &env); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal(env.Data, &data); err != nil {
		t.Fatal(err)
	}
	histories := data["histories"].(map[string]any)
	for _, entry := range histories {
		revisions := entry.([]any)
		profile := revisions[0].(map[string]any)["profile"].(map[string]any)
		profile["state"] = "sealed" // 单层 [0,4000)，距 10000 有缺口
	}
	env.Data, err = json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(env.Data)
	env.Digest = hex.EncodeToString(sum[:])
	if contents, err = json.Marshal(env); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, contents, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err = Open(dir); err == nil {
		t.Fatalf("校验通过但语义非法的快照必须拒绝启动")
	} else if !strings.Contains(err.Error(), "snapshot validation") {
		t.Fatalf("应报告快照校验失败，实际: %v", err)
	}
}

// 单进程独占：已打开的数据目录不能被第二个仓库再次打开；关闭后可以。
func TestOpenDirectoryLockExclusive(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	second, err := Open(dir)
	if err == nil {
		second.Close()
		t.Fatalf("数据目录已被占用时第二次打开必须失败")
	}
	if err = first.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	third, err := Open(dir)
	if err != nil {
		t.Fatalf("释放锁后应能再次打开: %v", err)
	}
	defer third.Close()
}

// 遗留的 .strata-* 临时文件不参与恢复。
func TestOpenIgnoresTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	seedSnapshot(t, dir, validState())
	if err := os.WriteFile(filepath.Join(dir, ".strata-garbage"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	repo, err := Open(dir)
	if err != nil {
		t.Fatalf("临时文件不得影响快照恢复: %v", err)
	}
	defer repo.Close()
}

// 构造 create -> layers -> seal 的自洽三版本历史，返回剖面编号。
func sealedHistory(state State, id, name string, rock geology.Lithology, base time.Time) {
	create := geology.Profile{
		ID:        id,
		Metadata:  geology.Metadata{Name: name, Site: "测试地点", DepthMM: 10000},
		Layers:    []geology.Layer{},
		State:     geology.Draft,
		Version:   1,
		CreatedAt: base,
		UpdatedAt: base,
	}
	full := create.Clone()
	full.Layers = []geology.Layer{{TopMM: 0, BottomMM: 10000, Rock: rock}}
	full.Version = 2
	full.UpdatedAt = base.Add(time.Minute)
	sealed := full.Clone()
	sealed.State = geology.Sealed
	sealed.Version = 3
	sealed.UpdatedAt = base.Add(2 * time.Minute)
	state.Histories[id] = []geology.Revision{
		{Profile: create, Event: geology.Event{Action: "create", Reason: "新建", Version: 1, At: base}},
		{Profile: full, Event: geology.Event{Action: "layers", Reason: "完整分层", Version: 2, At: full.UpdatedAt}},
		{Profile: sealed, Event: geology.Event{Action: "seal", Reason: "锁定", Version: 3, At: sealed.UpdatedAt}},
	}
}

// 启动时必须重算对比结果：篡改对比内容并修正校验和后仍拒绝启动。
func TestOpenRejectsTamperedComparison(t *testing.T) {
	dir := t.TempDir()
	seed := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	state := emptyState()
	firstID := "prf_00000000000000000000000000000001"
	secondID := "prf_00000000000000000000000000000002"
	sealedHistory(state, firstID, "剖面一", geology.Sandstone, seed)
	sealedHistory(state, secondID, "剖面二", geology.Sandstone, seed)

	left, _ := state.Latest(firstID)
	right, _ := state.Latest(secondID)
	request := correlation.Request{
		Left:  correlation.Reference{ID: firstID, Version: 3},
		Right: correlation.Reference{ID: secondID, Version: 3},
	}
	result, err := correlation.Align(left, right, request, seed.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("构造对比结果失败: %v", err)
	}
	state.Comparisons[result.ID] = result
	if err = state.Validate(); err != nil {
		t.Fatalf("前置状态应自洽: %v", err)
	}
	seedSnapshot(t, dir, state)

	// 篡改结果的累计厚度并重新计算校验和：磁盘字节“完好”，语义与重算值失配。
	contents, err := os.ReadFile(filepath.Join(dir, "strata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err = json.Unmarshal(contents, &env); err != nil {
		t.Fatal(err)
	}
	var loaded State
	if err = json.Unmarshal(env.Data, &loaded); err != nil {
		t.Fatal(err)
	}
	tampered := loaded.Comparisons[result.ID]
	tampered.EqualMM += 1
	loaded.Comparisons[result.ID] = tampered
	env.Data, err = json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(env.Data)
	env.Digest = hex.EncodeToString(sum[:])
	contents, err = json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "strata.json"), contents, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir); err == nil {
		t.Fatalf("对比结果与可重算值不一致时必须拒绝启动")
	}
}
