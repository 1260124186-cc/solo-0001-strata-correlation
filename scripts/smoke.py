#!/usr/bin/env python3
"""Run bounded HTTP checks in a disposable data directory using the real server."""
import argparse
import concurrent.futures
import csv
import hashlib
import io
import json
import os
from pathlib import Path
import selectors
import signal
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parent.parent


class Server:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.binary = self.directory / 'stratad'
        args = ['go', 'build']
        if os.environ.get('STRATA_SMOKE_RACE') == '1':
            args.append('-race')
        subprocess.run(args + ['-o', str(self.binary), './cmd/stratad'], cwd=ROOT, check=True, timeout=60)
        self.process = None
        self.log = open(self.directory / 'server.log', 'w+')
        try:
            self.start()
        except BaseException:
            if self.process is not None and self.process.poll() is None:
                self.process.kill()
                self.process.wait(timeout=5)
            self.log.close()
            raise

    def start(self):
        self.process = subprocess.Popen(
            [str(self.binary), '-addr', '127.0.0.1:0', '-data', str(self.directory / 'data')],
            stdout=subprocess.PIPE, stderr=self.log, text=True,
        )
        with selectors.DefaultSelector() as selector:
            selector.register(self.process.stdout, selectors.EVENT_READ)
            if not selector.select(timeout=10):
                raise RuntimeError('server did not become ready')
            line = self.process.stdout.readline().strip()
        if not line.startswith('STRATA_LISTEN='):
            raise RuntimeError('invalid ready output: ' + line)
        self.url = 'http://' + line.partition('=')[2]
        self.call('GET', '/healthz')

    def stop(self):
        if self.process is None:
            return
        if self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
                raise RuntimeError('server did not shut down')
        if self.process.returncode != 0:
            raise RuntimeError('server failed: ' + str(self.process.returncode))
        self.process.stdout.close()
        self.process = None

    def close(self):
        try:
            self.stop()
        finally:
            if self.process and self.process.poll() is None:
                self.process.kill()
                self.process.wait(timeout=5)
            self.log.flush()
            self.log.seek(0)
            content = self.log.read()
            self.log.close()
            if 'DATA RACE' in content:
                raise RuntimeError('race detector found a race')

    def call(self, method, path, body=None, expected=200, raw=False):
        data = None if body is None else json.dumps(body, ensure_ascii=False).encode()
        request = urllib.request.Request(self.url + path, data=data, method=method,
                                         headers={'Content-Type': 'application/json'})
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            payload = response.read()
            if response.status != expected:
                raise RuntimeError(f'{method} {path}: expected {expected}, got {response.status}: {payload.decode()}')
        return payload.decode() if raw else json.loads(payload)


def create(s, name='北坡剖面', site='赤石岭', depth=10000):
    return s.call('POST', '/api/v1/profiles', dict(name=name, site=site, depth_mm=depth, note='岩层编录'), 201)


def layers(boundary=4000):
    return [dict(top_mm=0, bottom_mm=boundary, rock='sandstone', marker=''),
            dict(top_mm=boundary, bottom_mm=10000, rock='mudstone', marker='凝灰标志')]


def replace(s, p, values, expected=200):
    return s.call('PUT', f'/api/v1/profiles/{p["id"]}/layers',
                  dict(expected_version=p['version'], layers=values, reason='补充分层记录'), expected)


def state(s, p, action, expected=200):
    return s.call('POST', f'/api/v1/profiles/{p["id"]}/{action}',
                  dict(expected_version=p['version'], reason='完成岩层核对'), expected)


def sealed(s, name, boundary=4000):
    return state(s, replace(s, create(s, name), layers(boundary)), 'seal')


def rewrite_snapshot(snapshot, state_obj):
    """重写快照信封。Go 校验 digest 针对 data 的原始 JSON 字节，而
    envelope 再次序列化时会把 data 作为对象嵌入，两次序列化必须产生
    完全相同的字节（紧凑、不转义非 ASCII），否则校验值失配。"""
    raw = json.dumps(state_obj, ensure_ascii=False, separators=(',', ':'))
    env_text = '{"digest":"' + hashlib.sha256(raw.encode()).hexdigest() + '","data":' + raw + '}'
    snapshot.write_text(env_text)


