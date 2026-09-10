package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/api"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/catalog"
	"github.com/1260124186-cc/solo-0001-strata-correlation/internal/persistence"
)

type harness struct {
	t        *testing.T
	server   *httptest.Server
	svc      *catalog.Service
	repo     *persistence.Repository
	repoPath string
}

func newHarness(t *testing.T, dir string) *harness {
	t.Helper()
	repo, err := persistence.Open(dir)
	if err != nil {
		t.Fatalf("打开数据目录失败: %v", err)
	}
	t.Cleanup(func() {
		repo.Close()
	})
	svc := catalog.New(repo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(api.New(svc, logger))
	t.Cleanup(server.Close)
	return &harness{t: t, server: server, svc: svc, repo: repo, repoPath: dir}
}

// startAgain 释放旧仓库（模拟进程退出），在同一数据目录上启动新服务。
func (h *harness) startAgain() {
	h.t.Helper()
	h.server.Close()
	if err := h.repo.Close(); err != nil {
		h.t.Fatalf("关闭旧仓库失败: %v", err)
	}
	repo, err := persistence.Open(h.repoPath)
	if err != nil {
		h.t.Fatalf("重启打开数据目录失败: %v", err)
	}
	h.repo = repo
	h.svc = catalog.New(repo)
	h.server = httptest.NewServer(api.New(h.svc, slog.New(slog.NewTextHandler(io.Discard, nil))))
	h.t.Cleanup(func() { h.server.Close(); h.repo.Close() })
}

func (h *harness) do(method, path string, body any) (*http.Response, map[string]any) {
	res, raw := h.doRaw(method, path, body)
	decoded := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			h.t.Fatalf("响应不是 JSON 对象: %v: %s", err, raw)
		}
	}
	return res, decoded
}

func (h *harness) doRaw(method, path string, body any) (*http.Response, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("HTTP 请求失败: %v", err)
	}
	h.t.Cleanup(func() { res.Body.Close() })
	raw, _ := io.ReadAll(res.Body)
	return res, raw
}

func (h *harness) doGet(method, path string) (*http.Response, []byte) {
	return h.doRaw(method, path, nil)
}

func (h *harness) createProfile(name string, depth int64) map[string]any {
	h.t.Helper()
	res, body := h.do("POST", "/api/v1/profiles", map[string]any{
		"name": name, "site": "赤石岭", "depth_mm": depth,
	})
	if res.StatusCode != http.StatusCreated {
		h.t.Fatalf("创建剖面期望 201，实际 %d: %v", res.StatusCode, body)
	}
	return body
}

func profileID(body map[string]any) string   { return body["id"].(string) }
func profileVersion(body map[string]any) int { return int(body["version"].(float64)) }