def record(s):
    p = create(s)
    assert p['version'] == 1 and p['layers'] == []
    s.call('POST', '/api/v1/profiles', dict(name='空', site='地点', depth_mm=0), 422)
    s.call('POST', '/api/v1/profiles', dict(name='空', site='地点', depth_mm=10, extra=True), 422)
    p = replace(s, p, [dict(top_mm=1000, bottom_mm=9000, rock='sandstone')])
    coverage = s.call('GET', f'/api/v1/profiles/{p["id"]}/coverage')['coverage']
    assert coverage['gaps'] == [dict(top_mm=0, bottom_mm=1000), dict(top_mm=9000, bottom_mm=10000)]
    point = s.call('GET', f'/api/v1/profiles/{p["id"]}/at?depth_mm=500')
    assert point['layer'] is None and point['gap']['bottom_mm'] == 1000
    bad = [dict(top_mm=0, bottom_mm=6000, rock='shale'), dict(top_mm=5000, bottom_mm=10000, rock='mudstone')]
    replace(s, p, bad, 422)
    current = s.call('GET', f'/api/v1/profiles/{p["id"]}')
    assert current == p
    state(s, p, 'seal', 409)
    p = replace(s, p, list(reversed(layers())))
    assert p['layers'][0]['top_mm'] == 0
    point = s.call('GET', f'/api/v1/profiles/{p["id"]}/at?depth_mm=4000')
    assert point['layer']['rock'] == 'mudstone' and point['distance_from_top_mm'] == 0
    s.call('GET', f'/api/v1/profiles/{p["id"]}/at?depth_mm=10000', expected=422)
    body = dict(expected_version=p['version'], metadata=dict(name=p['name'], site=p['site'], depth_mm=3000), reason='调整深度')
    s.call('PUT', f'/api/v1/profiles/{p["id"]}', body, 422)
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == p
    s.process.kill()
    s.process.wait(timeout=5)
    s.process.stdout.close()
    s.process = None
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == p
    s.stop()
    snapshot = s.directory / 'data' / 'strata.json'
    original = snapshot.read_bytes()
    damaged = json.loads(original)
    damaged['digest'] = '0' * 64
    snapshot.write_text(json.dumps(damaged))
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and b'checksum mismatch' in attempt.stderr
    snapshot.write_bytes(original)
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == p


def seal(s):
    p = sealed(s, '锁定剖面')
    replace(s, p, layers(), 409)
    locked = s.call('GET', f'/api/v1/profiles/{p["id"]}/revisions/3')
    p = state(s, p, 'reopen')
    state(s, p, 'reopen', 409)
    def edit(name):
        body = dict(expected_version=p['version'], metadata=dict(name=name, site=p['site'], depth_mm=p['depth_mm']), reason='修订名称')
        request = urllib.request.Request(s.url+f'/api/v1/profiles/{p["id"]}', method='PUT',
                    data=json.dumps(body).encode(), headers={'Content-Type': 'application/json'})
        try:
            response = urllib.request.urlopen(request, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            response.read()
            return response.status
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        codes = list(pool.map(edit, ['东岭剖面', '西岭剖面']))
    assert sorted(codes) == [200, 409], codes
    current = s.call('GET', f'/api/v1/profiles/{p["id"]}')
    assert current['version'] == 5
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}/revisions/3') == locked
    history = s.call('GET', f'/api/v1/profiles/{p["id"]}/history?offset=2&limit=2')
    assert history['total'] == 5 and [x['action'] for x in history['items']] == ['seal', 'reopen']
    diff = s.call('GET', f'/api/v1/profiles/{p["id"]}/diff?from=3&to=5')
    assert {f['field'] for f in diff['fields']} >= {'state', 'name'}
    assert diff['layers'] == []
    attempt = subprocess.run([str(s.binary), '-addr', '127.0.0.1:0', '-data', str(s.directory/'data')], capture_output=True, timeout=10)
    assert attempt.returncode != 0 and b'already in use' in attempt.stderr
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/profiles/{p["id"]}') == current


def compare(s):
    a, b = sealed(s, '西侧剖面'), sealed(s, '东侧剖面', 6000)
    request = dict(left=dict(id=a['id'], version=3), right=dict(id=b['id'], version=3), offset_mm=0)
    result = s.call('POST', '/api/v1/comparisons', request, 201)
    assert result['overlap_mm'] == 10000 and result['equal_mm'] == 8000 and result['similarity'] == .8
    assert s.call('POST', '/api/v1/comparisons', request) == result
    rows = list(csv.DictReader(io.StringIO(s.call('GET', f'/api/v1/comparisons/{result["id"]}/csv', raw=True))))
    assert len(rows) == 3 and sum(int(r['thickness_mm']) for r in rows) == 10000
    proposal = s.call('POST', '/api/v1/comparison-offsets', dict(left=request['left'], right=request['right']))
    assert proposal['comparison']['offset_mm'] == -2000 and not proposal['ambiguous']
    aligned = s.call('POST', '/api/v1/comparisons', proposal['comparison'], 201)
    assert aligned['similarity'] == 1 and aligned['overlap_mm'] == 8000
    s.call('POST', '/api/v1/comparisons', {**request, 'offset_mm': 10000}, 409)
    s.call('POST', '/api/v1/comparisons', {**request, 'left': dict(id=a['id'], version=2)}, 409)
    c = state(s, replace(s, create(s, '待识别岩性'), [dict(top_mm=0, bottom_mm=10000, rock='unknown')]), 'seal')
    unknown = s.call('POST', '/api/v1/comparisons', {**request, 'right': dict(id=c['id'], version=3)}, 201)
    assert unknown['known_mm'] == 0 and unknown['similarity'] is None
    state(s, a, 'reopen')
    assert s.call('POST', '/api/v1/comparisons', request) == result
    s.stop()
    s.start()
    assert s.call('GET', f'/api/v1/comparisons/{result["id"]}') == result


def browse(s):
    a = sealed(s, '赤石北剖面')
    b = create(s, '赤石南剖面')
    create(s, '远山剖面', '青岩岭')
    q = urllib.parse.urlencode(dict(q='赤石', site='赤石岭', state='draft', limit=1))
    page = s.call('GET', '/api/v1/profiles?'+q)
    assert page['total'] == 1 and page['items'][0]['id'] == b['id']
    page = s.call('GET', '/api/v1/profiles?rock=mudstone')
    assert page['total'] == 1 and page['items'][0]['id'] == a['id']
    assert s.call('GET', '/api/v1/profiles?offset=100')['items'] == []
    for query in ['limit=0', 'limit=no', 'limit=1&limit=2', 'state=absent', 'rock=absent', 'extra=1']:
        s.call('GET', '/api/v1/profiles?'+query, expected=422)
    assert len(s.call('GET', '/api/v1/rocks')['items']) == 6
    s.call('GET', '/api/v1/profiles/prf_'+'0'*32, expected=404)
    s.call('GET', f'/api/v1/profiles/{a["id"]}/revisions/500', expected=404)
    assert s.call('GET', f'/api/v1/comparisons?profile_id={a["id"]}')['items'] == []
    diff = s.call('GET', f'/api/v1/profiles/{a["id"]}/diff?from=1&to=3')
    assert len(diff['layers']) == 2