// 全链路：编录 → 缺口拒绝锁定 → 补全 → 锁定 → 重开 → 历史可读。
func TestProfileLifecycleHTTP(t *testing.T) {
	h := newHarness(t, t.TempDir())
	p := h.createProfile("赤石北坡", 10000)
	id := profileID(p)

	// 部分覆盖：coverage 暴露缺口并判定不可锁定。
	res, body := h.do("PUT", "/api/v1/profiles/"+id+"/layers", map[string]any{
		"expected_version": 1, "reason": "初编",
		"layers": []map[string]any{
			{"top_mm": 0, "bottom_mm": 4000, "rock": "sandstone"},
		},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("替换分层期望 200，实际 %d: %v", res.StatusCode, body)
	}

	res, coverage := h.do("GET", "/api/v1/profiles/"+id+"/coverage", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("coverage 期望 200，实际 %d", res.StatusCode)
	}
	cov := coverage["coverage"].(map[string]any)
	if cov["ready"].(bool) {
		t.Fatalf("存在缺口时 ready 必须为 false")
	}
	gaps := cov["gaps"].([]any)
	if len(gaps) != 1 || gaps[0].(map[string]any)["bottom_mm"].(float64) != 10000 {
		t.Fatalf("缺口应为 [4000,10000): %v", gaps)
	}

	// 锁定失败：HTTP 409，原版本保持可编辑。
	res, body = h.do("POST", "/api/v1/profiles/"+id+"/seal", map[string]any{
		"expected_version": 2, "reason": "提前锁定",
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("缺口锁定期望 409，实际 %d: %v", res.StatusCode, body)
	}

	// 补全 [0, depth) 后锁定。
	res, body = h.do("PUT", "/api/v1/profiles/"+id+"/layers", map[string]any{
		"expected_version": 2, "reason": "补全覆盖",
		"layers": []map[string]any{
			{"top_mm": 0, "bottom_mm": 4000, "rock": "sandstone"},
			{"top_mm": 4000, "bottom_mm": 10000, "rock": "mudstone", "marker": "凝灰标志"},
		},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("补全分层期望 200，实际 %d: %v", res.StatusCode, body)
	}
	res, sealed := h.do("POST", "/api/v1/profiles/"+id+"/seal", map[string]any{
		"expected_version": 3, "reason": "核对完成",
	})
	if res.StatusCode != http.StatusOK || sealed["state"] != "sealed" {
		t.Fatalf("锁定期望 200/sealed，实际 %d: %v", res.StatusCode, sealed)
	}
	if profileVersion(sealed) != 4 {
		t.Fatalf("锁定后版本应为 4，实际 %v", sealed["version"])
	}

	// 锁定后写入一律 409。
	res, body = h.do("PUT", "/api/v1/profiles/"+id+"/layers", map[string]any{
		"expected_version": 4, "reason": "锁定后改层",
		"layers": []map[string]any{
			{"top_mm": 0, "bottom_mm": 10000, "rock": "limestone"},
		},
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("锁定后编辑期望 409，实际 %d: %v", res.StatusCode, body)
	}

	// 重新打开产生下一版本；历史锁定版本仍可读取。
	res, reopened := h.do("POST", "/api/v1/profiles/"+id+"/reopen", map[string]any{
		"expected_version": 4, "reason": "补充描述",
	})
	if res.StatusCode != http.StatusOK || reopened["state"] != "draft" || profileVersion(reopened) != 5 {
		t.Fatalf("重新打开期望 200/draft/v5，实际 %d: %v", res.StatusCode, reopened)
	}
	res, old := h.do("GET", "/api/v1/profiles/"+id+"/revisions/4", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("历史版本期望 200，实际 %d", res.StatusCode)
	}
	oldProfile := old["profile"].(map[string]any)
	if oldProfile["state"] != "sealed" {
		t.Fatalf("历史版本必须保持 sealed: %v", oldProfile["state"])
	}

	// 左闭右开：4000 边界点属于下方泥岩。
	res, point := h.do("GET", "/api/v1/profiles/"+id+"/at?depth_mm=4000&version=4", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("深度查询期望 200，实际 %d", res.StatusCode)
	}
	layer := point["layer"].(map[string]any)
	if layer["rock"] != "mudstone" {
		t.Fatalf("边界点 4000 应属于泥岩下层: %v", layer)
	}
}

// 同版本并发 HTTP 写入：恰好一个 2xx，其余 409。
func TestConcurrentSameVersionHTTP(t *testing.T) {
	h := newHarness(t, t.TempDir())
	p := h.createProfile("并发HTTP", 10000)
	id := profileID(p)

	payload, _ := json.Marshal(map[string]any{
		"expected_version": 1, "reason": "并发分层",
		"layers": []map[string]any{
			{"top_mm": 0, "bottom_mm": 10000, "rock": "sandstone"},
		},
	})
	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	statuses := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req, _ := http.NewRequest("PUT", h.server.URL+"/api/v1/profiles/"+id+"/layers",
				bytes.NewReader(append([]byte{}, payload...)))
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("并发请求失败: %v", err)
				return
			}
			statuses[i] = res.StatusCode
			res.Body.Close()
		}(i)
	}
	close(start)
	wg.Wait()

	ok, conflict := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		default:
			t.Fatalf("意外状态码 %d", s)
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("并发结果 200=%d 409=%d，期望 1/%d", ok, conflict, n-1)
	}
}