def batch(s):
    a = create(s, '批量北剖面')
    b = create(s, '批量南剖面')
    locked_p = sealed(s, '批量锁定剖面')
    # 条目覆盖：成功的元数据/分层修订、版本冲突、锁定状态冲突、不存在的剖面、
    # 形状错误。后面成功条目必须不受前面失败影响。
    items = [
        dict(profile_id=a['id'], action='metadata', expected_version=1,
             metadata=dict(name='批量北剖面-改', site=a['site'], depth_mm=a['depth_mm'])),
        dict(profile_id=b['id'], action='layers', expected_version=1, layers=layers()),
        dict(profile_id=a['id'], action='metadata', expected_version=1,
             metadata=dict(name='过期版本', site=a['site'], depth_mm=a['depth_mm'])),
        dict(profile_id=locked_p['id'], action='layers', expected_version=locked_p['version'], layers=layers()),
        dict(profile_id='prf_' + '0' * 32, action='seal', expected_version=1),
        dict(profile_id=b['id'], action='seal', expected_version=2),
        dict(profile_id=a['id'], action='bogus', expected_version=2),
        dict(profile_id=a['id'], action='reopen', expected_version=2),
    ]
    result = s.call('POST', '/api/v1/profile-revisions/batch',
                    dict(reason='批量修订批次', items=items), 201)
    assert result['status'] == 'completed', result
    statuses = [(x['index'], x['status']) for x in result['items']]
    assert statuses == [(0, 'success'), (1, 'success'), (2, 'failed'),
                        (3, 'failed'), (4, 'failed'), (5, 'success'),
                        (6, 'failed'), (7, 'failed')], statuses
    assert [x['version'] for x in result['items'] if x['status'] == 'success'] == [2, 2, 3]
    assert {x['error_code'] for x in result['items'] if x['status'] == 'failed'} == {'conflict', 'missing', 'invalid'}
    failed_version = next(x for x in result['items'] if x['index'] == 2)
    assert failed_version['error_code'] == 'conflict' and '预期版本' in failed_version['error']
    # GET 台账与 POST 响应一致
    fetched = s.call('GET', f"/api/v1/profile-revisions/batch/{result['id']}")
    assert fetched == result
    # 单份原子性：成功条目落盘，冲突/形状错误的剖面保持原样
    cur_a = s.call('GET', f'/api/v1/profiles/{a["id"]}')
    assert cur_a['version'] == 2 and cur_a['name'] == '批量北剖面-改' and cur_a['layers'] == []
    cur_b = s.call('GET', f'/api/v1/profiles/{b["id"]}')
    assert cur_b['version'] == 3 and cur_b['state'] == 'sealed'
    assert [x['action'] for x in s.call('GET', f'/api/v1/profiles/{b["id"]}/history')['items']] == ['create', 'layers', 'seal']
    cur_locked = s.call('GET', f'/api/v1/profiles/{locked_p["id"]}')
    assert cur_locked['version'] == locked_p['version']
    # 请求级形状错误整体拒绝；条目级形状错误只让该条目失败
    s.call('POST', '/api/v1/profile-revisions/batch', dict(reason='空批', items=[]), 422)
    s.call('POST', '/api/v1/profile-revisions/batch', dict(reason='缺数组'), 422)
    s.call('POST', '/api/v1/profile-revisions/batch',
           dict(reason='条目过多', items=[dict(profile_id=a['id'], action='seal', expected_version=1)] * 201), 422)
    s.call('POST', '/api/v1/profile-revisions/batch',
           dict(reason='x' * 501, items=[dict(profile_id=a['id'], action='seal', expected_version=1)]), 422)
    malformed = s.call('POST', '/api/v1/profile-revisions/batch', dict(
        reason='载荷缺失', items=[
            dict(profile_id=a['id'], action='metadata', expected_version=2),
            dict(profile_id=a['id'], action='weird', expected_version=2),
            dict(profile_id='nope', action='seal', expected_version=1),
        ]), 201)
    assert malformed['status'] == 'completed'
    assert [x['status'] for x in malformed['items']] == ['failed', 'failed', 'failed']
    assert [x['error_code'] for x in malformed['items']] == ['invalid', 'invalid', 'invalid']
    s.call('GET', '/api/v1/profile-revisions/batch/bat_' + '0' * 32, expected=404)
    # 完成的批次重启后仍可查，剖面修订不丢
    s.stop()
    s.start()
    assert s.call('GET', f"/api/v1/profile-revisions/batch/{result['id']}") == result
    assert s.call('GET', f'/api/v1/profiles/{b["id"]}')['version'] == 3
    # 模拟崩溃发生在批次条目之间：真实崩溃点上，已提交的只有条目 0，
    # 后续条目的剖面修订根本不在快照中。构造方式：批次截断为 4 项，
    # 仅条目 0 保留 success，1–3 退回 pending；同时回滚 b 的历史到
    # 修订前（它的 layers/seal 修订来自条目 1/5，尚未提交）。
    s.stop()
    snapshot = s.directory / 'data' / 'strata.json'
    state = json.loads(snapshot.read_text())['data']
    batch = state['batches'][result['id']]
    assert batch['status'] == 'completed'
    batch['status'] = 'running'
    del batch['items'][4:]
    for item in batch['items'][1:]:
        item['status'] = 'pending'
        item['error_code'] = ''
        item['error'] = ''
        item['result_version'] = 0
    state['histories'][b['id']] = state['histories'][b['id']][:1]
    rewrite_snapshot(snapshot, state)
    s.start()
    recovered = s.call('GET', f"/api/v1/profile-revisions/batch/{result['id']}")
    assert recovered['status'] == 'interrupted', recovered
    assert recovered['items'][0]['status'] == 'success'
    assert len(recovered['items']) == 4
    assert all(x['status'] == 'not_attempted' and x['error_code'] == 'interrupted'
               for x in recovered['items'][1:]), recovered
    # 恢复写回的快照本身必须可再次打开（在任何新写入之前验证）
    s.stop()
    s.start()
    assert s.call('GET', f"/api/v1/profile-revisions/batch/{result['id']}")['status'] == 'interrupted'
    # 崩溃时只提交了条目 0：b 的分层修订必须不存在
    cur_b_after = s.call('GET', f'/api/v1/profiles/{b["id"]}')
    assert cur_b_after['version'] == 1 and cur_b_after['state'] == 'draft', cur_b_after
    # a 的元数据修订（条目 0）保留且可解释
    cur_a_after = s.call('GET', f'/api/v1/profiles/{a["id"]}')
    assert cur_a_after['version'] == 2 and cur_a_after['name'] == '批量北剖面-改'
    # 恢复后服务仍可继续接受新的批量修订
    again = s.call('POST', '/api/v1/profile-revisions/batch', dict(
        reason='恢复后批次', items=[dict(profile_id=a['id'], action='metadata', expected_version=2,
        metadata=dict(name='批量北剖面-再改', site=a['site'], depth_mm=a['depth_mm']))]), 201)
    assert again['status'] == 'completed' and again['items'][0]['status'] == 'success'

    # 崩溃在最后条目已提交、完成标记尚未写入之间：running 且无 pending，
    # 恢复必须终结为 completed（修订不丢、无 not_attempted）。
    s.stop()
    snapshot = s.directory / 'data' / 'strata.json'
    state2 = json.loads(snapshot.read_text())['data']
    state2['batches'][again['id']]['status'] = 'running'
    rewrite_snapshot(snapshot, state2)
    s.start()
    fixed = s.call('GET', f"/api/v1/profile-revisions/batch/{again['id']}")
    assert fixed['status'] == 'completed' and fixed['items'][0]['status'] == 'success'
    assert s.call('GET', f'/api/v1/profiles/{a["id"]}')['name'] == '批量北剖面-再改'


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('workflow', choices=['record', 'seal', 'compare', 'browse', 'batch', 'all'])
    args = parser.parse_args()
    names = ['record', 'seal', 'compare', 'browse', 'batch'] if args.workflow == 'all' else [args.workflow]
    for name in names:
        with tempfile.TemporaryDirectory(prefix='strata-smoke-') as directory:
            server = Server(directory)
            try:
                globals()[name](server)
            finally:
                server.close()
            print(f'{name}: HTTP workflow passed')


if __name__ == '__main__':
    main()