// 输入规则通过 HTTP 状态码约束：422 字段错误、404 资源缺失、415 类型错误。
func TestHTTPErrorMapping(t *testing.T) {
	h := newHarness(t, t.TempDir())

	// 深度越界 -> 422。
	res, _ := h.do("POST", "/api/v1/profiles", map[string]any{
		"name": "超深", "site": "赤石岭", "depth_mm": 2000000,
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("深度越界期望 422，实际 %d", res.StatusCode)
	}

	p := h.createProfile("正常剖面", 10000)
	id := profileID(p)

	// 重叠分层 -> 422，且剖面仍停留在版本 1。
	res, body := h.do("PUT", "/api/v1/profiles/"+id+"/layers", map[string]any{
		"expected_version": 1, "reason": "重叠",
		"layers": []map[string]any{
			{"top_mm": 0, "bottom_mm": 4000, "rock": "sandstone"},
			{"top_mm": 2000, "bottom_mm": 6000, "rock": "mudstone"},
		},
	})
	if res.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("重叠分层期望 422，实际 %d: %v", res.StatusCode, body)
	}
	res, current := h.do("GET", "/api/v1/profiles/"+id, nil)
	if res.StatusCode != http.StatusOK || profileVersion(current) != 1 {
		t.Fatalf("失败请求后版本必须保持 1: %v", current)
	}

	// 未知剖面 -> 404。
	res, _ = h.do("GET", "/api/v1/profiles/prf_00000000000000000000000000009999", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("未知剖面对期望 404，实际 %d", res.StatusCode)
	}

	// 错误 Content-Type -> 415。
	req, _ := http.NewRequest("POST", h.server.URL+"/api/v1/profiles",
		strings.NewReader(`{"name":"x","site":"y","depth_mm":10}`))
	req.Header.Set("Content-Type", "text/plain")
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("错误媒体类型期望 415，实际 %d", res.StatusCode)
	}
}

// 对比全链路：偏移切分、unknown 不计入 known、重复请求 200 复用。
func TestComparisonHTTPEndToEnd(t *testing.T) {
	h := newHarness(t, t.TempDir())

	sealFull := func(name string, rock string, marker string) map[string]any {
		p := h.createProfile(name, 10000)
		id := profileID(p)
		layer := map[string]any{"top_mm": 0, "bottom_mm": 10000, "rock": rock}
		if marker != "" {
			layer["marker"] = marker
		}
		res, body := h.do("PUT", "/api/v1/profiles/"+id+"/layers", map[string]any{
			"expected_version": 1, "reason": "完整分层", "layers": []any{layer},
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("分层失败: %d %v", res.StatusCode, body)
		}
		res, body = h.do("POST", "/api/v1/profiles/"+id+"/seal", map[string]any{
			"expected_version": 2, "reason": "锁定",
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("锁定失败: %d %v", res.StatusCode, body)
		}
		return body
	}

	left := sealFull("对比左", "sandstone", "凝灰标志")
	right := sealFull("对比右", "mudstone", "凝灰标志")
	payload := map[string]any{
		"left":      map[string]any{"id": profileID(left), "version": profileVersion(left)},
		"right":     map[string]any{"id": profileID(right), "version": profileVersion(right)},
		"offset_mm": -2000,
	}
	res, body := h.do("POST", "/api/v1/comparisons", payload)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("首次对比期望 201，实际 %d: %v", res.StatusCode, body)
	}
	firstID := body["id"].(string)
	if body["overlap_mm"].(float64) != 8000 || body["equal_mm"].(float64) != 0 || body["known_mm"].(float64) != 8000 {
		t.Fatalf("区间累计错误: %v", body)
	}
	if sim, ok := body["similarity"].(float64); !ok || sim != 0 {
		t.Fatalf("全不同岩性 similarity 应为 0: %v", body["similarity"])
	}

	// 重复请求 -> 200 且复用编号。
	res, again := h.do("POST", "/api/v1/comparisons", payload)
	if res.StatusCode != http.StatusOK || again["id"] != firstID {
		t.Fatalf("重复对比期望 200 + 相同编号，实际 %d: %v", res.StatusCode, again)
	}

	// CSV 可导出，表头固定。
	res, csvBody := h.doGet("GET", "/api/v1/comparisons/"+firstID+"/csv")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("CSV 期望 200，实际 %d", res.StatusCode)
	}
	if !bytes.HasPrefix(csvBody, []byte("top_mm,bottom_mm,thickness_mm,left_rock,right_rock,relation")) {
		t.Fatalf("CSV 表头错误: %s", csvBody)
	}

	// 草拟版本对比 -> 409。
	draft := h.createProfile("草拟侧", 10000)
	res, _ = h.do("POST", "/api/v1/comparisons", map[string]any{
		"left":      map[string]any{"id": profileID(draft), "version": 1},
		"right":     map[string]any{"id": profileID(right), "version": profileVersion(right)},
		"offset_mm": 0,
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("草拟版本对比期望 409，实际 %d", res.StatusCode)
	}

	// 标志层偏移建议：相同标志层所需偏移一致，中位数为 0。
	res, proposal := h.do("POST", "/api/v1/comparison-offsets", map[string]any{
		"left":  map[string]any{"id": profileID(left), "version": profileVersion(left)},
		"right": map[string]any{"id": profileID(right), "version": profileVersion(right)},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("偏移建议期望 200，实际 %d: %v", res.StatusCode, proposal)
	}
	c := proposal["comparison"].(map[string]any)
	if c["offset_mm"].(float64) != 0 {
		t.Fatalf("同深标志层建议偏移应为 0: %v", c["offset_mm"])
	}
	if proposal["ambiguous"].(bool) {
		t.Fatalf("单一一致证据不应有分歧")
	}
}

// unknown 岩性区间 similarity 为 null。
func TestUnknownSimilarityNullHTTP(t *testing.T) {
	h := newHarness(t, t.TempDir())

	sealUnknown := func(name string) map[string]any {
		p := h.createProfile(name, 10000)
		id := profileID(p)
		h.do("PUT", "/api/v1/profiles/"+id+"/layers", map[string]any{
			"expected_version": 1, "reason": "分层",
			"layers": []map[string]any{{"top_mm": 0, "bottom_mm": 10000, "rock": "unknown"}},
		})
		_, body := h.do("POST", "/api/v1/profiles/"+id+"/seal", map[string]any{
			"expected_version": 2, "reason": "锁定",
		})
		return body
	}
	left := sealUnknown("未知左")
	right := sealUnknown("未知右")
	res, body := h.do("POST", "/api/v1/comparisons", map[string]any{
		"left":      map[string]any{"id": profileID(left), "version": profileVersion(left)},
		"right":     map[string]any{"id": profileID(right), "version": profileVersion(right)},
		"offset_mm": 0,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("对比期望 201，实际 %d: %v", res.StatusCode, body)
	}
	if body["known_mm"].(float64) != 0 {
		t.Fatalf("全 unknown 时 known_mm 必须为 0")
	}
	if value, exists := body["similarity"]; !exists || value != nil {
		t.Fatalf("全 unknown 时 similarity 必须为 null，实际 %v", value)
	}
}

// 快照恢复：写入、锁定、对比之后重启进程，数据完整且相同输入复用旧结果。
func TestRecoveryAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)

	p := h.createProfile("恢复剖面", 10000)
	id := profileID(p)
	h.do("PUT", "/api/v1/profiles/"+id+"/layers", map[string]any{
		"expected_version": 1, "reason": "完整分层",
		"layers": []map[string]any{{"top_mm": 0, "bottom_mm": 10000, "rock": "sandstone"}},
	})
	_, sealed := h.do("POST", "/api/v1/profiles/"+id+"/seal", map[string]any{
		"expected_version": 2, "reason": "锁定",
	})

	other := h.createProfile("恢复剖面二", 10000)
	oid := profileID(other)
	h.do("PUT", "/api/v1/profiles/"+oid+"/layers", map[string]any{
		"expected_version": 1, "reason": "完整分层",
		"layers": []map[string]any{{"top_mm": 0, "bottom_mm": 10000, "rock": "sandstone"}},
	})
	_, sealedOther := h.do("POST", "/api/v1/profiles/"+oid+"/seal", map[string]any{
		"expected_version": 2, "reason": "锁定",
	})
	payload := map[string]any{
		"left":      map[string]any{"id": id, "version": profileVersion(sealed)},
		"right":     map[string]any{"id": oid, "version": profileVersion(sealedOther)},
		"offset_mm": 0,
	}
	_, comparison := h.do("POST", "/api/v1/comparisons", payload)
	cmpID := comparison["id"].(string)

	// 模拟进程重启。
	h.startAgain()

	res, restored := h.do("GET", "/api/v1/profiles/"+id+"/revisions/3", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("重启后历史版本读取失败: %d", res.StatusCode)
	}
	prof := restored["profile"].(map[string]any)
	if prof["state"] != "sealed" || int(prof["version"].(float64)) != 3 {
		t.Fatalf("重启后锁定历史版本不一致: %v", prof)
	}

	// 相同对比输入必须复用快照中的结果（200 + 相同编号）。
	res, reused := h.do("POST", "/api/v1/comparisons", payload)
	if res.StatusCode != http.StatusOK || reused["id"] != cmpID {
		t.Fatalf("重启后相同输入应复用结果: %d %v", res.StatusCode, reused)
	}

	// 健康检查就绪。
	res, _ = h.do("GET", "/healthz", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("重启后健康检查期望 200，实际 %d", res.StatusCode)
	}
}

// 损坏的快照必须拒绝启动（HTTP 服务无法建立）。
func TestCorruptSnapshotRejectsStartup(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)
	p := h.createProfile("待损坏", 10000)
	id := profileID(p)
	h.do("PUT", "/api/v1/profiles/"+id+"/layers", map[string]any{
		"expected_version": 1, "reason": "写入分层以生成快照",
		"layers": []map[string]any{{"top_mm": 0, "bottom_mm": 10000, "rock": "sandstone"}},
	})
	h.server.Close()
	if err := h.repo.Close(); err != nil {
		t.Fatal(err)
	}

	// 翻转 data 中的一个字节，校验和必然先于 JSON 解析失配。
	path := dir + "/strata.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(raw, []byte("sandstone"))
	if idx < 0 {
		t.Fatalf("快照中未找到可篡改的岩性字段")
	}
	raw[idx] = 'x'
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := persistence.Open(dir); err == nil {
		t.Fatalf("损坏快照必须拒绝启动")
	}
}

// 两个仓库不能同时打开同一数据目录（单进程独占）。
func TestDataDirectoryExclusiveHTTP(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)
	// 服务仍持有锁期间，另一个进程视角的 Open 必须失败。
	if _, err := persistence.Open(h.repoPath); err == nil {
		t.Fatalf("数据目录被占用时第二次打开必须失败")
	}
}

// 健康检查基础行为。
func TestHealthz(t *testing.T) {
	h := newHarness(t, t.TempDir())
	res, body := h.do("GET", "/healthz", nil)
	if res.StatusCode != http.StatusOK || body["status"] != "ready" {
		t.Fatalf("健康检查错误: %d %v", res.StatusCode, body)
	}
}
